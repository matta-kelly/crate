// Package router runs the machine's router, one Caddy container, and serves a workspace's routes.
package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/matta-kelly/crate/internal/limit"
	"github.com/matta-kelly/crate/internal/machine"
)

// image is the router's pinned Caddy, the copy's image.
const image = "docker.io/library/caddy:2.10"

// remakeFailed is the router's mark that a remake left no router, so the next make says so once.
const remakeFailed = "remake-failed"

// caddyfile is the router's Caddyfile, the copy's: every route file imported into one site, and the 404 after.
const caddyfile = `{
	admin localhost:2019
	auto_https off
}
:80 {
	import /etc/caddy/routes/*.caddy
	handle {
		respond "no such workspace" 404
	}
}
`

// Route is one of a workspace's routes: Name is the label under the workspace's name, Target is the network
// alias of the container that answers, and Port is the port on that container.
type Route struct {
	Name   string
	Target string
	Port   int
}

// Router is the machine's router: one container, made on demand.
type Router struct {
	m machine.Machine
}

// New is the router on the machine m. It reads its name, port and state folder from WORKSPACE_ROUTER,
// WORKSPACE_ROUTER_PORT and WORKSPACE_ROUTER_STATE.
func New(m machine.Machine) *Router { return &Router{m: m} }

// config is what the router reads of the environment, the same the copy's workspace.sh reads.
type config struct {
	name  string
	port  int
	state string
}

func (r *Router) config() (config, error) {
	c := config{name: "workspace-router", port: 80}
	if v := os.Getenv("WORKSPACE_ROUTER"); v != "" {
		c.name = v
	}
	if v := os.Getenv("WORKSPACE_ROUTER_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return config{}, fmt.Errorf("WORKSPACE_ROUTER_PORT is %q: expected a port number", v)
		}
		c.port = n
	}
	c.state = os.Getenv("WORKSPACE_ROUTER_STATE")
	if c.state == "" {
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return config{}, fmt.Errorf("the router's state folder: %w", err)
			}
			base = filepath.Join(home, ".local", "state")
		}
		c.state = filepath.Join(base, "workspace-router")
	}
	return c, nil
}

// Add serves workspace's routes on network, the first also at <workspace>.localhost. The same list changes
// nothing; another list replaces the old one.
func (r *Router) Add(workspace, network string, routes []Route) error {
	c, err := r.config()
	if err != nil {
		return err
	}
	if err := prepare(c); err != nil {
		return err
	}
	if err := r.ensure(c, network); err != nil {
		return err
	}
	path := filepath.Join(c.state, "routes", workspace+".caddy")
	body := []byte(render(workspace, routes))
	old, readErr := os.ReadFile(path)
	if readErr == nil && bytes.Equal(old, body) {
		return nil
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return err
	}
	if err := r.reload(c); err != nil {
		var restore error
		if readErr == nil {
			restore = os.WriteFile(path, old, 0o644)
		} else {
			restore = os.Remove(path)
		}
		return errors.Join(err, restore)
	}
	return nil
}

// Remove takes workspace's routes away and takes the router off network.
func (r *Router) Remove(workspace, network string) error {
	c, err := r.config()
	if err != nil {
		return err
	}
	path := filepath.Join(c.state, "routes", workspace+".caddy")
	switch err := os.Remove(path); {
	case err == nil:
		if err := r.reload(c); err != nil {
			return err
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if !r.exists(c.name) {
		return nil
	}
	networks, err := r.networks(c.name)
	if err != nil {
		return err
	}
	if slices.Contains(networks, network) {
		if _, err := r.podman("network", "disconnect", network, c.name); err != nil {
			return err
		}
	}
	return nil
}

// URLs is each of workspace's routes' URL, in order, the first route also at the workspace's own host.
func (r *Router) URLs(workspace string) ([]string, error) {
	c, err := r.config()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(c.state, "routes", workspace+".caddy"))
	if err != nil {
		return nil, err
	}
	own := workspace + ".localhost"
	var hosts []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "host" || !strings.HasPrefix(fields[0], "@") {
			continue
		}
		for _, host := range fields[2:] {
			if host == own || strings.HasSuffix(host, "."+own) {
				hosts = append(hosts, host)
			}
		}
	}
	if len(hosts) == 0 {
		return nil, nil
	}
	urls := make([]string, 0, len(hosts))
	for _, host := range hosts {
		urls = append(urls, url(host, c.port))
	}
	return urls, nil
}

func url(host string, port int) string {
	if port == 80 {
		return "http://" + host
	}
	return "http://" + host + ":" + strconv.Itoa(port)
}

// prepare makes the state folder the copy expects: its three folders and its Caddyfile.
func prepare(c config) error {
	for _, dir := range []string{"routes", "data", "config"} {
		if err := os.MkdirAll(filepath.Join(c.state, dir), 0o755); err != nil {
			return err
		}
	}
	path := filepath.Join(c.state, "Caddyfile")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.WriteFile(path, []byte(caddyfile), 0o644)
}

