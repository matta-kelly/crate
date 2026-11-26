package system

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/matta-kelly/crate/internal/declaration"
	"github.com/matta-kelly/crate/internal/fault"
	"github.com/matta-kelly/crate/internal/limit"
	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/member"
	"github.com/matta-kelly/crate/internal/process"
	"github.com/matta-kelly/crate/internal/record"
	"github.com/matta-kelly/crate/internal/router"
)

// Router is the machine's router, as a person's workspace needs it for its app's routes.
type Router interface {
	Add(workspace, network string, routes []router.Route) error
	Remove(workspace, network string) error
	Within() (string, error)
}

// routerEnv is CRATE_ROUTER, the router's URL from inside the workspace, for a person's workspace with routes: the
// router is on its network, so a member reaches another workspace's app there by that workspace's host. "" for none.
func (s *System) routerEnv(a declaration.App) (string, error) {
	if ports, _ := s.shape(a); s.spec.Kind != Work || s.spec.Router == nil || len(ports) == 0 {
		return "", nil
	}
	within, err := s.spec.Router.Within()
	if err != nil {
		return "", err
	}
	return "CRATE_ROUTER=" + within, nil
}

// appName is the app member's name: its container, or its Pod when [app] deploy defines it, its image, and its alias
// beside "app".
func (s *System) appName() string { return s.spec.Name + "-app" }

// shape is the app as this system runs it: its ports, and the container podman runs it in. [app] deploy's are its
// definition's; one that does not render gives no ports, and app says why.
func (s *System) shape(a declaration.App) ([]declaration.Port, string) {
	if a.Deploy == "" {
		return a.Ports, s.appName()
	}
	definition, values, err := readDefinition(s.spec.Checkout, a.Deploy)
	if err != nil {
		return nil, s.appName()
	}
	r, err := render(definition, values, s.spec.Name, "", nil)
	if err != nil {
		return nil, s.appName()
	}
	return r.Ports, r.Container
}

// definedSecrets are the podman secrets the app's play makes, from the checkout's [app] deploy, or none.
func (s *System) definedSecrets() []string {
	decl, _, err := s.read()
	if err != nil || decl.App.Deploy == "" {
		return nil
	}
	definition, values, err := readDefinition(s.spec.Checkout, decl.App.Deploy)
	if err != nil {
		return nil
	}
	r, err := render(definition, values, s.spec.Name, "", nil)
	if err != nil {
		return nil
	}
	return r.Secrets
}

// appURL is the URL the tools member reaches the app at, by its full name and its first port, or "" when this kind
// runs no app or the app has no port. A browser needs it: app alone is a top-level domain that Chrome sends to https
// (the HSTS preload list).
func (s *System) appURL(a declaration.App) string {
	runs := (s.built() && a.Build != "") || (!s.built() && a.Command != "")
	ports, _ := s.shape(a)
	if !runs || len(ports) == 0 {
		return ""
	}
	return fmt.Sprintf("http://%s:%d", s.appName(), ports[0].Port)
}

// built says this kind runs the image that ships, not the dev server.
func (s *System) built() bool { return s.spec.Kind == Check || s.spec.Kind == Postsubmit }

// app makes the app member from [app]: the built image for a check and postsubmit, the dev server for a person's
// workspace and an attempt. It waits for the health command, then a person's workspace gets its routes. A kind
// with nothing to run makes no app.
func (s *System) app(ctx context.Context, decl declaration.Declaration, folders []string) error {
	a := decl.App
	if a.Deploy != "" {
		return s.definedApp(ctx, decl, folders)
	}
	var spec member.Spec
	switch {
	case s.built() && a.Build != "":
		if missing := missingNeeds(a.Needs, a.Env, nil); len(missing) > 0 {
			return fault.Change(fmt.Errorf("app: [app] needs %s; [app] env does not give them", strings.Join(missing, ", ")))
		}
		placement, err := s.place()
		if err != nil {
			return err
		}
		if err := s.build(ctx, a.Build, placement); err != nil {
			return err
		}
		if len(a.Needs) > 0 {
			step := s.spec.Record.Step(record.Needs)
			err := s.refuses(ctx, a, placement)
			step.End(err, "")
			if err != nil {
				return err
			}
		}
		spec = member.Built(member.BuiltOptions{
			Name:      s.appName(),
			Image:     s.appName(),
			Network:   s.spec.Name,
			Aliases:   []string{"app", s.appName()},
			Env:       sortedEnv(a.Env),
			Health:    a.Health,
			Placement: placement,
		})
	case !s.built() && a.Command != "":
		if missing := missingNeeds(a.Needs, decl.Tools.Env, append(slices.Clone(s.spec.Env), s.spec.Pass...)); len(missing) > 0 {
			return fault.Change(fmt.Errorf("app: [app] needs %s; neither [tools] env nor crate up -e gives them", strings.Join(missing, ", ")))
		}
		placement, err := s.place()
		if err != nil {
			return err
		}
		spec = member.Tools(member.ToolsOptions{
			System:    s.spec.Name,
			Root:      s.spec.Checkout,
			Image:     s.image,
			Folders:   folders,
			Env:       decl.Tools.Env,
			Placement: placement,
		})
		spec.Name = s.appName()
		spec.Aliases = []string{"app", s.appName()}
		spec.Command = []string{"sh", "-c", "cd " + shellQuote(s.spec.Checkout) + " && " + a.Command}
		spec.Health = a.Health
		for _, mount := range s.spec.Mounts {
			spec.Mounts = append(spec.Mounts, member.Mount{Source: mount.Path, Target: mount.Path, ReadOnly: true})
		}
		spec.Env = append(spec.Env, s.spec.Env...)
		spec.Env = append(spec.Env, s.spec.Pass...)
		routerEnv, err := s.routerEnv(a)
		if err != nil {
			return err
		}
		if routerEnv != "" {
			spec.Env = append(spec.Env, routerEnv)
		}
		if len(s.spec.Pass) > 0 {
			// A member's env is set when it is made, so the dev server is made again to take the names.
			if _, err := s.call(ctx, "rm", "-f", "-t", "0", spec.Name); err != nil {
				return err
			}
		}
	default:
		return nil
	}

	step := s.spec.Record.Step(record.App)
	err := s.start(ctx, spec, a)
	step.End(err, "")
	return err
}

