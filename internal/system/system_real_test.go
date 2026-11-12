package system_test

// The system's tests on real podman. Each world is a checkout of a tiny git repo with its own crate.toml, and
// every network, pod, container and volume it makes starts with the world's name, which is unique to this run.
// A world is removed at the end of its test, so one test never sees another's parts.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine/machinetest"
	"github.com/matta-kelly/crate/internal/system"
)

const (
	alpine  = "docker.io/library/alpine:latest"
	timeout = 5 * time.Minute
)

var serial atomic.Int64

// worldOpts says what a world's crate.toml and services file hold.
type worldOpts struct {
	Kind     string
	Name     string   // the system's name, when a test needs its own; else one of this run's
	Setup    string   // [tools] setup, or "" for none
	Services bool     // a .devcontainer/pod.yaml with one Pod, WORKSPACE-postgres
	Mounts   []system.Mount
	Env      []string
	ToolsEnv map[string]string // [tools] env
	NoTools  bool              // no [tools] table at all
	Slice    string            // the recorder's placement
	Keep     []string
	BadVolumes bool // [tools] volumes is a string, not a list
}

type world struct {
	t    *testing.T
	dir  string
	root string
	name string
	out  bytes.Buffer
	rec  *machinetest.Recorder
	sys  *system.System
}

