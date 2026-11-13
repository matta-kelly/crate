package main_test

// The fixture of the dev-image tests: a git repo that holds the template's three inputs, and stand-in
// `skopeo`, `podman`, `systemd-run` and `systemctl` first on PATH. Each stand-in appends its arguments as
// one line to $T/calls. The registry is $T/registry, one `<tag> <digest>` per line: `skopeo inspect` answers
// a tag listed there, and `skopeo copy` and `podman push` add their target tag to it. This machine's store is
// $T/local, one tag per line: `podman build -t`, `podman tag` and `podman pull` add a tag, `podman image
// exists` reads it, and `podman image rm` removes one. The repo's smoke is a stand-in too: it logs
// `smoke <args>` and exits $SMOKE_EXIT. Env a test sets to steer the stand-ins:
//   FAIL=<podman subcommand>   that subcommand prints "podman <it> exploded" on stderr and exits 125
//   HANG=<subcommand|smoke>    that call never answers (podman <sub>, skopeo <sub>, or the smoke)
//   SKOPEO_ERR=<text>          skopeo inspect fails with this text, not "manifest unknown"
//   EXISTS_EXIT=<n>            podman image exists exits n
//   REMOTE_GUARD=1             podman fails any call that names a store path or a cgroup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	image      = "ghcr.io/matta-kelly/que-pasa-dev"
	goldenHash = "5408977388ce524d"
)

// crateBin is the built binary; TestMain builds it once.
var crateBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "crate-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	crateBin = filepath.Join(dir, "crate")
	if out, err := exec.Command("go", "build", "-o", crateBin, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "go build: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fixtureDeclaration declares the fixture: its name, and the template's three inputs. crate.toml is no input, so
// the golden hash is the inputs' alone.
const fixtureDeclaration = "name = \"que-pasa\"\n\n[dev]\ninputs = [\".devcontainer/devcontainer.json\", \".devcontainer/Containerfile\", \"package-lock.json\"]\n"

// the template's three inputs, each one fixed line, and one other file
var inputFiles = map[string]string{
	".devcontainer/devcontainer.json": "devcontainer.json fixture\n",
	".devcontainer/Containerfile":     "Containerfile fixture\n",
	"package-lock.json":               "package-lock.json fixture\n",
	"README.md":                       "readme\n",
}

const podmanStub = `#!/usr/bin/env bash
echo "podman $*" >>"$T/calls"
if [ "${REMOTE_GUARD:-}" = 1 ]; then
  case "$*" in
  *containers/storage* | */var/lib/containers* | *graphroot* | *cgroup*) echo "podman: a store path or a cgroup was named: $*" >&2; exit 99 ;;
  esac
fi
[ "${HANG:-}" != "$1" ] || exec sleep 60
if [ "${FAIL:-}" = "$1" ]; then echo "podman $1 exploded" >&2; exit 125; fi
case $1 in
push) echo "$2 sha256:$(echo "$2" | sha256sum | cut -c1-64)" >>"$T/registry" ;;
build)
  echo "STEP 1: building on stdout"
  tag=
  while [ $# -gt 0 ]; do [ "$1" = -t ] && tag=$2; shift; done
  echo "$tag" >>"$T/local"
  ;;
tag)
  grep -qxF -- "$2" "$T/local" || exit 1
  echo "$3" >>"$T/local"
  ;;
pull)
  echo "Trying to pull $2 on stdout"
  awk -v r="$2" '$1 == r {f=1} END {exit !f}' "$T/registry" && echo "$2" >>"$T/local" || { echo "Error: pull of $2: manifest unknown" >&2; exit 1; }
  ;;
image)
  case $2 in
  exists)
    [ -z "${EXISTS_EXIT:-}" ] || exit "$EXISTS_EXIT"
    grep -qxF -- "$3" "$T/local"
    ;;
  rm)
    grep -vxF -- "$3" "$T/local" >"$T/local.new"
    mv "$T/local.new" "$T/local"
    ;;
  esac
  ;;
esac
`

const skopeoStub = `#!/usr/bin/env bash
echo "skopeo $*" >>"$T/calls"
[ "${HANG:-}" != "skopeo $1" ] || exec sleep 60
case $1 in
inspect)
  ref=${!#}; ref=${ref#docker://}
  d=$(awk -v r="$ref" '$1 == r {print $2}' "$T/registry" | tail -1)
  if [ -z "$d" ]; then
    if [ -n "${SKOPEO_ERR:-}" ]; then echo "$SKOPEO_ERR" >&2; else echo "manifest unknown: $ref" >&2; fi
    exit 1
  fi
  echo "$d"
  ;;
copy)
  src=${*: -2:1}; dst=${!#}; src=${src#docker://}; dst=${dst#docker://}
  d=$(awk -v r="$src" '$1 == r {print $2}' "$T/registry" | tail -1)
  [ -n "$d" ] || exit 1
  echo "$dst $d" >>"$T/registry"
  ;;
esac
`

const smokeStub = `#!/usr/bin/env bash
echo "smoke $*" >>"$T/calls"
echo "smoke chatter on stdout"
[ "${HANG:-}" != smoke ] || exec sleep 60
exit "${SMOKE_EXIT:-0}"
`

const forbiddenStub = `#!/usr/bin/env bash
echo "FORBIDDEN $(basename "$0") $*" >>"$T/calls"
exit 1
`

// testSliceName is a slice name of the test's own, so a run's units are easy to find and stop.
func testSliceName() string { return fmt.Sprintf("crate-t%d-%d", os.Getpid(), time.Now().UnixNano()) }

// stopUserSlices stops every user slice whose name starts with prefix, deepest first, at the test's cleanup.
func stopUserSlices(t *testing.T, prefix string) {
	t.Helper()
	t.Cleanup(func() {
		out, _ := exec.Command("systemctl", "--user", "list-units", "--type=slice", "--all",
			"--plain", "--no-legend", "--no-pager", "--full").CombinedOutput()
		var units []string
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if name := fields[0]; strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".slice") {
				units = append(units, name)
			}
		}
		sort.Slice(units, func(i, j int) bool { return len(units[i]) > len(units[j]) })
		for _, unit := range units {
			_, _ = exec.Command("systemctl", "--user", "stop", unit).CombinedOutput()
		}
		for _, unit := range units {
			_, _ = exec.Command("systemctl", "--user", "reset-failed", unit).CombinedOutput()
		}
	})
}

