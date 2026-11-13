package main_test

// The workspace verbs, through the built crate: each gives the same from another folder with -C, run passes
// every word as argv, and down leaves nothing. These run on real podman.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const wsAlpine = "docker.io/library/alpine:latest"

var wsSerial atomic.Int64

// ws is one checkout of a tiny repo, with its own name, and real podman under it.
type ws struct {
	t    *testing.T
	dir  string
	root string
	name string
}

func newWs(t *testing.T, declared string) *ws {
	t.Helper()
	if _, err := exec.LookPath("podman"); err != nil {
		t.Fatalf("these tests run on real podman: %v", err)
	}
	w := &ws{t: t, dir: t.TempDir()}
	w.root = filepath.Join(w.dir, "ws")
	if declared == "" {
		declared = fmt.Sprintf("crtest-%d-%d", os.Getpid(), wsSerial.Add(1))
	}
	w.name = declared + "-ws"
	for _, d := range []string{w.root, filepath.Join(w.root, ".devcontainer"), filepath.Join(w.root, "src")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w.write("crate.toml", fmt.Sprintf(`name = %q
services = ".devcontainer/pod.yaml"

[dev]
image = "docker.io/library/alpine"
containerfile = ".devcontainer/Containerfile"
inputs = [".devcontainer/Containerfile", "package-lock.json"]

[tools]
setup = "echo ready"
volumes = ["node_modules"]
`, declared))
	w.write(".devcontainer/Containerfile", "FROM "+wsAlpine+"\n")
	w.write("package-lock.json", "lock\n")
	w.write(".devcontainer/pod.yaml", `apiVersion: v1
kind: Pod
metadata:
  name: WORKSPACE-postgres
spec:
  restartPolicy: Never
  containers:
    - name: postgres
      image: docker.io/library/busybox:latest
      command: ["sh", "-c", "echo service-up; echo ok >/tmp/index.html; exec httpd -f -p 5432 -h /tmp"]
  volumes:
    - name: WORKSPACE-pgdata
      emptyDir: {}
`)
	w.write("src/README.md", "readme\n")
	w.git("init", "-q", "-b", "main")
	w.git("add", "-A")
	w.git("commit", "-qm", "init")
	w.tagDevImage()
	t.Cleanup(func() {
		_, _ = w.run(w.dir, "down")
		w.sweep()
	})
	return w
}

func (w *ws) write(path, body string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.root, path), []byte(body), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *ws) git(args ...string) {
	w.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = w.root
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		w.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (w *ws) tagDevImage() {
	w.t.Helper()
	cmd := exec.Command("git", "ls-tree", "-r", "HEAD", "--", ".devcontainer/Containerfile", "package-lock.json")
	cmd.Dir = w.root
	listing, err := cmd.Output()
	if err != nil {
		w.t.Fatalf("git ls-tree: %v", err)
	}
	sum := sha256.Sum256(listing)
	tag := "docker.io/library/alpine:" + hex.EncodeToString(sum[:])[:16]
	if exec.Command("podman", "image", "exists", wsAlpine).Run() != nil {
		w.podman("pull", "-q", wsAlpine)
	}
	w.podman("tag", wsAlpine, tag)
}

func (w *ws) podman(args ...string) string {
	w.t.Helper()
	cmd := exec.Command("podman", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.t.Fatalf("podman %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// run runs crate in cwd, with -C when the caller wants another folder.
func (w *ws) run(cwd string, args ...string) (result, error) {
	w.t.Helper()
	return w.runEnv(cwd, nil, args...)
}

// runEnv is run with extra KEY=value lines in crate's own environment.
func (w *ws) runEnv(cwd string, env []string, args ...string) (result, error) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, crateBin, args...)
	cmd.Dir = cwd
	cmd.Env = append(filterEnv(os.Environ(), "CRATE_MACHINE", "CONTAINER_HOST", "CRATE_LIMIT_SCALE"),
		"PATH="+os.Getenv("PATH"),
		"CRATE_MACHINE="+filepath.Join(w.dir, "no-machine.toml"),
		"XDG_STATE_HOME="+filepath.Join(w.dir, "state"))
	cmd.Env = append(cmd.Env, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	start := time.Now()
	err := cmd.Run()
	r := result{out: out.String(), err: errb.String(), took: time.Since(start)}
	if ee, ok := err.(*exec.ExitError); ok {
		r.code = ee.ExitCode()
	} else if err != nil {
		return r, err
	}
	return r, nil
}

func (w *ws) mustRun(cwd string, args ...string) result {
	w.t.Helper()
	r, err := w.run(cwd, args...)
	if err != nil {
		w.t.Fatalf("crate %v: %v", args, err)
	}
	if r.code != 0 {
		w.t.Fatalf("crate %v: expected exit 0, got %s", args, r)
	}
	return r
}

// leftovers are the containers, pods, volumes and networks of this workspace that podman still holds.
func (w *ws) leftovers() []string {
	w.t.Helper()
	var found []string
	for _, ask := range [][]string{
		{"ps", "-a", "--format", "{{.Names}}"},
		{"pod", "ls", "--format", "{{.Name}}"},
		{"volume", "ls", "--format", "{{.Name}}"},
		{"network", "ls", "--format", "{{.Name}}"},
	} {
		out := w.podman(ask...)
		for _, name := range strings.Fields(out) {
			if strings.HasPrefix(name, w.name) {
				found = append(found, name)
			}
		}
	}
	return found
}

func (w *ws) sweep() {
	for _, ask := range [][]string{
		{"ps", "-a", "--format", "{{.Names}}"},
		{"pod", "ls", "--format", "{{.Name}}"},
		{"volume", "ls", "--format", "{{.Name}}"},
		{"network", "ls", "--format", "{{.Name}}"},
	} {
		for _, name := range strings.Fields(w.podman(ask...)) {
			if !strings.HasPrefix(name, w.name) {
				continue
			}
			switch ask[0] {
			case "ps":
				_, _ = w.rawPodman("rm", "-f", "-t", "0", name)
			case "pod":
				_, _ = w.rawPodman("pod", "rm", "-f", name)
			case "volume":
				_, _ = w.rawPodman("volume", "rm", "-f", name)
			case "network":
				_, _ = w.rawPodman("network", "rm", "-f", name)
			}
		}
	}
}

func (w *ws) rawPodman(args ...string) (string, error) {
	out, err := exec.Command("podman", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// --- name ---

// crate name prints <name>-<folder>, or the folder alone when no name is declared, from inside
// the checkout, from below it, and from another folder with -C.
func TestNamePrintsTheWorkspacesName(t *testing.T) {
	rows := []struct {
		name     string
		declared string
		want     string
	}{
		{"no declared name", "", "que-pasa"},
		{"a declared name", "name = \"alpha\"\n", "alpha-que-pasa"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := newFx(t, "que-pasa")
			if row.declared != "" {
				f.commit("crate.toml", row.declared)
			}
			f.commit("sub/deeper/file.txt", "x\n")
			inside := f.run(f.repo, nil, "name")
			below := f.run(filepath.Join(f.repo, "sub", "deeper"), nil, "name")
			away := f.run(t.TempDir(), nil, "-C", f.repo, "name")
			rel := f.run(f.dir, nil, "-C", "que-pasa", "name")
			if want := row.want + "\n"; inside.code != 0 || inside.out != want {
				t.Fatalf("crate name: expected exit 0 and %q, got %s", want, inside)
			}
			for name, got := range map[string]result{"below its root": below, "with -C": away, "with a relative -C": rel} {
				if got.code != 0 || got.out != inside.out {
					t.Errorf("%s: expected %s\ngot %s", name, inside, got)
				}
			}
		})
	}
}

// A name that is not a DNS label is refused with the reason, before podman is called.
func TestANameThatIsNoDNSLabelIsRefused(t *testing.T) {
	f := newFx(t, "que-pasa")
	f.commit("crate.toml", "name = \"Bad_Name\"\n")
	f.commit("package-lock.json", "lock\n")
	for _, args := range [][]string{{"up"}, {"up", "--attempt"}} {
		r := f.run(f.repo, nil, args...)
		if r.code != 1 || !strings.Contains(r.err, "Bad_Name") || !strings.Contains(r.err, "DNS label") {
			t.Errorf("crate %v: expected exit 1 and a reason that names Bad_Name and DNS label, got %s", args, r)
		}
		if calls := f.calls(); strings.Contains(calls, "podman") {
			t.Errorf("crate %v: expected no podman call before the refusal, got\n%s", args, calls)
		}
	}
}

// --- run ---

// crate run passes every word as argv, its flags among them, runs in the caller's folder inside the checkout
// and the checkout's root outside it, gives the command's exit, and makes the workspace first.
func TestRunPassesEveryWordAsArgv(t *testing.T) {
	t.Parallel()
	w := newWs(t, "")
	// no up: run makes the workspace first.
	r := w.mustRun(w.root, "run", "sh", "-c", `echo "$1"`, "x", "-h")
	if strings.TrimSpace(r.out) != "-h" {
		t.Errorf("run with -h: expected -h, got %q", r.out)
	}
	r = w.mustRun(w.root, "run", "--", "sh", "-c", `echo "$1"`, "x", "--help")
	if strings.TrimSpace(r.out) != "--help" {
		t.Errorf("run after --: expected --help, got %q", r.out)
	}
	r = w.mustRun(w.root, "run", "pwd")
	if strings.TrimSpace(r.out) != w.root {
		t.Errorf("run outside the checkout: expected %q, got %q", w.root, strings.TrimSpace(r.out))
	}
	r = w.mustRun(filepath.Join(w.root, "src"), "run", "pwd")
	if strings.TrimSpace(r.out) != filepath.Join(w.root, "src") {
		t.Errorf("run inside the checkout: expected %q, got %q", filepath.Join(w.root, "src"), strings.TrimSpace(r.out))
	}
	r, err := w.run(t.TempDir(), "-C", w.root, "run", "sh", "-c", "exit 7")
	if err != nil {
		t.Fatalf("crate run: %v", err)
	}
	if r.code != 7 {
		t.Errorf("run's exit: expected 7, got %d: %s", r.code, r)
	}
}

// run -e NAME gives the tools member the host's NAME, its value never in argv; a NAME the host has not set is refused,
// named, and runs nothing.
func TestRunPassesTheHostsEnvByName(t *testing.T) {
	t.Parallel()
	w := newWs(t, "")
	r, err := w.runEnv(w.root, []string{"CRATE_TEST_SECRET=s3cret value", "CRATE_TEST_OTHER=two"},
		"run", "-e", "CRATE_TEST_SECRET", "-e", "CRATE_TEST_OTHER", "--", "sh", "-c", `echo "$CRATE_TEST_SECRET|$CRATE_TEST_OTHER"`)
	if err != nil || r.code != 0 || strings.TrimSpace(r.out) != "s3cret value|two" {
		t.Errorf("run -e: expected the host's values, got %v %s", err, r)
	}
	r, err = w.run(w.root, "run", "-e", "CRATE_TEST_UNSET", "sh", "-c", "echo ran")
	if err != nil || r.code == 0 || !strings.Contains(r.err, "CRATE_TEST_UNSET is not set") || strings.Contains(r.out, "ran") {
		t.Errorf("run -e of an unset name: expected a refusal that names it, and nothing run, got %v %s", err, r)
	}
}

// --- the record ---

// up is a run on the person's record: a JSON line per step it took and a total, and its summary on stderr. A
// second up finds the tools member made, so it takes no setup.
func TestUpRecordsItsRun(t *testing.T) {
	t.Parallel()
	w := newWs(t, "")
	first := w.mustRun(w.root, "up")
	second := w.mustRun(w.root, "up")
	data, err := os.ReadFile(filepath.Join(w.dir, "state", "crate", "log.jsonl"))
	if err != nil {
		t.Fatalf("the record: %v", err)
	}
	runs := map[string][]string{}
	var order []string
	for _, l := range lines(string(data)) {
		var line struct{ Run, Kind, Repo, System, Commit, Step, Result, Note string }
		if err := json.Unmarshal([]byte(l), &line); err != nil {
			t.Fatalf("not a JSON line: %q", l)
		}
		if line.Kind != "work" || line.System != w.name || line.Repo == "" || len(line.Commit) != 40 {
			t.Errorf("a line lost the run's fields: %q", l)
		}
		if _, seen := runs[line.Run]; !seen {
			order = append(order, line.Run)
		}
		runs[line.Run] = append(runs[line.Run], line.Step+":"+line.Result)
	}
	if len(order) != 2 {
		t.Fatalf("expected two runs, got %d:\n%s", len(order), data)
	}
	if got, want := strings.Join(runs[order[0]], " "), "hold:pass image:pass services:pass tools:pass setup:pass total:pass"; got != want {
		t.Errorf("the first up: expected %q, got %q", want, got)
	}
	if got, want := strings.Join(runs[order[1]], " "), "hold:pass image:pass services:pass tools:pass total:pass"; got != want {
		t.Errorf("the second up: expected %q, got %q", want, got)
	}
	for _, r := range []result{first, second} {
		if !strings.Contains(r.err, "crate: hold ") || !strings.Contains(r.err, "; total ") {
			t.Errorf("up: expected its summary on stderr, got %q", r.err)
		}
	}
}

// --- the verbs and -C ---

// The verb table gains up, run, services, logs, down and name; each gives the same from another folder with
// -C as from inside. Up twice changes nothing, a stopped member starts again, an attempt in a checkout whose
// workspace is up is refused, and down leaves nothing.
func TestTheWorkspaceVerbsAndDashC(t *testing.T) {
	t.Parallel()
	w := newWs(t, "")
	away := t.TempDir()
	w.mustRun(away, "-C", w.root, "up")
	if r := w.mustRun(away, "-C", w.root, "name"); strings.TrimSpace(r.out) != w.name {
		t.Errorf("name with -C: expected %q, got %q", w.name, strings.TrimSpace(r.out))
	}
	if got := w.podman("inspect", w.name+"-tools", "--format", "{{.Id}}"); got == "" {
		t.Fatal("the tools member: expected it made")
	}

	// logs, by the system`s name, from inside and with -C.
	w.mustRun(w.root, "run", "wget", "-qO-", "http://postgres:5432/")
	if r := w.mustRun(away, "-C", w.root, "logs", "postgres"); !strings.Contains(r.out, "service-up") {
		t.Errorf("logs postgres with -C: expected service-up, got %q", r.out)
	}

	// services made again, with the member's start time kept.
	oldPod := w.podman("pod", "inspect", w.name+"-postgres", "--format", "{{.Id}}")
	oldTools := w.podman("inspect", w.name+"-tools", "--format", "{{.State.StartedAt}}")
	w.mustRun(away, "-C", w.root, "services")
	if got := w.podman("pod", "inspect", w.name+"-postgres", "--format", "{{.Id}}"); got == oldPod {
		t.Errorf("services with -C: expected a new pod, still %s", oldPod)
	}
	if got := w.podman("inspect", w.name+"-tools", "--format", "{{.State.StartedAt}}"); got != oldTools {
		t.Errorf("services: expected the tools member's start %q to stay, got %q", oldTools, got)
	}

	// up twice changes nothing, and a stopped member is started again.
	tools := w.podman("inspect", w.name+"-tools", "--format", "{{.Id}}")
	w.mustRun(away, "-C", w.root, "up")
	if got := w.podman("inspect", w.name+"-tools", "--format", "{{.Id}}"); got != tools {
		t.Errorf("up twice: expected the member %s to stay, got %s", tools, got)
	}
	w.podman("stop", "-t", "0", w.name+"-tools")
	w.mustRun(away, "-C", w.root, "up")
	if got := w.podman("inspect", w.name+"-tools", "--format", "{{.State.Running}}"); got != "true" {
		t.Errorf("a stopped member: expected it running again, got %q", got)
	}

	// an attempt in a checkout whose workspace is up is refused, and says why.
	attempt, err := w.run(w.root, "up", "--attempt")
	if err != nil {
		t.Fatalf("crate up --attempt: %v", err)
	}
	if attempt.code != 1 || !hasReasonLine(attempt.err) || !strings.Contains(attempt.err, "attempt") {
		t.Errorf("up --attempt with the workspace up: expected exit 1 and a reason that names the attempt, got %s", attempt)
	}

	// down with -C leaves nothing; a second down changes nothing.
	w.mustRun(away, "-C", w.root, "down")
	if left := w.leftovers(); len(left) > 0 {
		t.Errorf("after down: expected nothing, got %q", left)
	}
	if r := w.mustRun(away, "-C", w.root, "down"); r.err != "" {
		t.Errorf("a second down: expected no output, got %q", r.err)
	}
}