// build makes the app's image from its Dockerfile, in placement, within limit.Build.
func (s *System) build(ctx context.Context, dockerfile string, placement machine.Placement) (err error) {
	step := s.spec.Record.Step(record.Build)
	defer func() { step.End(err, "") }()
	build := []string{"build", "-f", filepath.Join(s.spec.Checkout, dockerfile), "-t", s.appName()}
	if s.spec.Source != "" {
		build = append(build, "--label", "org.opencontainers.image.source="+s.spec.Source)
	}
	if placement != "" {
		build = append(build, "--cgroup-parent="+string(placement))
	}
	build = append(build, s.spec.Checkout)
	buildCtx, cancel := context.WithTimeout(ctx, limit.Build())
	defer cancel()
	if _, err := s.call(buildCtx, build...); err != nil {
		return fault.Own(fmt.Errorf("app: the build of %s: %w", dockerfile, err))
	}
	return nil
}

// KeepApp tags the built app's image as ref, so it outlives the system: a check's image, kept to be shipped once its
// commit's main is green. The system's own name for it goes with the system, as before.
func (s *System) KeepApp(ctx context.Context, ref string) error {
	if _, err := s.call(ctx, "tag", s.appName(), ref); err != nil {
		return fmt.Errorf("app: the keep of the image as %s: %w", ref, err)
	}
	return nil
}

// start runs the app's member unless it runs already, waits for its health, and adds a person's routes.
func (s *System) start(ctx context.Context, spec member.Spec, a declaration.App) error {
	made, err := s.containerExists(ctx, spec.Name)
	if err != nil {
		return err
	}
	if made {
		running, err := s.running(ctx, spec.Name)
		if err != nil {
			return err
		}
		if !running {
			if _, err := s.call(ctx, "rm", "-f", "-t", "0", spec.Name); err != nil {
				return err
			}
			made = false
		}
	}
	if !made {
		if _, err := s.call(ctx, s.runArgs(spec)...); err != nil {
			return err
		}
	}
	if a.Health != "" {
		if err := s.ready(ctx, spec.Name); err != nil {
			return err
		}
	}
	return s.route(a.Ports)
}

// route adds a person's workspace's routes to the app, one per port, at its name on the network.
func (s *System) route(ports []declaration.Port) error {
	if s.spec.Kind != Work || s.spec.Router == nil || len(ports) == 0 {
		return nil
	}
	routes := make([]router.Route, len(ports))
	for i, p := range ports {
		routes[i] = router.Route{Name: p.Name, Target: s.appName(), Port: p.Port}
	}
	if err := s.spec.Router.Add(s.spec.Name, s.spec.Name, routes); err != nil {
		return fmt.Errorf("app: its routes: %w", err)
	}
	return nil
}

// refusedName is the run of the built image that proves it refuses to start without its needs.
func (s *System) refusedName() string { return s.spec.Name + "-app-without-needs" }