// ensure leaves the router made, started and on network. A router the copy made, with a CPU limit, is made
// again in this machine's slice, once, on every network the old one had joined.
func (r *Router) ensure(c config, network string) error {
	if _, err := r.podman("network", "exists", c.name); err != nil {
		if _, err := r.podman("network", "create", c.name); err != nil {
			return err
		}
	}
	if !r.exists(c.name) {
		if err := r.make(c, []string{c.name, network}); err != nil {
			return err
		}
		r.sayRemade(c)
		return nil
	}
	cpus, err := r.podman("inspect", c.name, "--format", "{{.HostConfig.NanoCpus}}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(cpus) != "0" {
		networks, err := r.networks(c.name)
		if err != nil {
			return err
		}
		if _, err := r.podman("rm", "-f", c.name); err != nil {
			return err
		}
		mark := filepath.Join(c.state, remakeFailed)
		if err := os.WriteFile(mark, nil, 0o644); err != nil {
			return err
		}
		if err := r.make(c, append(networks, network)); err != nil {
			return err
		}
		os.Remove(mark)
		return nil
	}
	if _, err := r.podman("start", c.name); err != nil {
		return err
	}
	networks, err := r.networks(c.name)
	if err != nil {
		return err
	}
	if !slices.Contains(networks, network) {
		if _, err := r.podman("network", "connect", network, c.name); err != nil {
			return err
		}
	}
	return nil
}

// sayRemade is the word after a make that followed a failed remake: once, then the mark is gone.
func (r *Router) sayRemade(c config) {
	mark := filepath.Join(c.state, remakeFailed)
	if _, err := os.Stat(mark); err != nil {
		return
	}
	os.Remove(mark)
	fmt.Fprintln(os.Stderr, "crate: the router was gone after a failed remake: made it again")
}

// make runs the router's container, placed through the machine and on every network, with the copy's port and
// mounts.
func (r *Router) make(c config, networks []string) error {
	placement, err := r.m.Place(machine.Work, machine.Router)
	if err != nil {
		return err
	}
	args := []string{"run", "-d", "--name", c.name}
	seen := map[string]bool{}
	for _, network := range networks {
		if network == "" || seen[network] {
			continue
		}
		seen[network] = true
		args = append(args, "--network", network)
	}
	if placement != "" {
		args = append(args, "--cgroup-parent", string(placement))
	}
	args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:80", c.port))
	for _, mount := range []string{"Caddyfile:/etc/caddy/Caddyfile", "routes:/etc/caddy/routes", "data:/data", "config:/config"} {
		source, target, _ := strings.Cut(mount, ":")
		args = append(args, "-v", filepath.Join(c.state, source)+":"+target+":Z")
	}
	args = append(args, image)
	_, err = r.podman(args...)
	return err
}

// reload asks the router to take its route files, and tries again until RouterReload passes.
func (r *Router) reload(c config) error {
	deadline := time.Now().Add(limit.RouterReload())
	for {
		_, err := call("RouterReload", limit.RouterReload(),
			"exec", c.name, "caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile")
		if err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			return err
		}
		wait := min(500*time.Millisecond, time.Until(deadline))
		time.Sleep(wait)
	}
}

// exists says whether the container is there, without failing when it is not.
func (r *Router) exists(name string) bool {
	_, err := r.podman("container", "exists", name)
	return err == nil
}

// networks is the networks the router has joined.
func (r *Router) networks(name string) ([]string, error) {
	out, err := r.podman("inspect", name, "--format",
		"{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

func (r *Router) podman(args ...string) (string, error) {
	return call("RouterPodman", limit.RouterPodman(), args...)
}

// call runs podman with a limit, and fails with podman's message, or with the limit's name when it passes.
func call(limitName string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "podman", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return stdout.String(), fmt.Errorf("podman %s: %s passed after %v",
			strings.Join(args, " "), limitName, timeout)
	}
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return stdout.String(), fmt.Errorf("podman %s: %s", strings.Join(args, " "), message)
	}
	return stdout.String(), nil
}

// render is a workspace's route file: each route's hosts, the first route also at the workspace's own host.
func render(workspace string, routes []Route) string {
	var b strings.Builder
	for i, route := range routes {
		matcher := matcher(workspace, i)
		hosts := route.Name + "." + workspace + ".localhost"
		if i == 0 {
			hosts = workspace + ".localhost " + hosts
		}
		fmt.Fprintf(&b, "%s host %s\nhandle %s {\n\treverse_proxy %s:%d\n}\n",
			matcher, hosts, matcher, route.Target, route.Port)
	}
	return b.String()
}

// matcher is a route's matcher name in the one site: the workspace's letters and digits, and the route's place.
func matcher(workspace string, i int) string {
	var b strings.Builder
	for _, r := range workspace {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return fmt.Sprintf("@%s_%d", b.String(), i)
}
