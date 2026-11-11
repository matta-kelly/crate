package crate_test

// The tests of crate's release and rollback: `just release <version>` and `just rollback <version>`, on real
// git and `just`, in a copy of this repo whose tests are emptied (so the release's check does not run these
// tests again), with a bare repo for GitHub's git and a stand-in `gh`. The stand-in keeps its releases in
// $T/gh.releases (one tag per line) and each release's asset in $T/gh.assets/<tag>.tgz, and logs each call to
// $T/gh.calls. GH_VISIBILITY says what `gh repo view` reports (PUBLIC), GH_FAIL_ONCE=upload fails the first
// `gh release upload` only.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"debug/elf"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const ghStub = `#!/usr/bin/env bash
echo "$*" >>"$T/gh.calls"
has() { grep -qxF -- "$1" "$T/gh.releases"; }
case "$1 $2" in
"repo view")
  vis=${GH_VISIBILITY:-PUBLIC}
  case "$*" in
  *--jq* | *" -q "*) echo "$vis" ;;
  *--json*) echo "{\"visibility\":\"$vis\"}" ;;
  *) printf 'name: matta-kelly/crate\nvisibility: %s\n' "$vis" ;;
  esac
  ;;
"release view") has "$3" ;;
"release create")
  tag=$3
  has "$tag" && { echo "gh: release $tag already exists" >&2; exit 1; }
  echo "$tag" >>"$T/gh.releases"
  shift 3
  for a; do f=${a%%#*}; [ -f "$f" ] && cp "$f" "$T/gh.assets/$tag.tgz"; done
  ;;
"release upload")
  tag=$3
  has "$tag" || { echo "gh: release $tag not found" >&2; exit 1; }
  if [ "${GH_FAIL_ONCE:-}" = upload ] && [ ! -e "$T/gh.failed" ]; then
    : >"$T/gh.failed"
    echo "gh: upload failed: connection reset" >&2
    exit 1
  fi
  shift 3
  clobber=0
  for a; do [ "$a" = --clobber ] && clobber=1; done
  for a; do
    f=${a%%#*}
    [ -f "$f" ] || continue
    if [ -e "$T/gh.assets/$tag.tgz" ] && [ $clobber = 0 ]; then echo "gh: asset exists" >&2; exit 1; fi
    cp "$f" "$T/gh.assets/$tag.tgz"
  done
  ;;
"release download")
  tag=$3
  shift 3
  out=
  dir=.
  while [ $# -gt 0 ]; do
    case $1 in
    -O | --output) out=$2; shift ;;
    -D | --dir) dir=$2; shift ;;
    esac
    shift
  done
  [ -e "$T/gh.assets/$tag.tgz" ] || { echo "gh: no asset on $tag" >&2; exit 1; }
  cp "$T/gh.assets/$tag.tgz" "${out:-$dir/crate-linux-amd64.tar.gz}"
  ;;