// realSystemdPATH is the real systemctl and systemd-run before the fixture's stand-ins, so a test of the
// binary's real placement keeps the stand-in podman and skopeo it needs for the build.
func realSystemdPATH(t *testing.T, bin string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"systemctl", "systemd-run", "machinectl"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		must(t, os.Symlink(path, filepath.Join(dir, name)))
	}
	return dir + string(os.PathListSeparator) + bin + string(os.PathListSeparator) + os.Getenv("PATH")
}

// fx is one fixture: T holds the repo, the stand-ins and their files.
type fx struct {
	t      *testing.T
	dir    string // T
	repo   string
	folder string
	bin    string
}

// result is one run of crate.
type result struct {
	out  string
	err  string
	code int
	took time.Duration
}

// newFx makes a git repo in a folder of that name, with the three inputs, the smoke and a README
// committed, and the stand-ins on PATH.
func newFx(t *testing.T, folder string) *fx {
	t.Helper()
	dir := t.TempDir()
	f := &fx{t: t, dir: dir, folder: folder, repo: filepath.Join(dir, folder), bin: filepath.Join(dir, "bin")}
	must(t, os.MkdirAll(f.bin, 0o755))
	must(t, os.MkdirAll(f.repo, 0o755))
	for _, s := range []struct{ name, body string }{
		{"podman", podmanStub}, {"skopeo", skopeoStub},
		{"systemd-run", forbiddenStub}, {"systemctl", forbiddenStub}, {"machinectl", forbiddenStub},
	} {
		must(t, os.WriteFile(filepath.Join(f.bin, s.name), []byte(s.body), 0o755))
	}
	for _, n := range []string{"calls", "registry", "local"} {
		must(t, os.WriteFile(filepath.Join(dir, n), nil, 0o644))
	}
	for p, c := range inputFiles {
		f.write(p, c, 0o644)
	}
	f.write("scripts/dev-image-smoke.sh", smokeStub, 0o755)
	f.write("crate.toml", fixtureDeclaration, 0o644)
	f.git(f.repo, "init", "-q", "-b", "main")
	f.git(f.repo, "add", "-A")
	f.git(f.repo, "commit", "-qm", "init")
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fx) write(path, content string, mode os.FileMode) {
	f.t.Helper()
	full := filepath.Join(f.repo, path)
	must(f.t, os.MkdirAll(filepath.Dir(full), 0o755))
	must(f.t, os.WriteFile(full, []byte(content), mode))
}

// commit writes the file and commits it.
func (f *fx) commit(path, content string) {
	f.t.Helper()
	f.write(path, content, 0o644)
	f.git(f.repo, "add", "-A")
	f.git(f.repo, "commit", "-qm", "change "+path)
}