func newWorld(t *testing.T, o worldOpts) *world {
	t.Helper()
	if _, err := exec.LookPath("podman"); err != nil {
		t.Fatalf("these tests run on real podman: %v", err)
	}
	n := serial.Add(1)
	w := &world{t: t, dir: t.TempDir()}
	w.name = o.Name
	if w.name == "" {
		w.name = fmt.Sprintf("cratetest-%d-%d", os.Getpid(), n)
	}
	w.root = filepath.Join(w.dir, "checkout")
	for _, d := range []string{w.root, filepath.Join(w.root, ".devcontainer"), filepath.Join(w.root, "src")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	setup := o.Setup
	services := ""
	if o.Services {
		services = `services = ".devcontainer/pod.yaml"`
	}
	tools := ""
	if !o.NoTools {
		setupLine := ""
		if setup != "" {
			setupLine = "setup = " + strconv.Quote(setup)
		}
		volumes := `["node_modules"]`
		if o.BadVolumes {
			volumes = `"node_modules"`
		}
		tools = fmt.Sprintf("[tools]\n%s\nvolumes = %s\n", setupLine, volumes)
		if len(o.ToolsEnv) > 0 {
			tools += "[tools.env]\n"
			for key, value := range o.ToolsEnv {
				tools += fmt.Sprintf("%s = %s\n", key, strconv.Quote(value))
			}
		}
	}
	w.write("crate.toml", fmt.Sprintf(`name = %s
%s

[dev]
image = "docker.io/library/alpine"
containerfile = ".devcontainer/Containerfile"
inputs = [".devcontainer/Containerfile", "package-lock.json"]

%s`, strconv.Quote(w.name), services, tools))
	w.write(".devcontainer/Containerfile", "FROM "+alpine+"\n")
	w.write("package-lock.json", "lock\n")
	w.write("src/README.md", "readme\n")
	if o.Services {
		w.write(".devcontainer/pod.yaml", servicesFile())
	}
	w.git("init", "-q", "-b", "main")
	w.git("add", "-A")
	w.git("commit", "-qm", "init")
	w.tagDevImage()
	w.rec = &machinetest.Recorder{Slice: o.Slice}
	w.sys = system.New(w.rec, system.Spec{
		Kind:     o.Kind,
		Name:     w.name,
		Checkout: w.root,
		Mounts:   o.Mounts,
		Env:      o.Env,
		Keep:     o.Keep,
		Out:      &w.out,
	})
	t.Cleanup(func() {
		_ = w.sys.Remove(nil)
		w.sweep()
	})
	return w
}

// servicesFile is one Pod named WORKSPACE-postgres with a volume WORKSPACE-pgdata, serving "ok" on 5432.
func servicesFile() string {
	return `apiVersion: v1
kind: Pod
metadata:
  name: WORKSPACE-postgres
spec:
  restartPolicy: Never
  containers:
    - name: postgres
      image: docker.io/library/busybox:latest
      command: ["sh", "-c", "echo service-up; echo ok >/tmp/index.html; exec httpd -f -p 5432 -h /tmp"]
      volumeMounts:
        - {name: WORKSPACE-pgdata, mountPath: /var/lib/postgresql/data}
  volumes:
    - name: WORKSPACE-pgdata
      emptyDir: {}
`
}

func (w *world) write(path, body string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.root, path), []byte(body), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) git(args ...string) {
	w.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = w.root
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		w.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// tagDevImage puts the small alpine image in this machine's store under the tag `crate dev-image local` reads,
// so a test makes no build. The tag is the hash dev-image gives the [dev] inputs, worked out here so that the
// fixture needs no declaration parser.
func (w *world) tagDevImage() {
	w.t.Helper()
	cmd := exec.Command("git", "ls-tree", "-r", "HEAD", "--", ".devcontainer/Containerfile", "package-lock.json")
	cmd.Dir = w.root
	listing, err := cmd.Output()
	if err != nil {
		w.t.Fatalf("git ls-tree: %v", err)
	}
	sum := sha256.Sum256(listing)
	tag := "docker.io/library/alpine:" + hex.EncodeToString(sum[:])[:16]
	if _, err := w.podman("image", "exists", alpine); err != nil {
		w.podmanOK("pull", "-q", alpine)
	}
	w.podmanOK("tag", alpine, tag)
}

// up holds, images and makes the system.
func (w *world) up() {
	w.t.Helper()
	if err := w.sys.Hold(); err != nil {
		w.t.Fatalf("Hold: expected a slot, got %v", err)
	}
	if err := w.sys.Image(); err != nil {
		w.t.Fatalf("Image: expected the dev image on this machine, got %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := w.sys.Up(ctx); err != nil {
		w.t.Fatalf("Up: expected the system, got %v\noutput:\n%s", err, w.out.String())
	}
}

// result is one command run in the tools member.
type result struct {
	out  string
	code int
	err  error
}

func (w *world) run(argv ...string) result {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out bytes.Buffer
	code, err := w.sys.Run(ctx, argv, &out)
	return result{out: out.String(), code: code, err: err}
}

// mustRun runs argv and requires exit 0.
func (w *world) mustRun(argv ...string) string {
	w.t.Helper()
	r := w.run(argv...)
	if r.err != nil || r.code != 0 {
		w.t.Fatalf("run %v: expected exit 0, got exit %d, error %v, output %q", argv, r.code, r.err, r.out)
	}
	return r.out
}

func (w *world) podman(args ...string) (string, error) {
	cmd := exec.Command("podman", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (w *world) podmanOK(args ...string) string {
	w.t.Helper()
	out, err := w.podman(args...)
	if err != nil {
		w.t.Fatalf("podman %v: %v\n%s", args, err, out)
	}
	return out
}

// leftovers are the containers, pods, volumes and networks of this world that podman still holds.
func (w *world) leftovers() []string {
	w.t.Helper()
	var found []string
	for _, ask := range [][]string{
		{"ps", "-a", "--format", "{{.Names}}"},
		{"pod", "ls", "--format", "{{.Name}}"},
		{"volume", "ls", "--format", "{{.Name}}"},
		{"network", "ls", "--format", "{{.Name}}"},
	} {
		out, err := w.podman(ask...)
		if err != nil {
			w.t.Fatalf("podman %v: %v\n%s", ask, err, out)
		}
		for _, name := range strings.Fields(out) {
			if strings.HasPrefix(name, w.name) {
				found = append(found, name)
			}
		}
	}
	return found
}

func (w *world) sweep() {
	for _, ask := range [][]string{
		{"ps", "-a", "--format", "{{.Names}}"},
		{"pod", "ls", "--format", "{{.Name}}"},
		{"volume", "ls", "--format", "{{.Name}}"},
		{"network", "ls", "--format", "{{.Name}}"},
	} {
		out, err := w.podman(ask...)
		if err != nil {
			continue
		}
		for _, name := range strings.Fields(out) {
			if !strings.HasPrefix(name, w.name) {
				continue
			}
			switch ask[0] {
			case "ps":
				_, _ = w.podman("rm", "-f", "-t", "0", name)
			case "pod":
				_, _ = w.podman("pod", "rm", "-f", name)
			case "volume":
				_, _ = w.podman("volume", "rm", "-f", name)
			case "network":
				_, _ = w.podman("network", "rm", "-f", name)
			}
		}
	}
}

func (w *world) mustLeaveNothing() {
	w.t.Helper()
	for _, left := range w.leftovers() {
		w.t.Errorf("after the system is gone: expected nothing, found %q", left)
	}
}

// A service's address, for the test that a workspace does not reach another's.
func (w *world) serviceIP(service string) string {
	w.t.Helper()
	pod := w.name + "-" + service
	infra := w.podmanOK("pod", "inspect", pod, "--format", "{{.InfraContainerID}}")
	return w.podmanOK("inspect", infra, "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
}

// A [tools] volumes that is not a list is refused, and names the key.
func TestAVolumesThatIsNotAListIsRefused(t *testing.T) {
	w := newWorld(t, worldOpts{Kind: system.Work, BadVolumes: true})
	if err := w.sys.Hold(); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := w.sys.Up(ctx)
	if err == nil || !strings.Contains(err.Error(), "volumes") {
		t.Fatalf("Up with a volumes that is a string: expected an error that names the key volumes, got %v", err)
	}
	if left := w.leftovers(); len(left) > 0 {
		t.Errorf("after the refusal: expected nothing started, found %q", left)
	}
}

// A repo with no [tools] still gets the node_modules volume, and a [tools] env value reaches the member.
func TestNoToolsStillGetsNodeModulesAndToolsEnvReachesTheMember(t *testing.T) {
	t.Run("no [tools]", func(t *testing.T) {
		w := newWorld(t, worldOpts{Kind: system.Work, NoTools: true})
		w.up()
		w.podmanOK("volume", "inspect", w.name+"-node-modules")
		w.mustRun("test", "-d", "node_modules")
	})
	t.Run("a [tools] with env", func(t *testing.T) {
		w := newWorld(t, worldOpts{Kind: system.Work, ToolsEnv: map[string]string{"FROM_TOOLS": "yes"}})
		w.up()
		if got := w.mustRun("printenv", "FROM_TOOLS"); strings.TrimSpace(got) != "yes" {
			t.Errorf("the tools env: expected yes, got %q", got)
		}
	})
}

// --- its own services ---

// Two checkouts of one repo each reach their own postgres by name, and neither reaches the other's by name or
// address. Each has its own <system>-pgdata, and down removes each.
func TestWorkspacesReachTheirOwnServicesAndNotAnothers(t *testing.T) {
	a := newWorld(t, worldOpts{Kind: system.Work, Services: true})
	b := newWorld(t, worldOpts{Kind: system.Work, Services: true})
	a.up()
	b.up()

	for _, w := range []*world{a, b} {
		if got := w.mustRun("wget", "-qO-", "http://postgres:5432/"); !strings.Contains(got, "ok") {
			t.Errorf("%s: its own postgres: expected \"ok\", got %q", w.name, got)
		}
		w.podmanOK("volume", "inspect", w.name+"-pgdata")
	}
	if a.name+"-pgdata" == b.name+"-pgdata" {
		t.Fatalf("the two volumes: expected two names, both are %q", a.name+"-pgdata")
	}
	if ip := b.serviceIP("postgres"); ip == "" {
		t.Fatal("the other workspace's postgres: expected an address")
	} else if r := a.run("wget", "-T", "3", "-qO-", "http://"+ip+":5432/"); r.err == nil && r.code == 0 {
		t.Errorf("reaching the other workspace by address %s: expected a failure, got %q", ip, r.out)
	}
	if r := a.run("wget", "-T", "3", "-qO-", "http://"+b.name+":5432/"); r.err == nil && r.code == 0 {
		t.Errorf("reaching the other workspace by name %s: expected a failure, got %q", b.name, r.out)
	}

	if err := a.sys.Remove(nil); err != nil {
		t.Fatalf("Remove %s: %v", a.name, err)
	}
	if err := b.sys.Remove(nil); err != nil {
		t.Fatalf("Remove %s: %v", b.name, err)
	}
	a.mustLeaveNothing()
	b.mustLeaveNothing()
	for _, w := range []*world{a, b} {
		if names, _ := w.podman("volume", "ls", "--format", "{{.Name}}"); strings.Contains(names, w.name+"-pgdata") {
			t.Errorf("%s: after down, expected no %s-pgdata, got %q", w.name, w.name, names)
		}
	}
}

// --- the services made again ---

// A running workspace's services are made again: their pod is new, and the tools member, with the same start
// time, reaches them by the same name.
func TestServicesAreMadeAgainUnderTheSameMember(t *testing.T) {
	w := newWorld(t, worldOpts{Kind: system.Work, Services: true})
	w.up()
	oldPod := w.podmanOK("pod", "inspect", w.name+"-postgres", "--format", "{{.Id}}")
	before := w.podmanOK("inspect", w.name+"-tools", "--format", "{{.Id}} {{.State.StartedAt}}")

	if err := w.sys.Services(); err != nil {
		t.Fatalf("Services: expected the services made again, got %v", err)
	}

	newPod := w.podmanOK("pod", "inspect", w.name+"-postgres", "--format", "{{.Id}}")
	if newPod == oldPod {
		t.Errorf("the pod: expected a new one, still %s", oldPod)
	}
	after := w.podmanOK("inspect", w.name+"-tools", "--format", "{{.Id}} {{.State.StartedAt}}")
	if after != before {
		t.Errorf("the tools member: expected the same start time, before %q, after %q", before, after)
	}
	if got := w.mustRun("wget", "-qO-", "http://postgres:5432/"); !strings.Contains(got, "ok") {
		t.Errorf("the new postgres: expected \"ok\", got %q", got)
	}
}

// --- an attempt ---

// An attempt reaches its own services, no address outside, and no other workspace by name or address; its
// network is internal, so it has no route.
func TestAnAttemptReachesItsServicesAndNothingOutside(t *testing.T) {
	a := newWorld(t, worldOpts{Kind: system.Attempt, Services: true})
	b := newWorld(t, worldOpts{Kind: system.Work, Services: true})
	a.up()
	b.up()

	if got := a.mustRun("wget", "-qO-", "http://postgres:5432/"); !strings.Contains(got, "ok") {
		t.Errorf("the attempt's own postgres: expected \"ok\", got %q", got)
	}
	if r := a.run("wget", "-T", "5", "-qO-", "http://example.com/"); r.err == nil && r.code == 0 {
		t.Errorf("the attempt reached the outside: expected a failure, got %q", r.out)
	}
	if r := a.run("wget", "-T", "5", "-qO-", "http://"+b.name+":5432/"); r.err == nil && r.code == 0 {
		t.Errorf("the attempt reached %s by name: expected a failure, got %q", b.name, r.out)
	}
	if r := a.run("wget", "-T", "3", "-qO-", "http://"+b.serviceIP("postgres")+":5432/"); r.err == nil && r.code == 0 {
		t.Errorf("the attempt reached another workspace by address: expected a failure, got %q", r.out)
	}
	if got := a.podmanOK("network", "inspect", a.name, "--format", "{{.Internal}}"); got != "true" {
		t.Errorf("the attempt's network: expected internal true, got %q", got)
	}
	// An attempt holds its slot after up: only Remove or down releases it.
	for _, c := range a.rec.Calls {
		if c.Op == "Release" {
			t.Errorf("an attempt after up: expected no Release, got %+v", c)
		}
	}
}

// --- the network ---

// The system's network is the system's name with isolate strict; a check reaches the outside.
func TestACheckReachesTheOutsideOnAStrictNetwork(t *testing.T) {
	w := newWorld(t, worldOpts{Kind: system.Check, Services: true})
	w.up()

	if got := vetted(w, "{{index .Options \"isolate\"}}"); got != "strict" {
		t.Errorf("the network: expected isolate strict, got %q", got)
	}
	if got := vetted(w, "{{.Internal}}"); got != "false" {
		t.Errorf("a check's network: expected internal false, got %q", got)
	}
	if r := w.run("wget", "-T", "10", "-qO-", "http://example.com/"); r.err != nil || r.code != 0 {
		t.Errorf("a check's tools member reaching example.com: expected exit 0, got exit %d, error %v", r.code, r.err)
	}
}

// vetted inspects this world's network with one format.
func vetted(w *world, format string) string {
	return w.podmanOK("network", "inspect", w.name, "--format", format)
}

// --- setup ---

// A setup with && runs in the tools member, as node, in the checkout's root; what it writes in a volume is
// there after up, and its output went to the caller.
func TestSetupRunsInTheToolsMemberAndItsMarkStays(t *testing.T) {
	w := newWorld(t, worldOpts{Kind: system.Work, Setup: "echo setup-output && mkdir -p node_modules && echo made > node_modules/mark && pwd > node_modules/pwd && id -u > node_modules/user"})
	w.up()

	if got := w.mustRun("cat", "node_modules/mark"); strings.TrimSpace(got) != "made" {
		t.Errorf("the mark: expected made, got %q", got)
	}
	if got := w.mustRun("cat", "node_modules/pwd"); strings.TrimSpace(got) != w.root {
		t.Errorf("setup's folder: expected %q, got %q", w.root, strings.TrimSpace(got))
	}
	if got := w.mustRun("cat", "node_modules/user"); strings.TrimSpace(got) != "1000" {
		t.Errorf("setup's user: expected 1000, got %q", strings.TrimSpace(got))
	}
	if !strings.Contains(w.out.String(), "setup-output") {
		t.Errorf("up's output: expected the setup's output, got %q", w.out.String())
	}
}

// A setup that fails makes up fail with its output and leaves no member, network or service of the system.
func TestAFailedSetupFailsUpAndLeavesNothing(t *testing.T) {
	w := newWorld(t, worldOpts{Kind: system.Work, Services: true, Setup: "echo boom >&2; exit 3"})
	if err := w.sys.Hold(); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := w.sys.Image(); err != nil {
		t.Fatalf("Image: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := w.sys.Up(ctx)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Up with a failed setup: expected an error with its output, got %v", err)
	}
	if !strings.Contains(w.out.String(), "boom") {
		t.Errorf("up's output: expected the setup's output, got %q", w.out.String())
	}
	for _, left := range w.leftovers() {
		if strings.Contains(left, "node-modules") || strings.Contains(left, "pgdata") {
			continue // a volume is not a member, network or service
		}
		t.Errorf("after a failed setup: expected no member, network or service, found %q", left)
	}
}

// --- run ---

// Every word after run is the command's argv, its flags among them; the exit is the command's; the folder is
// the checkout's root when the caller is outside, and the caller's folder inside it.
func TestRunRunsTheArgvUntouched(t *testing.T) {
	w := newWorld(t, worldOpts{Kind: system.Work})
	w.up()

	if got := w.mustRun("sh", "-c", `echo "$1"`, "x", "-h"); strings.TrimSpace(got) != "-h" {
		t.Errorf("the argv: expected -h, got %q", got)
	}
	if r := w.run("sh", "-c", "exit 7"); r.err != nil || r.code != 7 {
		t.Errorf("the exit: expected 7 and no error, got exit %d, error %v", r.code, r.err)
	}
	if got := w.mustRun("pwd"); strings.TrimSpace(got) != w.root {
		t.Errorf("the folder outside the checkout: expected %q, got %q", w.root, strings.TrimSpace(got))
	}
	t.Chdir(filepath.Join(w.root, "src"))
	if got := w.mustRun("pwd"); strings.TrimSpace(got) != filepath.Join(w.root, "src") {
		t.Errorf("the folder inside the checkout: expected %q, got %q", filepath.Join(w.root, "src"), strings.TrimSpace(got))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out bytes.Buffer
	if code, err := w.sys.Run(ctx, []string{"sh", "-c", "sleep 60"}, &out); err == nil || code == 0 {
		t.Errorf("a command past its ctx: expected an error and no clean exit, got exit %d, error %v", code, err)
	}
}

// --- logs ---

// Each member's output is readable by its name while the system lives.
func TestLogsShowsEachMembersOutput(t *testing.T) {
	w := newWorld(t, worldOpts{Kind: system.Work, Services: true, Setup: "echo from-setup"})
	w.up()
	// make the service print, and wait for its line to be there.
	w.mustRun("wget", "-qO-", "http://postgres:5432/")

	rows := []struct{ member, want string }{
		{"postgres", "service-up"},
	}
	for _, row := range rows {
		var out bytes.Buffer
		if err := w.sys.Logs(row.member, &out); err != nil {
			t.Errorf("Logs(%q): expected its output, got %v", row.member, err)
			continue
		}
		if !strings.Contains(out.String(), row.want) {
			t.Errorf("Logs(%q): expected %q, got %q", row.member, row.want, out.String())
		}
	}
	var tools bytes.Buffer
	if err := w.sys.Logs("tools", &tools); err != nil {
		t.Errorf("Logs(tools): expected the member, got %v", err)
	}
}

// --- down and up twice ---

// Up twice changes nothing, and a stopped member is started again. Down leaves no container, pod, volume or
// network, and a second down changes nothing.
func TestUpTwiceChangesNothingAndDownLeavesNothing(t *testing.T) {
	w := newWorld(t, worldOpts{Kind: system.Work, Services: true})
	w.up()
	firstTools := w.podmanOK("inspect", w.name+"-tools", "--format", "{{.Id}}")
	firstPod := w.podmanOK("pod", "inspect", w.name+"-postgres", "--format", "{{.Id}}")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := w.sys.Up(ctx); err != nil {
		t.Fatalf("Up a second time: %v", err)
	}
	if got := w.podmanOK("inspect", w.name+"-tools", "--format", "{{.Id}}"); got != firstTools {
		t.Errorf("up twice: the tools member: expected %s to stay, got %s", firstTools, got)
	}
	if got := w.podmanOK("pod", "inspect", w.name+"-postgres", "--format", "{{.Id}}"); got != firstPod {
		t.Errorf("up twice: the services: expected %s to stay, got %s", firstPod, got)
	}

	w.podmanOK("stop", "-t", "0", w.name+"-tools")
	if err := w.sys.Up(ctx); err != nil {
		t.Fatalf("Up after a stopped member: %v", err)
	}
	if got := w.podmanOK("inspect", w.name+"-tools", "--format", "{{.State.Running}}"); got != "true" {
		t.Errorf("a stopped member: expected it running again, got %q", got)
	}

	if err := w.sys.Remove(nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	w.mustLeaveNothing()
	if err := w.sys.Remove(nil); err != nil {
		t.Errorf("a second Remove: expected no error, got %v", err)
	}
	w.mustLeaveNothing()
}

// The record: up's output, every step's and every failure's, goes to the caller.
func TestDownPutsItsOutputToTheCaller(t *testing.T) {
	w := newWorld(t, worldOpts{Kind: system.Work, Setup: "echo setup-line"})
	w.up()
	if !strings.Contains(w.out.String(), "setup-line") {
		t.Errorf("up's output: expected the setup's line, got %q", w.out.String())
	}
	before := len(w.out.String())
	if err := w.sys.Remove(nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(w.out.String()) == before {
		t.Errorf("down's output: expected a step's line, got nothing past %q", w.out.String())
	}
}

