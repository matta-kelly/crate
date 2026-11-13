package system_test

// The app on real podman: the dev server in a person's workspace behind the router, the built image in a check
// as production runs it, and an app that never passes its health command.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine/machinetest"
	"github.com/matta-kelly/crate/internal/router"
	"github.com/matta-kelly/crate/internal/system"
)

// serve is a busybox httpd on 8080 that serves the word.
func serve(word string) string {
	return fmt.Sprintf("mkdir -p /tmp/www && echo %s >/tmp/www/index.html && exec httpd -f -p 8080 -h /tmp/www", word)
}

const health = `health = "wget -qO- http://127.0.0.1:8080/"`

const port = `
[[app.ports]]
name = "web"
port = 8080
`

// ownRouter is a router of this test's own: its own name, port and state folder.
func ownRouter(t *testing.T) (*router.Router, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	name := fmt.Sprintf("cratetest-router-%d-%d", os.Getpid(), serial.Add(1))
	t.Cleanup(func() { _ = exec.Command("podman", "rm", "-f", "-t", "0", name).Run() })
	return router.NewAt(&machinetest.Recorder{}, name, p, t.TempDir()), p
}

// through asks the router for host until it answers want, or 30 s pass.
func through(t *testing.T, p int, host, want string) {
	t.Helper()
	var last string
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/", p), nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			last = err.Error()
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		last = fmt.Sprintf("%d %s", resp.StatusCode, strings.TrimSpace(string(body)))
		if resp.StatusCode == 200 && strings.TrimSpace(string(body)) == want {
			return
		}
	}
	t.Fatalf("%s through the router: expected %q, got %s", host, want, last)
}

func TestAWorkspaceServesItsDevServerAtItsURLsAndDownLeavesNothing(t *testing.T) {
	t.Parallel()
	r, p := ownRouter(t)
	w := newWorld(t, worldOpts{Kind: system.Work, Busybox: true, Router: r,
		App: "[app]\ncommand = " + fmt.Sprintf("%q", serve("dev-server")) + "\n" + health + "\n" + port})
	w.up()

	if got := strings.TrimSpace(w.mustRun("wget", "-qO-", "http://app:8080/")); got != "dev-server" {
		t.Errorf("the tools member asks app: expected dev-server, got %q", got)
	}
	through(t, p, w.name+".localhost", "dev-server")
	through(t, p, "web."+w.name+".localhost", "dev-server")

	if err := w.sys.Remove(nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	w.mustLeaveNothing()
	if _, err := r.URLs(w.name); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after down: expected no routes, got %v", err)
	}
}

func TestACheckRunsTheBuiltAppAsProductionDoes(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOpts{Kind: system.Check, Busybox: true,
		App: "[app]\nbuild = \"app.Containerfile\"\n" + health + "\nenv = { GREETING = \"built-app\" }\n" + port,
		AppFiles: map[string]string{"app.Containerfile": "FROM " + busyboxImage + "\n" +
			`CMD ["sh", "-c", "mkdir -p /tmp/www && echo $GREETING >/tmp/www/index.html && exec httpd -f -p 8080 -h /tmp/www"]` + "\n"}})
	w.up()

	if got := strings.TrimSpace(w.mustRun("wget", "-qO-", "http://app:8080/")); got != "built-app" {
		t.Errorf("the tools member asks app: expected built-app, got %q", got)
	}
	app := w.name + "-app"
	rows := []struct{ what, format, want string }{
		{"the user", "{{.Config.User}}", "1000:1000"},
		{"the root", "{{.HostConfig.ReadonlyRootfs}}", "true"},
		{"the capabilities", "{{.EffectiveCaps}}", "[]"},
	}
	for _, row := range rows {
		if got := w.podmanOK("inspect", "-f", row.format, app); got != row.want {
			t.Errorf("%s of the built app: expected %s, got %s", row.what, row.want, got)
		}
	}
	if got := w.podmanOK("inspect", "-f", "{{.HostConfig.SecurityOpt}}", app); !strings.Contains(got, "no-new-privileges") {
		t.Errorf("the built app's security options: expected no-new-privileges, got %s", got)
	}

	if err := w.sys.Remove(nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	w.mustLeaveNothing()
	if _, err := w.podman("image", "exists", app); err == nil {
		t.Errorf("after down: expected the image %s gone, it is there", app)
	}
}

// The one test that shortens AppReady, so it runs alone: CRATE_LIMIT_SCALE is the process's.
func TestAnAppThatNeverPassesItsHealthFailsUpWithItsLog(t *testing.T) {
	t.Setenv("CRATE_LIMIT_SCALE", "6")
	w := newWorld(t, worldOpts{Kind: system.Work, Busybox: true,
		App: "[app]\ncommand = \"echo app-says-hello; exec sleep 600\"\nhealth = \"false\"\n"})
	if err := w.sys.Hold(); err != nil {
		t.Fatal(err)
	}
	if err := w.sys.Image(); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	err := w.sys.Up(context.Background())
	if err == nil {
		t.Fatalf("Up: expected an error, got none")
	}
	if took := time.Since(began); took > 2*time.Minute {
		t.Errorf("Up: expected it to stop at the ready limit, it took %s", took)
	}
	for _, word := range []string{"health", "app-says-hello"} {
		if !strings.Contains(err.Error(), word) {
			t.Errorf("Up's error: expected it to hold %q, got %v", word, err)
		}
	}
	w.mustLeaveNothing()
}