// refuses runs the built image once as the app runs, but with [app] env less every need, and holds the image to its
// binding: it must stop non-zero within limit.AppReady, and its output must name each need. The run is removed
// after, whatever it did.
func (s *System) refuses(ctx context.Context, a declaration.App, placement machine.Placement) error {
	env := map[string]string{}
	for key, value := range a.Env {
		if !slices.Contains(a.Needs, key) {
			env[key] = value
		}
	}
	name := s.refusedName()
	spec := member.Built(member.BuiltOptions{
		Name:      name,
		Image:     s.appName(),
		Network:   s.spec.Name,
		Env:       sortedEnv(env),
		Placement: placement,
	})
	if _, err := s.call(ctx, s.runArgs(spec)...); err != nil {
		return err
	}
	defer func() { _, _ = ask(context.Background(), "rm", "-f", "-t", "0", name) }()
	deadline := time.Now().Add(limit.AppReady())
	state := ""
	for {
		out, err := ask(ctx, "inspect", name, "--format", "{{.State.Running}} {{.State.ExitCode}}")
		state = out
		if err == nil && strings.HasPrefix(state, "false ") {
			break
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			state = ""
			break
		}
		time.Sleep(time.Second)
	}
	text := lastOutput(name)
	without := strings.Join(a.Needs, ", ")
	switch {
	case state == "":
		return fault.Change(fmt.Errorf("app: without %s the built image was still running after %s; it must refuse to start. Its output:\n%s", without, limit.AppReady(), text))
	case state == "false 0":
		return fault.Change(fmt.Errorf("app: without %s the built image exited 0; it must refuse to start. Its output:\n%s", without, text))
	}
	var unnamed []string
	for _, need := range a.Needs {
		if !strings.Contains(text, need) {
			unnamed = append(unnamed, need)
		}
	}
	if len(unnamed) > 0 {
		return fault.Change(fmt.Errorf("app: without %s the built image refused, but its output does not name %s. Its output:\n%s", without, strings.Join(unnamed, ", "), text))
	}
	return nil
}

// missingNeeds is each need that neither env nor extra gives. An extra is a KEY=value line, or a name alone that
// podman passes from crate's own env.
func missingNeeds(needs []string, env map[string]string, extra []string) []string {
	var missing []string
	for _, need := range needs {
		if _, ok := env[need]; ok {
			continue
		}
		if slices.ContainsFunc(extra, func(kv string) bool { return kv == need || strings.HasPrefix(kv, need+"=") }) {
			continue
		}
		missing = append(missing, need)
	}
	return missing
}

// ready runs the app's health command once a second until it passes, within limit.AppReady. crate runs it, not
// podman's timer, so it holds on every machine. An app that stops, or is not healthy in time, fails Up with its
// last lines of output.
func (s *System) ready(ctx context.Context, name string) error {
	deadline := time.Now().Add(limit.AppReady())
	why := fmt.Sprintf("it did not pass its health command within %s", limit.AppReady())
	for {
		if _, err := ask(ctx, "healthcheck", "run", name); err == nil {
			return nil
		}
		running, rerr := s.running(ctx, name)
		if rerr == nil && !running {
			why = "it stopped before it passed its health command"
			break
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	return fault.Change(fmt.Errorf("app: %s: %s. Its last output:\n%s", name, why, lastOutput(name)))
}

// lastOutput is the last 20 lines of a container's output, stdout and stderr as one.
func lastOutput(name string) string {
	text, _ := process.Combined(context.Background(), process.Call{Name: "podman logs", Prog: "podman",
		Args: []string{"logs", "--tail", "20", name}, Limit: limit.Podman()})
	return text
}

// removeApp takes the app's routes away, then its Pod and secrets or its container, and its built image, if they are
// there.
func (s *System) removeApp(ctx context.Context) error {
	if s.spec.Kind == Work && s.spec.Router != nil {
		if err := s.spec.Router.Remove(s.spec.Name, s.spec.Name); err != nil {
			return fmt.Errorf("down: the routes of %s are still there: %w", s.spec.Name, err)
		}
	}
	if err := s.removeDefined(ctx, s.appName(), s.definedSecrets()); err != nil {
		return fmt.Errorf("down: %w", err)
	}
	for _, name := range []string{s.refusedName(), s.appName()} {
		made, err := s.containerExists(ctx, name)
		if err != nil {
			return err
		}
		if made {
			if _, err := s.call(ctx, "rm", "-f", "-t", "0", name); err != nil {
				return fmt.Errorf("down: the container %s is still there: %w", name, err)
			}
		}
	}
	image, err := s.exists(ctx, "image", "exists", s.appName())
	if err != nil {
		return err
	}
	if image {
		if _, err := s.call(ctx, "image", "rm", s.appName()); err != nil {
			return fmt.Errorf("down: the image %s is still there: %w", s.appName(), err)
		}
	}
	return nil
}

func sortedEnv(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, len(keys))
	for i, key := range keys {
		out[i] = key + "=" + env[key]
	}
	return out
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
