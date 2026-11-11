package devimage_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matta-kelly/crate/internal/devimage"
	"github.com/matta-kelly/crate/internal/machine"
)

// A stand-in podman and skopeo first on PATH. Each appends its arguments as one line to $T/calls. The registry
// ($T/registry) and the store ($T/local) start empty, so a build is needed.
const podmanStub = `#!/usr/bin/env bash
echo "podman $*" >>"$T/calls"
case $1 in
build) tag=; while [ $# -gt 0 ]; do [ "$1" = -t ] && tag=$2; shift; done; echo "$tag" >>"$T/local" ;;
tag) echo "$3" >>"$T/local" ;;
push) echo "$2 sha256:$(echo "$2" | sha256sum | cut -c1-64)" >>"$T/registry" ;;
pull) exit 1 ;;
image) case $2 in exists) grep -qxF -- "$3" "$T/local" ;; esac ;;
esac
`

const skopeoStub = `#!/usr/bin/env bash
echo "skopeo $*" >>"$T/calls"
ref=${!#}; ref=${ref#docker://}
d=$(awk -v r="$ref" '$1 == r {print $2}' "$T/registry" | tail -1)
[ -n "$d" ] || { echo "manifest unknown: $ref" >&2; exit 1; }
echo "$d"
`

const smokeStub = "#!/usr/bin/env bash\necho \"smoke $*\" >>\"$T/calls\"\n"

// given: a checkout in a folder que-pasa with the three inputs and a smoke, and the stand-ins on PATH.
func given(t *testing.T) (root, calls string) {
	t.Helper()
	dir := t.TempDir()
	root = filepath.Join(dir, "que-pasa")
	files := map[string]string{
		".devcontainer/devcontainer.json": "a\n", ".devcontainer/Containerfile": "b\n", "package-lock.json": "c\n",
		"scripts/dev-image-smoke.sh": smokeStub,
	}
	for p, c := range files {
		full := filepath.Join(root, p)
		must(t, os.MkdirAll(filepath.Dir(full), 0o755))
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		must(t, os.WriteFile(full, []byte(c), mode))
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	bin := filepath.Join(dir, "bin")
	must(t, os.MkdirAll(bin, 0o755))
	must(t, os.WriteFile(filepath.Join(bin, "podman"), []byte(podmanStub), 0o755))
	must(t, os.WriteFile(filepath.Join(bin, "skopeo"), []byte(skopeoStub), 0o755))
	for _, n := range []string{"calls", "registry", "local"} {
		must(t, os.WriteFile(filepath.Join(dir, n), nil, 0o644))
	}
	t.Setenv("T", dir)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CRATE_MACHINE", filepath.Join(dir, "none.toml"))
	return root, filepath.Join(dir, "calls")
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func buildLine(t *testing.T, calls string) string {
	t.Helper()
	b, err := os.ReadFile(calls)
	must(t, err)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "podman build") {
			return l
		}
	}
	t.Fatalf("expected a podman build in the calls, got\n%s", b)
	return ""
}

// The build runs in the slice of the placement its caller gives, and in none with none.
func TestTheBuildTakesItsPlacementFromItsCaller(t *testing.T) {
	rows := []struct {
		name      string
		placement machine.Placement
		wantFlag  string // "" for none
	}{
		{"a slice", "crate-work.slice", "--cgroup-parent=crate-work.slice"},
		{"another slice", "user-1000.slice/crate-build-7.slice", "--cgroup-parent=user-1000.slice/crate-build-7.slice"},
		{"none", "", ""},
	}
	verbs := []struct {
		name string
		do   func(o devimage.Options, p machine.Placement) (string, error)
	}{{"Local", devimage.Local}, {"Ensure", devimage.Ensure}}
	for _, verb := range verbs {
		for _, row := range rows {
			t.Run(verb.name+" with "+row.name, func(t *testing.T) {
				root, calls := given(t)
				var stderr bytes.Buffer
				got, err := verb.do(devimage.Options{Root: root, Stderr: &stderr}, row.placement)
				if err != nil || got == "" {
					t.Fatalf("expected a tag or a ref and no error, got %q, %v\n%s", got, err, stderr.String())
				}
				line := buildLine(t, calls)
				if row.wantFlag == "" {
					if strings.Contains(line, "--cgroup-parent") {
						t.Errorf("with no placement: expected no --cgroup-parent, got %q", line)
					}
				} else if !strings.Contains(" "+line+" ", " "+row.wantFlag+" ") {
					t.Errorf("expected the build to hold the argument %q, got %q", row.wantFlag, line)
				}
			})
		}
	}
}

// The placement is for the build alone: no other call of the image names a cgroup.
func TestOnlyTheBuildNamesTheSlice(t *testing.T) {
	root, calls := given(t)
	var stderr bytes.Buffer
	if got, err := devimage.Ensure(devimage.Options{Root: root, Stderr: &stderr}, "crate-work.slice"); err != nil || got == "" {
		t.Fatalf("expected a ref and no error, got %q, %v\n%s", got, err, stderr.String())
	}
	b, err := os.ReadFile(calls)
	must(t, err)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, "crate-work.slice") && !strings.HasPrefix(l, "podman build") {
			t.Errorf("a call other than the build names the slice: %q", l)
		}
	}
}
