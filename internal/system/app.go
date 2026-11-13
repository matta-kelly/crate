package system

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/matta-kelly/crate/internal/declaration"
	"github.com/matta-kelly/crate/internal/limit"
	"github.com/matta-kelly/crate/internal/member"
	"github.com/matta-kelly/crate/internal/router"
)

// Router is the machine's router, as a person's workspace needs it for its app's routes.
type Router interface {
	Add(workspace, network string, routes []router.Route) error
	Remove(workspace, network string) error
}

// appName is the app member's container, and its alias beside "app".
func (s *System) appName() string { return s.spec.Name + "-app" }

// built says this kind runs the image that ships, not the dev server.
func (s *System) built() bool { return s.spec.Kind == Check || s.spec.Kind == Postsubmit }

// app makes the app member from [app]: the built image for a check and postsubmit, the dev server for a person's
// workspace and an attempt. It waits for the health command, then a person's workspace gets its routes. A kind
// with nothing to run makes no app.
func (s *System) app(ctx context.Context, decl declaration.Declaration, folders []string) error {
	a := decl.App
	var spec member.Spec
	switch {
	case s.built() && a.Build != "":
		placement, err := s.m.Place(s.spec.Kind, s.spec.Name)
		if err != nil {
			return err
		}
		build := []string{"build", "-f", filepath.Join(s.spec.Checkout, a.Build), "-t", s.appName()}
		if placement != "" {
			build = append(build, "--cgroup-parent="+string(placement))
		}
		build = append(build, s.spec.Checkout)
		buildCtx, cancel := context.WithTimeout(ctx, limit.Build())
		_, err = s.call(buildCtx, build...)
		cancel()
		if err != nil {
			return fmt.Errorf("app: the build of %s: %w", a.Build, err)
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
		placement, err := s.m.Place(s.spec.Kind, s.spec.Name)
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
	default:
		return nil
	}

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
		if err := s.ready(ctx); err != nil {
			return err
		}
	}
	if s.spec.Kind == Work && s.spec.Router != nil && len(a.Ports) > 0 {
		routes := make([]router.Route, len(a.Ports))
		for i, p := range a.Ports {
			routes[i] = router.Route{Name: p.Name, Target: s.appName(), Port: p.Port}
		}
		if err := s.spec.Router.Add(s.spec.Name, s.spec.Name, routes); err != nil {
			return fmt.Errorf("app: its routes: %w", err)
		}
	}
	return nil
}

// ready runs the app's health command once a second until it passes, within limit.AppReady. crate runs it, not
// podman's timer, so it holds on every machine. An app that stops, or is not healthy in time, fails Up with its
// last lines of output.
func (s *System) ready(ctx context.Context) error {
	deadline := time.Now().Add(limit.AppReady())
	why := fmt.Sprintf("it did not pass its health command within %s", limit.AppReady())
	for {
		ask, cancel := context.WithTimeout(ctx, limit.Podman())
		err := command(ask, "podman", nil, "healthcheck", "run", s.appName()).Run()
		cancel()
		if err == nil {
			return nil
		}
		running, rerr := s.running(ctx, s.appName())
		if rerr == nil && !running {
			why = "it stopped before it passed its health command"
			break
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	logCtx, cancel := context.WithTimeout(context.Background(), limit.Podman())
	defer cancel()
	logs, _ := command(logCtx, "podman", nil, "logs", "--tail", "20", s.appName()).CombinedOutput()
	return fmt.Errorf("app: %s: %s. Its last output:\n%s", s.appName(), why, strings.TrimSpace(string(logs)))
}

// removeApp takes the app's routes away, then its container and its built image, if they are there.
func (s *System) removeApp(ctx context.Context) error {
	if s.spec.Kind == Work && s.spec.Router != nil {
		if err := s.spec.Router.Remove(s.spec.Name, s.spec.Name); err != nil {
			return fmt.Errorf("down: the routes of %s are still there: %w", s.spec.Name, err)
		}
	}
	made, err := s.containerExists(ctx, s.appName())
	if err != nil {
		return err
	}
	if made {
		if _, err := s.call(ctx, "rm", "-f", "-t", "0", s.appName()); err != nil {
			return fmt.Errorf("down: the container %s is still there: %w", s.appName(), err)
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