// remove deletes the file and commits that.
func (f *fx) remove(path string) {
	f.t.Helper()
	f.git(f.repo, "rm", "-q", path)
	f.git(f.repo, "commit", "-qm", "remove "+path)
}

// script writes an executable script and commits it.
func (f *fx) script(path, content string) {
	f.t.Helper()
	f.write(path, content, 0o755)
	f.git(f.repo, "add", "-A")
	f.git(f.repo, "commit", "-qm", "script "+path)
}

func (f *fx) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = f.gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *fx) gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
}

func (f *fx) appendTo(name, line string) {
	f.t.Helper()
	fh, err := os.OpenFile(filepath.Join(f.dir, name), os.O_APPEND|os.O_WRONLY, 0o644)
	must(f.t, err)
	defer fh.Close()
	_, err = fmt.Fprintln(fh, line)
	must(f.t, err)
}

// inRegistry puts <tag> in the stand-in registry with digest n.
func (f *fx) inRegistry(tag string, n int) { f.appendTo("registry", tag+" "+digest(n)) }

// onMachine puts <tag> in the stand-in store.
func (f *fx) onMachine(tag string) { f.appendTo("local", tag) }

func (f *fx) read(name string) string {
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	must(f.t, err)
	return string(b)
}

func (f *fx) calls() string    { return f.read("calls") }
func (f *fx) registry() string { return f.read("registry") }
func (f *fx) local() string    { return f.read("local") }

func digest(n int) string { return fmt.Sprintf("sha256:%064d", n) }

// hashNow is the hash crate gives at the checkout.
func (f *fx) hashNow() string {
	f.t.Helper()
	r := f.run(f.repo, nil, "dev-image", "hash")
	return strings.TrimSpace(r.out)
}

// run runs crate in cwd with extra env (KEY=VALUE), and the args.
func (f *fx) run(cwd string, env []string, args ...string) result {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, crateBin, args...)
	cmd.Dir = cwd
	base := []string{
		"PATH=" + f.bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"T=" + f.dir, "HOME=" + filepath.Join(f.dir, "home"),
		"CRATE_MACHINE=" + filepath.Join(f.dir, "no-machine.toml"),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	}
	cmd.Env = append(filterEnv(os.Environ(), "PATH", "CRATE_MACHINE", "CONTAINER_HOST", "FAIL", "HANG", "SKOPEO_ERR",
		"REMOTE_GUARD", "EXISTS_EXIT", "SMOKE_EXIT", "CRATE_LIMIT_SCALE", "ENGINE", "DEV_IMAGE", "DEV_IMAGE_SMOKE"), base...)
	cmd.Env = append(cmd.Env, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	start := time.Now()
	err := cmd.Run()
	r := result{out: out.String(), err: errb.String(), took: time.Since(start)}
	if ee, ok := err.(*exec.ExitError); ok {
		r.code = ee.ExitCode()
	} else if err != nil {
		f.t.Fatalf("run crate %v: %v", args, err)
	}
	return r
}

// dev runs `crate dev-image <args>` in the repo.
func (f *fx) dev(env []string, args ...string) result {
	f.t.Helper()
	return f.run(f.repo, env, append([]string{"dev-image"}, args...)...)
}

func filterEnv(env []string, drop ...string) []string {
	var kept []string
next:
	for _, e := range env {
		for _, d := range drop {
			if strings.HasPrefix(e, d+"=") {
				continue next
			}
		}
		kept = append(kept, e)
	}
	return kept
}

// lines is the non-empty lines of s.
func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// callsMatching is the lines of the call log that start with one of the prefixes, in order.
func (f *fx) callsMatching(prefixes ...string) string {
	var out []string
	for _, l := range lines(f.calls()) {
		for _, p := range prefixes {
			if strings.HasPrefix(l, p) {
				out = append(out, l)
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

func count(s, sub string) int { return strings.Count(s, sub) }

func (r result) String() string {
	return fmt.Sprintf("exit %d\n  stdout: %q\n  stderr: %q", r.code, r.out, r.err)
}

// hasReasonLine says some line of stderr starts with `crate:`.
func hasReasonLine(stderr string) bool { return reasonLineCount(stderr) > 0 }

// reasonLineCount is how many lines of stderr start with `crate:`.
func reasonLineCount(stderr string) int {
	n := 0
	for _, l := range lines(stderr) {
		if strings.HasPrefix(l, "crate:") {
			n++
		}
	}
	return n
}