esac
`

const trivialStub = "#!/usr/bin/env bash\nexit 0\n"

const asset = "crate-linux-amd64.tar.gz"

type rel struct {
	t      *testing.T
	dir    string // T
	work   string
	github string
	bin    string
	just   string
}

type outcome struct {
	out, err string
	code     int
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func gitEnv() []string {
	return []string{"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"}
}

func (r *rel) git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitEnv()...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	must(t, filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(target, b, info.Mode().Perm())
		}
	}))
}

// newRel makes the copy of this repo as a clone of a bare repo "GitHub": commit 1 holds the tree as it is,
// commit 2 the same with every test emptied. Main is clean and equal to GitHub's.
func newRel(t *testing.T) *rel {
	t.Helper()
	just, err := exec.LookPath("just")
	if err != nil {
		t.Fatalf("just is needed: %v", err)
	}
	dir := t.TempDir()
	r := &rel{t: t, dir: dir, work: filepath.Join(dir, "work"), github: filepath.Join(dir, "github.git"), bin: filepath.Join(dir, "bin"), just: just}
	must(t, os.MkdirAll(r.bin, 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, "gh.assets"), 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, "tmp"), 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, "home"), 0o755))
	for _, n := range []string{"gh.calls", "gh.releases"} {
		must(t, os.WriteFile(filepath.Join(dir, n), nil, 0o644))
	}
	for name, body := range map[string]string{"gh": ghStub, "podman": trivialStub, "shellcheck": trivialStub, "docker": trivialStub} {
		must(t, os.WriteFile(filepath.Join(r.bin, name), []byte(body), 0o755))
	}
	root, err := os.Getwd()
	must(t, err)
	copyTree(t, root, r.work)
	r.git(dir, "init", "-q", "--bare", "-b", "main", r.github)
	r.git(r.work, "init", "-q", "-b", "main")
	r.git(r.work, "remote", "add", "origin", r.github)
	r.git(r.work, "add", "-A")
	r.git(r.work, "commit", "-qm", "start", "--allow-empty")
	r.git(r.work, "push", "-q", "origin", "main")
	// every test emptied, so the release's check runs no test of this repo again
	must(t, filepath.WalkDir(r.work, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(r.work, path)
		switch {
		case strings.HasPrefix(rel, ".git"+string(filepath.Separator)):
		case strings.HasSuffix(rel, "_test.go"):
			return os.Remove(path)
		case strings.HasPrefix(rel, "test"+string(filepath.Separator)) && strings.HasSuffix(rel, ".test.sh"):
			return os.WriteFile(path, []byte(trivialStub), 0o755)
		}
		return nil
	}))
	r.git(r.work, "add", "-A")
	r.git(r.work, "commit", "-qm", "empty the tests")
	r.git(r.work, "push", "-q", "origin", "main")
	return r
}

func (r *rel) env() []string {
	return append(os.Environ(),
		"PATH="+r.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"T="+r.dir, "HOME="+filepath.Join(r.dir, "home"), "TMPDIR="+filepath.Join(r.dir, "tmp"),
		"GIT_TERMINAL_PROMPT=0")
}

func (r *rel) just_(env []string, args ...string) outcome {
	r.t.Helper()
	cmd := exec.Command(r.just, args...)
	cmd.Dir = r.work
	cmd.Env = append(append(r.env(), gitEnv()...), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	done := make(chan error, 1)
	must(r.t, cmd.Start())
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(8 * time.Minute):
		_ = cmd.Process.Kill()
		r.t.Fatalf("just %v ran past 8 minutes", args)
	}
	o := outcome{out: out.String(), err: errb.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		o.code = ee.ExitCode()
	}
	return o
}

func (r *rel) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(r.dir, name))
	return string(b)
}

func (r *rel) originTags() string { return r.git(r.github, "tag", "--sort=v:refname") }

func (r *rel) originTag(tag string) string {
	cmd := exec.Command("git", "rev-parse", "-q", "--verify", "refs/tags/"+tag+"^{commit}")
	cmd.Dir = r.github
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

func (r *rel) head() string { return r.git(r.work, "rev-parse", "HEAD") }

func (o outcome) String() string {
	return "exit " + string(rune('0'+o.code%10)) + "\n  stdout: " + o.out + "\n  stderr: " + o.err
}

// tgz makes an asset: bin/crate, VERSION and COMMIT, and hooks/ when hooks is set.
func tgz(t *testing.T, version string, hooks bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := [][2]string{{"bin/crate", "binary of " + version}, {"VERSION", version + "\n"}, {"COMMIT", strings.Repeat("a", 40) + "\n"}}
	if hooks {
		files = append(files, [2]string{"hooks/pre-receive", "#!/bin/sh\n"})
	}
	for _, f := range files {
		must(t, tw.WriteHeader(&tar.Header{Name: f[0], Mode: 0o755, Size: int64(len(f[1]))}))
		_, err := tw.Write([]byte(f[1]))
		must(t, err)
	}
	must(t, tw.Close())
	must(t, gz.Close())
	return buf.Bytes()
}

// readTgz gives the entries of a tar.gz: name to content, and the names in order.
func readTgz(t *testing.T, b []byte) (map[string]string, []string) {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	must(t, err)
	tr := tar.NewReader(gz)
	files := map[string]string{}
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		must(t, err)
		body, err := io.ReadAll(tr)
		must(t, err)
		files[h.Name] = string(body)
		names = append(names, h.Name)
	}
	return files, names
}

// seedRelease puts a release with this asset in the stand-in (a nil asset is a release with none).
func (r *rel) seedRelease(tag string, asset []byte) {
	r.t.Helper()
	fh, err := os.OpenFile(filepath.Join(r.dir, "gh.releases"), os.O_APPEND|os.O_WRONLY, 0o644)
	must(r.t, err)
	_, err = fh.WriteString(tag + "\n")
	must(r.t, err)
	must(r.t, fh.Close())
	if asset != nil {
		must(r.t, os.WriteFile(filepath.Join(r.dir, "gh.assets", tag+".tgz"), asset, 0o644))
	}
}

func (r *rel) assetOf(tag string) []byte {
	b, _ := os.ReadFile(filepath.Join(r.dir, "gh.assets", tag+".tgz"))
	return b
}

// tagOnOrigin makes a tag at the commit in the work repo and pushes it.
func (r *rel) tagOnOrigin(tag, commit string) {
	r.git(r.work, "tag", "-f", tag, commit)
	r.git(r.work, "push", "-q", "-f", "origin", tag)
}

// --- the dry run ---

func TestADryRunLeavesTheAssetAndTouchesNothingOutside(t *testing.T) {
	r := newRel(t)
	o := r.just_(nil, "release", "2.0.0", "--dry-run")
	if o.code != 0 {
		t.Fatalf("the exit: expected 0, got %v", o)
	}
	var found []string
	must(t, filepath.WalkDir(r.dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == asset && !strings.Contains(path, "gh.assets") {
			found = append(found, path)
		}
		return nil
	}))
	if len(found) != 1 {
		t.Fatalf("expected %s in one folder, found %v", asset, found)
	}
	b, err := os.ReadFile(found[0])
	must(t, err)
	files, names := readTgz(t, b)
	for _, n := range names {
		if strings.HasPrefix(n, "./") || strings.HasPrefix(n, "/") || strings.HasPrefix(n, "crate-") || strings.HasPrefix(n, "dist/") {
			t.Errorf("expected every entry at the root with no folder before it, got %q", n)
		}
	}
	for _, want := range []string{"bin/crate", "VERSION", "COMMIT"} {
		if _, ok := files[want]; !ok {
			t.Errorf("expected the entry %q, got %v", want, names)
		}
	}
	if files["VERSION"] != "2.0.0\n" {
		t.Errorf("VERSION: expected %q, got %q", "2.0.0\n", files["VERSION"])
	}
	if want := r.head() + "\n"; files["COMMIT"] != want {
		t.Errorf("COMMIT: expected the full commit %q, got %q", want, files["COMMIT"])
	}
	// the binary is static, and says its version and commit
	bin := filepath.Join(t.TempDir(), "crate")
	must(t, os.WriteFile(bin, []byte(files["bin/crate"]), 0o755))
	ef, err := elf.Open(bin)
	if err != nil {
		t.Fatalf("bin/crate: expected an ELF binary, got %v", err)
	}
	defer ef.Close()
	libs, _ := ef.ImportedLibraries()
	if ef.Section(".interp") != nil || len(libs) != 0 || ef.Machine != elf.EM_X86_64 {
		t.Errorf("bin/crate: expected a static amd64 binary, got interp %v, libs %v, machine %v", ef.Section(".interp") != nil, libs, ef.Machine)
	}
	v, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(v), "2.0.0") || !strings.Contains(string(v), r.head()) {
		t.Errorf("bin/crate --version: expected the version and the full commit, got %q, %v", v, err)
	}
	if tags := r.originTags(); tags != "" {
		t.Errorf("a dry run: expected no tag on GitHub's git, got %q", tags)
	}
	if local := r.git(r.work, "tag"); local != "" {
		t.Errorf("a dry run: expected no tag here, got %q", local)
	}
	if calls := r.read("gh.calls"); strings.Contains(calls, "release create") || strings.Contains(calls, "release upload") {
		t.Errorf("a dry run: expected no release made, got\n%s", calls)
	}
}

// --- a release ---

func TestAReleaseTagsAndPutsTheAssetOnTheVersionAndTheMajor(t *testing.T) {
	rows := []struct {
		name    string
		version string
		major   string
		seed    bool // the major's tag and release exist, at the first commit
	}{
		{"a first 2.0.0", "2.0.0", "v2", false},
		{"a 1.x, with the major moved", "1.4.0", "v1", true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			r := newRel(t)
			first := r.git(r.work, "rev-parse", "HEAD~1")
			if row.seed {
				r.tagOnOrigin(row.major, first)
				r.seedRelease(row.major, []byte("old asset"))
			}
			o := r.just_(nil, "release", row.version)
			if o.code != 0 {
				t.Fatalf("the exit: expected 0, got %v", o)
			}
			head := r.head()
			for _, tag := range []string{"v" + row.version, row.major} {
				if got := r.originTag(tag); got != head {
					t.Errorf("tag %s on GitHub's git: expected %s, got %q", tag, head, got)
				}
			}
			releases := lines(r.read("gh.releases"))
			for _, tag := range []string{"v" + row.version, row.major} {
				if !contains(releases, tag) {
					t.Errorf("expected a release %s, got %v", tag, releases)
				}
			}
			versionAsset, majorAsset := r.assetOf("v"+row.version), r.assetOf(row.major)
			if len(versionAsset) == 0 || !bytes.Equal(versionAsset, majorAsset) {
				t.Errorf("expected the same asset on v%s and %s, got %d and %d bytes", row.version, row.major, len(versionAsset), len(majorAsset))
			}
			files, _ := readTgz(t, versionAsset)
			if files["VERSION"] != row.version+"\n" || files["COMMIT"] != head+"\n" {
				t.Errorf("the asset: expected VERSION %q and COMMIT %q, got %q and %q", row.version+"\n", head+"\n", files["VERSION"], files["COMMIT"])
			}
			calls := r.read("gh.calls")
			view := strings.Index(calls, "repo view")
			create := strings.Index(calls, "release ")
			if view < 0 || create < 0 || view > create {
				t.Errorf("gh calls: expected `repo view` before the first release call, got\n%s", calls)
			}
		})
	}
}

func TestAReleaseThatIsRefusedMakesNothing(t *testing.T) {
	rows := []struct {
		name    string
		version string
		env     []string
		prep    func(r *rel)
		code    int
	}{
		{"a repo that is not public", "2.0.0", []string{"GH_VISIBILITY=PRIVATE"}, func(r *rel) {}, 1},
		{"a checkout with an uncommitted change", "2.0.0", nil, func(r *rel) {
			must(r.t, os.WriteFile(filepath.Join(r.work, "README.md"), []byte("changed\n"), 0o644))
		}, 1},
		{"a main that is not GitHub's main", "2.0.0", nil, func(r *rel) {
			must(r.t, os.WriteFile(filepath.Join(r.work, "extra.txt"), []byte("x\n"), 0o644))
			r.git(r.work, "add", "-A")
			r.git(r.work, "commit", "-qm", "not pushed")
		}, 1},
		{"a branch that is not main", "2.0.0", nil, func(r *rel) { r.git(r.work, "checkout", "-q", "-b", "other") }, 1},
		{"a version that is not major.minor.patch", "2.0", nil, func(r *rel) {}, 2},
		{"a check that fails", "2.0.0", nil, func(r *rel) {
			must(r.t, os.WriteFile(filepath.Join(r.work, "test", "hooks.test.sh"), []byte("#!/usr/bin/env bash\necho red >&2\nexit 1\n"), 0o755))
			r.git(r.work, "add", "-A")
			r.git(r.work, "commit", "-qm", "red")
			r.git(r.work, "push", "-q", "origin", "main")
		}, 1},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			r := newRel(t)
			row.prep(r)
			o := r.just_(row.env, "release", row.version)
			if o.code != row.code {
				t.Errorf("the exit: expected %d, got %v", row.code, o)
			}
			if tags := r.originTags(); tags != "" {
				t.Errorf("expected no tag on GitHub's git, got %q", tags)
			}
			if calls := r.read("gh.calls"); strings.Contains(calls, "release create") || strings.Contains(calls, "release upload") {
				t.Errorf("expected no release made, got\n%s", calls)
			}
		})
	}
}

func TestAReleaseThatFailedGoesOnFromTheStepThatFailed(t *testing.T) {
	r := newRel(t)
	first := r.git(r.work, "rev-parse", "HEAD~1")
	r.tagOnOrigin("v2", first)
	r.seedRelease("v2", []byte("old asset"))
	env := []string{"GH_FAIL_ONCE=upload"}
	one := r.just_(env, "release", "2.0.0")
	if one.code != 1 || !strings.Contains(one.err, "upload") {
		t.Fatalf("the first run: expected exit 1 and the step that failed, got %v", one)
	}
	tagged := r.originTag("v2.0.0")
	if tagged != r.head() {
		t.Errorf("after the failed run: expected v2.0.0 kept at %s, got %q", r.head(), tagged)
	}
	two := r.just_(env, "release", "2.0.0")
	if two.code != 0 {
		t.Fatalf("the second run: expected exit 0, got %v", two)
	}
	if got := r.originTag("v2.0.0"); got != tagged {
		t.Errorf("the tag: expected v2.0.0 kept at %s, got %q", tagged, got)
	}
	if got := r.originTag("v2"); got != r.head() {
		t.Errorf("the major tag: expected %s, got %q", r.head(), got)
	}
	if n := count(lines(r.read("gh.releases")), "v2.0.0"); n != 1 {
		t.Errorf("the release v2.0.0: expected it made once, got %d", n)
	}
	if !bytes.Equal(r.assetOf("v2"), r.assetOf("v2.0.0")) || bytes.Equal(r.assetOf("v2"), []byte("old asset")) {
		t.Errorf("the major's asset: expected it replaced by the version's")
	}
}

// --- a rollback ---

func TestARollbackMovesTheMajorAndCopiesTheAsset(t *testing.T) {
	rows := []struct {
		name     string
		oldHooks bool // the asset of the version to go back to
		newHooks bool // the asset on the major now
		refused  bool
	}{
		{"both have hooks", true, true, false},
		{"neither has hooks", false, false, false},
		{"the old one has hooks and the major has none", true, false, false},
		{"the old one lacks hooks that the major has", false, true, true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			r := newRel(t)
			first, second := r.git(r.work, "rev-parse", "HEAD~1"), r.head()
			oldAsset, newAsset := tgz(t, "1.1.0", row.oldHooks), tgz(t, "1.2.0", row.newHooks)
			r.tagOnOrigin("v1.1.0", first)
			r.tagOnOrigin("v1.2.0", second)
			r.tagOnOrigin("v1", second)
			r.seedRelease("v1.1.0", oldAsset)
			r.seedRelease("v1.2.0", newAsset)
			r.seedRelease("v1", newAsset)
			o := r.just_(nil, "rollback", "1.1.0")
			if row.refused {
				if o.code != 1 || !strings.Contains(o.err, "hooks") {
					t.Errorf("the exit: expected 1 and a reason that names hooks, got %v", o)
				}
				if got := r.originTag("v1"); got != second {
					t.Errorf("the major tag: expected it unmoved at %s, got %q", second, got)
				}
				if !bytes.Equal(r.assetOf("v1"), newAsset) {
					t.Errorf("the major's asset: expected it unchanged")
				}
				return
			}
			if o.code != 0 {
				t.Fatalf("the exit: expected 0, got %v", o)
			}
			if got := r.originTag("v1"); got != first {
				t.Errorf("the major tag: expected it moved to %s, got %q", first, got)
			}
			if !bytes.Equal(r.assetOf("v1"), oldAsset) {
				t.Errorf("the major's asset: expected the asset of v1.1.0")
			}
			if got := r.originTag("v1.2.0"); got != second {
				t.Errorf("v1.2.0: expected it kept at %s, got %q", second, got)
			}
		})
	}
}

// --- the check ---

func TestTheCheckRunsGoVetAndGoTestAndTheFourShellTests(t *testing.T) {
	cmd := exec.Command("just", "--dry-run", "check")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("just --dry-run check: %v\n%s", err, out)
	}
	for _, want := range []string{"go vet", "go test ./...", "test/hooks.test.sh", "test/apply.test.sh", "test/release.test.sh", "test/postsubmit.test.sh"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the check recipe: expected %q, got\n%s", want, out)
		}
	}
	list, err := exec.Command("just", "--summary").Output()
	must(t, err)
	for _, recipe := range []string{"check", "release", "rollback"} {
		if !regexp.MustCompile(`(^|\s)` + recipe + `(\s|$)`).Match(list) {
			t.Errorf("the recipes: expected %q, got %q", recipe, list)
		}
	}
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func contains(xs []string, x string) bool { return count(xs, x) > 0 }

func count(xs []string, x string) int {
	n := 0
	for _, v := range xs {
		if v == x {
			n++
		}
	}
	return n
}
