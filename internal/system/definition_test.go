package system_test

// The app from its definition ([app] deploy) on real podman: the dev server from the checkout in a person's
// workspace, behind the router; the built image in a check, as the definition says production runs it; and a deploy
// that lacks a key the definition names.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/matta-kelly/crate/internal/system"
)

// definition is an app.yaml of one Pod: an init container that refuses to end well without the values' greeting, then
// the app, which serves the greeting and the secret's word on 8080, with a livenessProbe. Each image is the one the
// repo ships.
const definition = `apiVersion: v1
kind: Pod
metadata:
  name: hello
spec:
  initContainers:
    - name: prepare
      image: example.test/hello:VERSION
      command: ["sh", "-c", "test \"$GREETING\" = hello"]
      env:
        - { name: GREETING, valueFrom: { configMapKeyRef: { name: hello, key: GREETING } } }
  containers:
    - name: web
      image: example.test/hello:VERSION
      command: ["sh", "-c", "mkdir -p /tmp/www && echo \"$GREETING $WORD\" >/tmp/www/index.html && exec httpd -f -p 8080 -h /tmp/www"]
      ports:
        - { name: web, containerPort: 8080 }
      env:
        - { name: GREETING, valueFrom: { configMapKeyRef: { name: hello, key: GREETING } } }
        - { name: WORD, valueFrom: { secretKeyRef: { name: hello, key: WORD } } }
      livenessProbe:
        exec: { command: ["wget", "-qO-", "http://127.0.0.1:8080/"] }
      securityContext:
        readOnlyRootFilesystem: true
        allowPrivilegeEscalation: false
        capabilities: { drop: [ALL] }
`

const devValues = `apiVersion: v1
kind: ConfigMap
metadata:
  name: hello
data:
  GREETING: hello
---
apiVersion: v1
kind: Secret
metadata:
  name: hello
stringData:
  WORD: secret-word
`

func definedFiles(values string) map[string]string {
	return map[string]string{
		"deploy/app.yaml":   definition,
		"deploy/dev.yaml":   values,
		"deploy/prod.yaml":  "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: hello\ndata:\n  GREETING: prod\n",
		"app.Containerfile": "FROM " + busyboxImage + "\nUSER 1000:1000\n",
	}
}

func TestACheckRunsTheDefinitionWithTheBuiltImage(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOpts{Kind: system.Check, Busybox: true,
		App: "[app]\nbuild = \"app.Containerfile\"\ndeploy = \"deploy\"\n", AppFiles: definedFiles(devValues)})
	w.up()

	if got := strings.Fields(w.mustRun("wget", "-qO-", "http://app:8080/")); strings.Join(got, " ") != "hello secret-word" {
		t.Errorf("the tools member asks app: expected the values' greeting, then the secret's word, got %q", got)
	}
	if got := strings.ReplaceAll(strings.TrimSpace(w.mustRun("sh", "-c", `echo "$CRATE_APP_URL"`)), "\r", ""); got != "http://"+w.name+"-app:8080" {
		t.Errorf("CRATE_APP_URL: expected the app's name and its first port, got %q", got)
	}
	app := w.name + "-app-web"
	if got := w.podmanOK("inspect", "-f", "{{.Config.Image}}", app); got != "localhost/"+w.name+"-app:latest" {
		t.Errorf("the app's image: expected the check's build, got %s", got)
	}
	rows := []struct{ what, format, want string }{
		{"the root", "{{.HostConfig.ReadonlyRootfs}}", "true"},
		{"the capabilities", "{{.EffectiveCaps}}", "[]"},
	}
	for _, row := range rows {
		if got := w.podmanOK("inspect", "-f", row.format, app); got != row.want {
			t.Errorf("%s, as the definition says: expected %s, got %s", row.what, row.want, got)
		}
	}
	if got := w.podmanOK("secret", "ls", "--format", "{{.Name}}", "--filter", "name="+w.name+"-hello"); got != w.name+"-hello" {
		t.Errorf("the play's secret: expected it named for the system, %s-hello, got %q", w.name, got)
	}

	if err := w.sys.Remove(nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	w.mustLeaveNothing()
}

func TestAWorkspaceRunsTheDefinitionWithItsDevServerBehindTheRouter(t *testing.T) {
	t.Parallel()
	r, p := ownRouter(t)
	w := newWorld(t, worldOpts{Kind: system.Work, Busybox: true, Router: r,
		App: "[app]\ncommand = " + fmt.Sprintf("%q", `mkdir -p /tmp/www && echo "$GREETING $WORD dev-server from $(pwd)" >/tmp/www/index.html && exec httpd -f -p 8080 -h /tmp/www`) +
			"\ndeploy = \"deploy\"\n", AppFiles: definedFiles(devValues)})
	w.up()

	want := "hello secret-word dev-server from " + w.root
	if got := strings.TrimSpace(w.mustRun("wget", "-qO-", "http://app:8080/")); got != want {
		t.Errorf("the tools member asks app: expected %q, got %q", want, got)
	}
	through(t, p, w.name+".localhost", want)
	through(t, p, "web."+w.name+".localhost", want)
	if got := w.podmanOK("inspect", "-f", "{{.Config.User}}", w.name+"-app-web"); got != "1000:1000" {
		t.Errorf("the dev server's user: expected the tools member's, 1000:1000, got %s", got)
	}

	w.up() // a second up changes nothing
	if err := w.sys.Remove(nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	w.mustLeaveNothing()
	if _, err := r.URLs(w.name); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after down: expected no routes, got %v", err)
	}
}

func TestADeployThatLacksAKeyTheDefinitionNamesFailsUpAndNamesIt(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOpts{Kind: system.Check, Busybox: true,
		App: "[app]\nbuild = \"app.Containerfile\"\ndeploy = \"deploy\"\n", AppFiles: definedFiles(strings.Replace(devValues, "WORD: secret-word", "OTHER: x", 1))})
	if err := w.sys.Hold(); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if err := w.sys.Image(); err != nil {
		t.Fatalf("Image: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := w.sys.Up(ctx)
	if err == nil || !strings.Contains(err.Error(), "WORD") {
		t.Fatalf("Up with a Secret that lacks WORD: expected an error that names WORD, got %v", err)
	}
	w.mustLeaveNothing()
}
