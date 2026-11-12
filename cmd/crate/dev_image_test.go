package main_test

// The tests of `crate dev-image`, ported from kinetic's templates/repo/scripts/dev-image.test.sh (the cases
// from `# --- hash ---` to `# --- usage ---`), and the cases the Go verb adds. They run the built binary,
// the way the gate and a workflow call it.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// golden values for the fixture of helpers_test.go: the template's three inputs, each one fixed line
// (".devcontainer/devcontainer.json", ".devcontainer/Containerfile" and "package-lock.json", each
// "<file name> fixture"), mode 100644. The hash is `git ls-tree -r HEAD -- <inputs> | sha256sum | cut -c1-16`
// of the tree entries, so one set of bytes gives one hash on every machine, whatever the author and date.
//
// Where they come from: the rule of kinetic's templates/repo/scripts/dev-image.sh `hash`, at lot commit
// 8e2589cb55dba221ace165a1a987cb01b11d123d (kinetic/templates/repo/scripts/dev-image.sh). The git blob
// ids of the three files, sha1("blob <size>\0<bytes>") for each, were worked out apart from git and the
// script, and put through the rule's listing and sha256. goldenA and goldenB are the same rule over
// the inputs a [dev] table lists: the Containerfile and package-lock.json (A); the Containerfile and
// README.md (B). The test reads no other repo and holds no copy of the script.
const (
	goldenA = "e24ffb7dd004cd7c"
	goldenB = "dc73925ad6e7a75e"
)

const goldenRef = "ghcr.io/matta-kelly/que-pasa-dev@sha256:0000000000000000000000000000000000000000000000000000000000000007"

// --- hash ---

func TestHashIsStable(t *testing.T) {
	f := newFx(t, "que-pasa")
	first := f.dev(nil, "hash")
	second := f.dev(nil, "hash")
	if first.code != 0 {
		t.Fatalf("the exit: expected 0, got %s", first)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}\n$`).MatchString(first.out) {
		t.Errorf("the hash: expected 16 lowercase hex and a newline, got %q", first.out)
	}
	if first.out != second.out {
		t.Errorf("the second hash: expected %q, got %q", first.out, second.out)
	}
}

func TestHashFailsOnAMissingInput(t *testing.T) {
	f := newFx(t, "que-pasa")
	f.remove("package-lock.json")
	r := f.dev(nil, "hash")
	if r.code != 1 {
		t.Errorf("the exit: expected 1, got %s", r)
	}
	if !strings.Contains(r.err, "package-lock.json") || !hasReasonLine(r.err) {
		t.Errorf("stderr: expected a `crate:` line that names package-lock.json, got %q", r.err)
	}
	if r.out != "" {
		t.Errorf("stdout: expected nothing, got %q", r.out)
	}
}

func TestHashFollowsEachInput(t *testing.T) {
	f := newFx(t, "que-pasa")
	for _, in := range []string{".devcontainer/devcontainer.json", ".devcontainer/Containerfile", "package-lock.json"} {
		before := f.hashNow()
		f.commit(in, in+" changed\n")
		after := f.hashNow()
		if before == after || after == "" {
			t.Errorf("the hash did not change with %s: before %q, after %q", in, before, after)
		}
	}
}

func TestHashIgnoresOtherFiles(t *testing.T) {
	f := newFx(t, "que-pasa")
	before := f.hashNow()
	f.commit("README.md", "more\n")
	if after := f.hashNow(); before == "" || after != before {
		t.Errorf("the hash: expected %q to stay, got %q", before, after)
	}
}

// --- ensure ---

func TestEnsureBuildsNothingWhenTheTagExists(t *testing.T) {
	f := newFx(t, "que-pasa")
	f.inRegistry(image+":"+goldenHash, 7)
	r := f.dev(nil, "ensure")
	calls := f.calls()
	if r.code != 0 {
		t.Fatalf("the exit: expected 0, got %s", r)
	}
	for _, not := range []string{"podman build", "podman push", "smoke", "skopeo copy"} {
		if strings.Contains(calls, not) {
			t.Errorf("the calls: expected no %q, got\n%s", not, calls)
		}
	}
	if want := image + "@" + digest(7) + "\n"; r.out != want {
		t.Errorf("stdout: expected %q, got %q", want, r.out)
	}
	if !strings.Contains(r.err, image+":"+goldenHash+" exists: not built") {
		t.Errorf("stderr: expected it to say the tag exists, got %q", r.err)
	}
}

func TestEnsureBuildsSmokesAndPushesWhenTheTagIsMissing(t *testing.T) {
	f := newFx(t, "que-pasa")
	r := f.dev(nil, "ensure")
	tag := image + ":" + goldenHash
	want := strings.Join([]string{
		"podman build -f .devcontainer/Containerfile -t " + tag + "-candidate .",
		"smoke " + tag + "-candidate",
		"podman tag " + tag + "-candidate " + tag,
		"podman push " + tag,
	}, "\n")
	if r.code != 0 {
		t.Fatalf("the exit: expected 0, got %s", r)
	}
	if got := f.callsMatching("podman build", "podman tag", "podman push", "smoke", "skopeo copy"); got != want {
		t.Errorf("the calls, in order: expected\n%s\ngot\n%s", want, got)
	}
	if !strings.Contains(f.calls(), "podman image rm "+tag+"-candidate") {
		t.Errorf("the calls: expected the candidate to be dropped, got\n%s", f.calls())
	}
	if want := image + "@sha256:"; !strings.HasPrefix(r.out, want) || len(lines(r.out)) != 1 {
		t.Errorf("stdout: expected one line, the ref of the pushed tag, got %q", r.out)
	}
}

func TestEnsureMovesNothingOnAFailedSmoke(t *testing.T) {
	f := newFx(t, "que-pasa")
	r := f.dev([]string{"SMOKE_EXIT=1"}, "ensure")
	if r.code != 1 {
		t.Errorf("the exit: expected 1, got %s", r)
	}
	for _, not := range []string{"podman push", "skopeo copy"} {
		if strings.Contains(f.calls(), not) {
			t.Errorf("the calls: expected no %q, got\n%s", not, f.calls())
		}
	}
}

// --- a failed smoke leaves no image ---

func TestAFailedSmokeLeavesNoImage(t *testing.T) {
	f := newFx(t, "que-pasa")
	tag := image + ":" + goldenHash
	env := []string{"SMOKE_EXIT=1"}
	ensure := f.dev(env, "ensure")
	if ensure.code != 1 || strings.Contains(f.local(), tag+"\n") {
		t.Errorf("after a failed ensure: expected exit 1 and no %s on this machine, got %s\nstore: %q", tag, ensure, f.local())
	}
	local := f.dev(env, "local")
	if local.code != 1 || strings.Contains(f.local(), tag+"\n") {
		t.Errorf("after a failed local: expected exit 1 and no %s on this machine, got %s\nstore: %q", tag, local, f.local())
	}
	if strings.Contains(f.local(), tag+"-candidate") {
		t.Errorf("after both: expected no candidate on this machine, got %q", f.local())
	}
}

func TestAFailedSmokeDoesNotLeaveAGoodImage(t *testing.T) {
	f := newFx(t, "que-pasa")
	first := f.dev([]string{"SMOKE_EXIT=1"}, "local")
	second := f.dev([]string{"SMOKE_EXIT=0"}, "local")
	if first.code != 1 {
		t.Errorf("the first exit: expected 1, got %s", first)
	}
	if want := image + ":" + goldenHash + "\n"; second.code != 0 || second.out != want {
		t.Errorf("the second run: expected exit 0 and %q, got %s", want, second)
	}
	if n := count(f.calls(), "podman build"); n != 2 {
		t.Errorf("the builds: expected 2, got %d", n)
	}
	if n := count(f.calls(), "smoke "); n != 2 {
		t.Errorf("the smokes: expected 2, got %d", n)
	}
}

// --- promote ---

func TestPromotePointsMainAtTheCommitsTag(t *testing.T) {
	f := newFx(t, "que-pasa")
	f.inRegistry(image+":"+goldenHash, 3)
	r := f.dev(nil, "promote")
	if r.code != 0 {
		t.Fatalf("the exit: expected 0, got %s", r)
	}
	want := "skopeo copy --preserve-digests docker://" + image + ":" + goldenHash + " docker://" + image + ":main"
	if !strings.Contains(f.calls(), want) {
		t.Errorf("the calls: expected %q, got\n%s", want, f.calls())
	}
	if want := image + "@" + digest(3) + "\n"; r.out != want {
		t.Errorf("stdout: expected %q, got %q", want, r.out)
	}
}

func TestPromoteFailsWhenTheTagIsMissing(t *testing.T) {
	f := newFx(t, "que-pasa")
	r := f.dev(nil, "promote")
	if r.code != 1 {
		t.Errorf("the exit: expected 1, got %s", r)
	}
	if strings.Contains(f.calls(), "skopeo copy") {
		t.Errorf("the calls: expected no skopeo copy, got\n%s", f.calls())
	}
	if !strings.Contains(r.err, image+":"+goldenHash) {
		t.Errorf("stderr: expected it to name %s:%s, got %q", image, goldenHash, r.err)
	}
}

// --- local ---

func TestLocalUsesTheMachinesCopy(t *testing.T) {
	f := newFx(t, "que-pasa")
	tag := image + ":" + goldenHash
	f.onMachine(tag)
	r := f.dev(nil, "local")
	if r.code != 0 || r.out != tag+"\n" {
		t.Errorf("expected exit 0 and %q, got %s", tag, r)
	}
	for _, not := range []string{"podman pull", "podman build"} {
		if strings.Contains(f.calls(), not) {
			t.Errorf("the calls: expected no %q, got\n%s", not, f.calls())
		}
	}
}

func TestLocalPullsFromGhcr(t *testing.T) {
	f := newFx(t, "que-pasa")
	tag := image + ":" + goldenHash
	f.inRegistry(tag, 4)
	r := f.dev(nil, "local")
	if r.code != 0 || r.out != tag+"\n" {
		t.Errorf("expected exit 0 and %q, got %s", tag, r)
	}
	if !strings.Contains(f.calls(), "podman pull "+tag) {
		t.Errorf("the calls: expected a pull of %s, got\n%s", tag, f.calls())
	}
	if strings.Contains(f.calls(), "podman build") {
		t.Errorf("the calls: expected no build, got\n%s", f.calls())
	}
}

func TestLocalBuildsWhenNoOneHasIt(t *testing.T) {
	f := newFx(t, "que-pasa")
	tag := image + ":" + goldenHash
	r := f.dev(nil, "local")
	if r.code != 0 || r.out != tag+"\n" {
		t.Errorf("expected exit 0 and %q, got %s", tag, r)
	}
	calls := f.calls()
	for _, want := range []string{
		"podman build -f .devcontainer/Containerfile -t " + tag + "-candidate .",
		"smoke " + tag + "-candidate",
		"podman tag " + tag + "-candidate " + tag,
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("the calls: expected %q, got\n%s", want, calls)
		}
	}
	for _, not := range []string{"podman push", "skopeo copy"} {
		if strings.Contains(calls, not) {
			t.Errorf("the calls: expected no %q, got\n%s", not, calls)
		}
	}
}

func TestLocalFailsOnAFailedSmoke(t *testing.T) {
	f := newFx(t, "que-pasa")
	if r := f.dev([]string{"SMOKE_EXIT=1"}, "local"); r.code != 1 {
		t.Errorf("the exit: expected 1, got %s", r)
	}
}

// --- ref ---

func TestRefPrefersTheCommitsOwnTag(t *testing.T) {
	f := newFx(t, "que-pasa")
	f.inRegistry(image+":"+goldenHash, 1)
	f.inRegistry(image+":main", 2)
	r := f.dev(nil, "ref")
	if want := image + "@" + digest(1) + "\n"; r.code != 0 || r.out != want {
		t.Errorf("expected exit 0 and %q, got %s", want, r)
	}
}

func TestRefFallsBackToMain(t *testing.T) {
	f := newFx(t, "que-pasa")
	f.inRegistry(image+":main", 2)
	r := f.dev(nil, "ref")
	if want := image + "@" + digest(2) + "\n"; r.code != 0 || r.out != want {
		t.Errorf("expected exit 0 and %q, got %s", want, r)
	}
}

func TestRefFailsWhenNeitherExists(t *testing.T) {
	f := newFx(t, "que-pasa")
	r := f.dev(nil, "ref")
	if r.code != 1 {
		t.Errorf("the exit: expected 1, got %s", r)
	}
	for _, want := range []string{"neither", image + ":" + goldenHash, image + ":main"} {
		if !strings.Contains(r.err, want) {
			t.Errorf("stderr: expected it to hold %q, got %q", want, r.err)
		}
	}
	if r.out != "" {
		t.Errorf("stdout: expected nothing, got %q", r.out)
	}
}

// --- usage ---

func TestUsageExits2(t *testing.T) {
	f := newFx(t, "que-pasa")
	rows := []struct {
		name string
		args []string
	}{
		{"no verb", nil},
		{"an unknown verb", []string{"no-such-verb"}},
		{"no sub-verb", []string{"dev-image"}},
		{"an unknown sub-verb", []string{"dev-image", "no-such-command"}},
		{"an unknown flag", []string{"dev-image", "hash", "--no-such-flag"}},
		{"-C after the sub-verb", []string{"dev-image", "hash", "-C", f.repo}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			r := f.run(f.repo, nil, row.args...)
			if r.code != 2 {
				t.Errorf("the exit: expected 2, got %s", r)
			}
			if !strings.Contains(r.err, "usage:") || !strings.Contains(r.err, "crate") {
				t.Errorf("stderr: expected it to say how to use crate, got %q", r.err)
			}
			if r.out != "" {
				t.Errorf("stdout: expected nothing, got %q", r.out)
			}
		})
	}
}

// --- what the Go verb adds to the template's cases ---

func TestEachVerbPrintsOneLineOnStdout(t *testing.T) {
	// A build's, a pull's and a smoke's output go to stderr: the stand-ins print on stdout, and it must not show.
	rows := []struct {
		name  string
		setup func(f *fx)
		verb  string
		want  string
	}{
		{"hash", func(f *fx) {}, "hash", goldenHash},
		{"ref", func(f *fx) { f.inRegistry(image+":"+goldenHash, 7) }, "ref", goldenRef},
		{"ensure that builds", func(f *fx) {}, "ensure", ""},
		{"promote", func(f *fx) { f.inRegistry(image+":"+goldenHash, 7) }, "promote", goldenRef},
		{"local that builds", func(f *fx) {}, "local", image + ":" + goldenHash},
		{"local that pulls", func(f *fx) { f.inRegistry(image+":"+goldenHash, 7) }, "local", image + ":" + goldenHash},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := newFx(t, "que-pasa")
			row.setup(f)
			r := f.dev(nil, row.verb)
			if r.code != 0 {
				t.Fatalf("the exit: expected 0, got %s", r)
			}
			if got := lines(r.out); len(got) != 1 || !strings.HasSuffix(r.out, "\n") {
				t.Errorf("stdout: expected exactly one line, got %q", r.out)
			}
			if row.want != "" && r.out != row.want+"\n" {
				t.Errorf("stdout: expected %q, got %q", row.want, r.out)
			}
			if row.verb == "local" && row.name == "local that builds" && !strings.Contains(r.err, "STEP 1: building on stdout") {
				t.Errorf("stderr: expected the build's output, got %q", r.err)
			}
		})
	}
}

func TestEveryFailureHasOneReasonLineOnStderr(t *testing.T) {
	rows := []struct {
		name string
		verb string
		env  []string
		prep func(f *fx)
	}{
		{"ref with neither tag", "ref", nil, func(f *fx) {}},
		{"promote with no tag", "promote", nil, func(f *fx) {}},
		{"hash with a missing input", "hash", nil, func(f *fx) { f.remove("package-lock.json") }},
		{"local with a failed smoke", "local", []string{"SMOKE_EXIT=1"}, func(f *fx) {}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := newFx(t, "que-pasa")
			row.prep(f)
			r := f.dev(row.env, row.verb)
			if r.code != 1 || !hasReasonLine(r.err) || r.out != "" {
				t.Errorf("expected exit 1, a `crate:` reason line on stderr and nothing on stdout, got %s", r)
			}
		})
	}
}

func TestARegistryThatFailsIsNotAMissingTag(t *testing.T) {
	rows := []struct{ name, skopeo string }{
		{"no answer", "dial tcp 140.82.112.34:443: connect: connection refused"},
		{"a refused login", "unauthorized: authentication required"},
		{"a 5xx", "received unexpected HTTP status: 503 Service Unavailable"},
	}
	for _, row := range rows {
		for _, verb := range []string{"ref", "ensure", "promote"} {
			t.Run(row.name+" "+verb, func(t *testing.T) {
				f := newFx(t, "que-pasa")
				r := f.dev([]string{"SKOPEO_ERR=" + row.skopeo}, verb)
				if r.code != 1 {
					t.Errorf("the exit: expected 1, got %s", r)
				}
				for _, want := range []string{"ghcr.io", goldenHash, row.skopeo} {
					if !strings.Contains(r.err, want) {
						t.Errorf("stderr: expected it to hold %q, got %q", want, r.err)
					}
				}
				if strings.Contains(r.err, "neither") {
					t.Errorf("stderr: a failed registry is not a missing tag, got %q", r.err)
				}
				for _, not := range []string{"podman build", "podman push", "skopeo copy", "smoke "} {
					if strings.Contains(f.calls(), not) {
						t.Errorf("the calls: expected no %q after a failed ask, got\n%s", not, f.calls())
					}
				}
			})
		}
	}
}

// ghcr answers "denied" for a package that does not exist yet, and "name unknown" for an image with no package:
// for ensure each is a missing tag, so a new repo's first image is made.
func TestEnsureBuildsAndPushesWhenGhcrAnswersDeniedOrNameUnknown(t *testing.T) {
	for _, msg := range []string{"denied: requested access to the resource is denied", "name unknown: repository name not known to registry"} {
		t.Run(msg, func(t *testing.T) {
			f := newFx(t, "que-pasa")
			r := f.dev([]string{"SKOPEO_ERR=" + msg}, "ensure")
			tag := image + ":" + goldenHash
			if r.code != 0 || !strings.HasPrefix(r.out, image+"@sha256:") || len(lines(r.out)) != 1 {
				t.Errorf("expected exit 0 and one line, the ref, got %s", r)
			}
			for _, want := range []string{"podman build", "podman push " + tag} {
				if !strings.Contains(f.calls(), want) {
					t.Errorf("the calls: expected %q, got\n%s", want, f.calls())
				}
			}
		})
	}
}

func TestDeniedIsAFailureForRefAndPromoteAndNameUnknownIsAMissingTag(t *testing.T) {
	const denied = "denied: requested access to the resource is denied"
	const unknown = "name unknown: repository name not known to registry"
	for _, verb := range []string{"ref", "promote"} {
		t.Run("denied "+verb, func(t *testing.T) {
			f := newFx(t, "que-pasa")
			r := f.dev([]string{"SKOPEO_ERR=" + denied}, verb)
			if r.code != 1 || !strings.Contains(r.err, denied) || !strings.Contains(r.err, goldenHash) || strings.Contains(r.err, "neither") || r.out != "" {
				t.Errorf("expected exit 1 and a failure that names the tag and skopeo's message, got %s", r)
			}
		})
	}
	t.Run("name unknown ref", func(t *testing.T) {
		f := newFx(t, "que-pasa")
		r := f.dev([]string{"SKOPEO_ERR=" + unknown}, "ref")
		if r.code != 1 || !strings.Contains(r.err, "neither") {
			t.Errorf("expected exit 1 and `neither ... exists`, got %s", r)
		}
	})
	t.Run("name unknown promote", func(t *testing.T) {
		f := newFx(t, "que-pasa")
		r := f.dev([]string{"SKOPEO_ERR=" + unknown}, "promote")
		if r.code != 1 || !strings.Contains(r.err, "ensure") || !strings.Contains(r.err, image+":"+goldenHash) {
			t.Errorf("expected exit 1 and the missing tag with `run ensure first`, got %s", r)
		}
	})
}

func TestAStoreThatAnswersNeitherYesNorNoIsAFailure(t *testing.T) {
	f := newFx(t, "que-pasa")
	f.inRegistry(image+":"+goldenHash, 4)
	r := f.dev([]string{"EXISTS_EXIT=125"}, "local")
	if r.code != 1 || !strings.Contains(r.err, "125") || !hasReasonLine(r.err) || r.out != "" {
		t.Errorf("expected exit 1 and a `crate:` line that names the exit 125, got %s", r)
	}
	for _, not := range []string{"podman pull", "podman build"} {
		if strings.Contains(f.calls(), not) {
			t.Errorf("the calls: expected no %q, got\n%s", not, f.calls())
		}
	}
}

func TestALocalPullThatFailsGoesOnToABuild(t *testing.T) {
	f := newFx(t, "que-pasa")
	tag := image + ":" + goldenHash
	f.inRegistry(tag, 4)
	r := f.dev([]string{"FAIL=pull"}, "local")
	if r.code != 0 || r.out != tag+"\n" {
		t.Errorf("expected exit 0 and %q, got %s", tag, r)
	}
	if !strings.Contains(f.calls(), "podman build") {
		t.Errorf("the calls: expected a build after the failed pull, got\n%s", f.calls())
	}
	if !strings.Contains(r.err, "podman pull exploded") {
		t.Errorf("stderr: expected the reason the pull failed, got %q", r.err)
	}
}

func TestAFailedStepExitsWithTheStepAndLeavesNoImage(t *testing.T) {
	tag := image + ":" + goldenHash
	rows := []struct {
		name string
		fail string
		verb string
	}{
		{"build", "build", "local"},
		{"build in ensure", "build", "ensure"},
		{"tag", "tag", "local"},
		{"tag in ensure", "tag", "ensure"},
		{"push", "push", "ensure"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := newFx(t, "que-pasa")
			r := f.dev([]string{"FAIL=" + row.fail}, row.verb)
			if r.code != 1 {
				t.Errorf("the exit: expected 1, got %s", r)
			}
			if !strings.Contains(r.err, row.fail) || !strings.Contains(r.err, "podman "+row.fail+" exploded") || !hasReasonLine(r.err) {
				t.Errorf("stderr: expected a `crate:` line that names the step %q, with its output, got %q", row.fail, r.err)
			}
			if r.out != "" {
				t.Errorf("stdout: expected nothing, got %q", r.out)
			}
			store := f.local()
			if strings.Contains(store, tag+"\n") || strings.Contains(store, tag+"-candidate") {
				t.Errorf("this machine: expected neither %s nor its candidate, got %q", tag, store)
			}
		})
	}
}

func TestAGitFailureExitsWithGitsMessage(t *testing.T) {
	f := newFx(t, "que-pasa")
	r := f.dev(nil, "hash", "--rev", "no-such-commit")
	if r.code != 1 || !hasReasonLine(r.err) || !strings.Contains(r.err, "Not a valid object name") {
		t.Errorf("expected exit 1 and a `crate:` line with git's message, got %s", r)
	}
	outside := t.TempDir()
	r = f.run(outside, nil, "dev-image", "hash")
	if r.code != 1 || !hasReasonLine(r.err) || r.out != "" {
		t.Errorf("from a folder that is no checkout: expected exit 1 and a `crate:` line, got %s", r)
	}
}

func TestACallPastItsLimitStopsAndSaysWhatItWaitedFor(t *testing.T) {
	// CRATE_LIMIT_SCALE (a whole number) divides every limit by one factor, so each limit becomes about a second here.
	// A stand-in that never answers must be stopped at its limit: not at once, and not never.
	rows := []struct {
		name  string
		limit float64 // seconds
		hang  string
		verb  string
		want  string
		reg   bool
	}{
		{"skopeo inspect", 30, "skopeo inspect", "ref", "skopeo inspect", false},
		{"skopeo copy", 600, "skopeo copy", "promote", "skopeo copy", true},
		{"podman push", 900, "push", "ensure", "podman push", false},
		{"podman build", 1800, "build", "local", "podman build", false},
		{"the smoke", 600, "smoke", "local", "smoke", false},
		{"podman image exists", 30, "image", "local", "image exists", false},
	}
	timeLike := regexp.MustCompile(`\d+(\.\d+)?\s?(ms|s|m|min)\b`)
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := newFx(t, "que-pasa")
			if row.reg {
				f.inRegistry(image+":"+goldenHash, 3)
			}
			scale := int(row.limit)
			r := f.dev([]string{"HANG=" + row.hang, "CRATE_LIMIT_SCALE=" + strconv.Itoa(scale)}, row.verb)
			if r.code != 1 {
				t.Errorf("the exit: expected 1, got %s", r)
			}
			if !strings.Contains(r.err, row.want) || !timeLike.MatchString(r.err) || !hasReasonLine(r.err) {
				t.Errorf("stderr: expected a `crate:` line that names %q and how long it waited, got %q", row.want, r.err)
			}
			if r.took.Seconds() < 0.7 || r.took.Seconds() > 15 {
				t.Errorf("the wait: expected about one second (the limit, scaled), got %v", r.took)
			}
		})
	}
}

// --- the image name ---

func TestAScaleThatIsNotAWholeNumberOfOneOrMoreIsRefusedAndNamed(t *testing.T) {
	for _, bad := range []string{"0", "-3", "1.5", "abc", "0.01"} {
		t.Run(bad, func(t *testing.T) {
			f := newFx(t, "que-pasa")
			f.inRegistry(image+":"+goldenHash, 7)
			r := f.dev([]string{"CRATE_LIMIT_SCALE=" + bad}, "ref")
			if r.code != 1 || !strings.Contains(r.err, "CRATE_LIMIT_SCALE") || !strings.Contains(r.err, bad) || !hasReasonLine(r.err) || r.out != "" {
				t.Errorf("expected exit 1 and a `crate:` line that names CRATE_LIMIT_SCALE and %q, got %s", bad, r)
			}
		})
	}
}

func TestTheImageNameFollowsItsOrder(t *testing.T) {
	// Each row gives the image name through `ref`: the stand-in registry holds the tag under the name the row
	// wants, so any other name finds no tag and fails. Every row has the fixture's three inputs: one hash.
	rows := []struct {
		name   string
		folder string // the checkout's folder
		toml   string // the crate.toml, or none
		cwd    string // "repo", "worktree" (folder x), or "bare" (a clone, que-pasa.git, asked with --rev)
		args   []string
		want   string
	}{
		{"a checkout in the folder que-pasa", "que-pasa", "", "repo", nil, "ghcr.io/matta-kelly/que-pasa-dev"},
		{"a worktree of it in a folder x", "que-pasa", "", "worktree", nil, "ghcr.io/matta-kelly/que-pasa-dev"},
		{"a clone in a folder work, with --name", "work", "", "repo", []string{"--name", "que-pasa"}, "ghcr.io/matta-kelly/que-pasa-dev"},
		{"a clone in a folder work, with no name", "work", "", "repo", nil, "ghcr.io/matta-kelly/work-dev"},
		{"crate.toml name over --name", "work", "name = \"que-pasa\"\n", "repo", []string{"--name", "other"}, "ghcr.io/matta-kelly/que-pasa-dev"},
		{"crate.toml name over the folder", "work", "name = \"alpha\"\n", "repo", nil, "ghcr.io/matta-kelly/alpha-dev"},
		{"--name over the folder", "work", "", "repo", []string{"--name", "beta"}, "ghcr.io/matta-kelly/beta-dev"},
		{"name and [dev] with no image", "work", "name = \"que-pasa\"\n\n[dev]\nsmoke = \"scripts/dev-image-smoke.sh\"\n", "repo", nil, "ghcr.io/matta-kelly/que-pasa-dev"},
		{"[dev] image over name", "work", "name = \"que-pasa\"\n\n[dev]\nimage = \"example.test/team/custom-dev\"\n", "repo", []string{"--name", "other"}, "example.test/team/custom-dev"},
		{"a bare repo que-pasa.git with --rev", "que-pasa", "", "bare", nil, "ghcr.io/matta-kelly/que-pasa-dev"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := newFx(t, row.folder)
			if row.toml != "" {
				f.commit("crate.toml", row.toml)
			}
			f.inRegistry(row.want+":"+goldenHash, 7)
			cwd := f.repo
			args := append([]string{"dev-image", "ref"}, row.args...)
			switch row.cwd {
			case "worktree":
				cwd = filepath.Join(f.dir, "x")
				f.git(f.repo, "worktree", "add", "-q", "-b", "wt", cwd)
			case "bare":
				cwd = filepath.Join(f.dir, "que-pasa.git")
				if row.folder != "que-pasa" {
					t.Fatal("the bare row clones into que-pasa.git beside the fixture")
				}
				f.git(f.dir, "clone", "-q", "--bare", f.repo, cwd)
				args = append(args, "--rev", f.git(f.repo, "rev-parse", "HEAD"))
			}
			r := f.run(cwd, nil, args...)
			if want := row.want + "@" + digest(7) + "\n"; r.code != 0 || r.out != want {
				t.Errorf("expected exit 0 and %q, got %s", want, r)
			}
		})
	}
}

func TestLocalTakesTheNameFlag(t *testing.T) {
	f := newFx(t, "work")
	r := f.dev(nil, "local", "--name", "que-pasa")
	if want := image + ":" + goldenHash + "\n"; r.code != 0 || r.out != want {
		t.Errorf("expected exit 0 and %q, got %s", want, r)
	}
}

// --- the same tag ---

func TestHashIsTheGoldenTag(t *testing.T) {
	f := newFx(t, "que-pasa")
	if r := f.dev(nil, "hash"); r.code != 0 || r.out != goldenHash+"\n" {
		t.Errorf("expected exit 0 and %q, got %s", goldenHash, r)
	}
}

func TestRefIsTheGoldenRef(t *testing.T) {
	f := newFx(t, "que-pasa")
	f.inRegistry(image+":"+goldenHash, 7)
	if r := f.dev(nil, "ref"); r.code != 0 || r.out != goldenRef+"\n" {
		t.Errorf("expected exit 0 and %q, got %s", goldenRef, r)
	}
}

// --- [dev] ---

func TestDevInputsAreExactlyWhatTheHashCovers(t *testing.T) {
	rows := []struct {
		name   string
		inputs string
		want   string
	}{
		{"the Containerfile and the lock", `[".devcontainer/Containerfile", "package-lock.json"]`, goldenA},
		{"the Containerfile and the README", `[".devcontainer/Containerfile", "README.md"]`, goldenB},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := newFx(t, "que-pasa")
			f.commit("crate.toml", "[dev]\ninputs = "+row.inputs+"\n")
			if r := f.dev(nil, "hash"); r.code != 0 || r.out != row.want+"\n" {
				t.Errorf("expected exit 0 and %q, got %s", row.want, r)
			}
		})
	}
	t.Run("a file the list leaves out", func(t *testing.T) {
		f := newFx(t, "que-pasa")
		f.commit("crate.toml", "[dev]\ninputs = [\".devcontainer/Containerfile\", \"package-lock.json\"]\n")
		f.commit(".devcontainer/devcontainer.json", "changed\n")
		if r := f.dev(nil, "hash"); r.out != goldenA+"\n" {
			t.Errorf("the hash: expected %q to stay when an unlisted input changes, got %s", goldenA, r)
		}
	})
}

func TestDevContainerfileMustBeAnInput(t *testing.T) {
	rows := []struct {
		name string
		toml string
		want string
	}{
		{"the default Containerfile", "[dev]\ninputs = [\"package-lock.json\"]\n", ".devcontainer/Containerfile"},
		{"a named Containerfile", "[dev]\ncontainerfile = \"docker/Containerfile\"\ninputs = [\".devcontainer/Containerfile\", \"package-lock.json\"]\n", "docker/Containerfile"},
	}
	for _, row := range rows {
		for _, verb := range []string{"hash", "local"} {
			t.Run(row.name+" "+verb, func(t *testing.T) {
				f := newFx(t, "que-pasa")
				f.commit("docker/Containerfile", "FROM scratch\n")
				f.commit("crate.toml", row.toml)
				r := f.dev(nil, verb)
				if r.code != 1 || !strings.Contains(r.err, row.want) || !hasReasonLine(r.err) || r.out != "" {
					t.Errorf("expected exit 1 and a `crate:` line that names %s, got %s", row.want, r)
				}
				if strings.Contains(f.calls(), "podman build") {
					t.Errorf("the calls: expected no build, got\n%s", f.calls())
				}
			})
		}
	}
}

func TestDevContainerfileIsWhatTheBuildReads(t *testing.T) {
	f := newFx(t, "que-pasa")
	f.commit("docker/Containerfile", "FROM scratch\n")
	f.commit("crate.toml", "[dev]\ncontainerfile = \"docker/Containerfile\"\ninputs = [\"docker/Containerfile\", \"package-lock.json\"]\n")
	r := f.dev(nil, "local")
	if r.code != 0 {
		t.Fatalf("the exit: expected 0, got %s", r)
	}
	if want := "podman build -f docker/Containerfile -t " + strings.TrimSpace(r.out) + "-candidate ."; !strings.Contains(f.calls(), want) {
		t.Errorf("the calls: expected %q, got\n%s", want, f.calls())
	}
}

func TestDevSmokeRunsWithTheImageAsItsArgument(t *testing.T) {
	f := newFx(t, "que-pasa")
	f.script("ci/own-smoke.sh", "#!/usr/bin/env bash\necho \"own-smoke $1 [$#]\" >>\"$T/calls\"\n")
	f.commit("crate.toml", "[dev]\nsmoke = \"ci/own-smoke.sh\"\n")
	r := f.dev(nil, "local")
	tag := image + ":" + goldenHash
	if r.code != 0 {
		t.Fatalf("the exit: expected 0, got %s", r)
	}
	if want := "own-smoke " + tag + "-candidate [1]"; !strings.Contains(f.calls(), want) {
		t.Errorf("the calls: expected %q, got\n%s", want, f.calls())
	}
	if got := f.callsMatching("smoke "); got != "" {
		t.Errorf("the calls: expected the default smoke not to run, got\n%s", got)
	}
}

// --- the folder and the flags ---

func TestDashCActsOnItsPathFromAnyFolder(t *testing.T) {
	// One row per verb that takes a checkout. Each later cl adds its verbs here.
	rows := []struct {
		name string
		args []string
		reg  bool
	}{
		{"dev-image hash", []string{"dev-image", "hash"}, false},
		{"dev-image ref", []string{"dev-image", "ref"}, true},
		{"dev-image local", []string{"dev-image", "local"}, false},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := newFx(t, "que-pasa")
			if row.reg {
				f.inRegistry(image+":"+goldenHash, 7)
			}
			f.commit("sub/deeper/file.txt", "x\n")
			other := t.TempDir()
			inside := f.run(f.repo, nil, row.args...)
			below := f.run(filepath.Join(f.repo, "sub", "deeper"), nil, row.args...)
			away := f.run(other, nil, append([]string{"-C", f.repo}, row.args...)...)
			rel := f.run(f.dir, nil, append([]string{"-C", "que-pasa"}, row.args...)...)
			if inside.code != 0 || inside.out == "" {
				t.Fatalf("from inside the checkout: expected exit 0 and a result, got %s", inside)
			}
			for name, got := range map[string]result{"from a folder below its root": below, "with -C from another folder": away, "with a relative -C": rel} {
				if got.code != inside.code || got.out != inside.out {
					t.Errorf("%s: expected %s\ngot %s", name, inside, got)
				}
			}
		})
	}
}

// --- a commit with no checkout ---

func TestRevReadsTheCommitsTreeFromABareRepo(t *testing.T) {
	f := newFx(t, "que-pasa")
	f.commit("crate.toml", "name = \"que-pasa\"\n\n[dev]\ninputs = [\".devcontainer/Containerfile\", \"package-lock.json\"]\n")
	first := f.git(f.repo, "rev-parse", "HEAD")
	// the head moves on: a new name and other inputs, and a changed lock
	f.commit("crate.toml", "name = \"other\"\n\n[dev]\ninputs = [\".devcontainer/Containerfile\", \"README.md\"]\n")
	f.commit("package-lock.json", "a newer lock\n")
	bare := filepath.Join(f.dir, "somewhere.git")
	f.git(f.dir, "clone", "-q", "--bare", f.repo, bare)
	checkout := filepath.Join(f.dir, "at-first")
	f.git(f.repo, "worktree", "add", "-q", "--detach", checkout, first)
	f.inRegistry("ghcr.io/matta-kelly/que-pasa-dev:"+goldenA, 7)

	hashBare := f.run(bare, nil, "dev-image", "hash", "--rev", first)
	hashCheckout := f.run(checkout, nil, "dev-image", "hash")
	refBare := f.run(bare, nil, "dev-image", "ref", "--rev", first)
	refCheckout := f.run(checkout, nil, "dev-image", "ref")
	if hashBare.code != 0 || hashBare.out != goldenA+"\n" || hashBare.out != hashCheckout.out {
		t.Errorf("hash --rev from a bare repo: expected %q as in a checkout of the commit (%s), got %s", goldenA, hashCheckout, hashBare)
	}
	if want := "ghcr.io/matta-kelly/que-pasa-dev@" + digest(7) + "\n"; refBare.code != 0 || refBare.out != want || refBare.out != refCheckout.out {
		t.Errorf("ref --rev from a bare repo: expected %q as in a checkout of the commit (%s), got %s", want, refCheckout, refBare)
	}
	if head := f.run(f.repo, nil, "dev-image", "hash", "--rev", "HEAD"); head.out == hashBare.out {
		t.Errorf("--rev HEAD in the checkout: expected the head's own hash, not the first commit's %q", hashBare.out)
	}
}

func TestRevIsRefusedByVerbsThatBuildFromACheckout(t *testing.T) {
	for _, verb := range []string{"local", "ensure", "promote"} {
		t.Run(verb, func(t *testing.T) {
			f := newFx(t, "que-pasa")
			r := f.dev(nil, verb, "--rev", "HEAD")
			if r.code != 2 || !strings.Contains(r.err, "checkout") {
				t.Errorf("expected exit 2 and a message that %s builds from a checkout, got %s", verb, r)
			}
			if strings.Contains(f.calls(), "podman") || strings.Contains(f.calls(), "skopeo") {
				t.Errorf("the calls: expected none, got\n%s", f.calls())
			}
		})
	}
}

// --- a remote engine ---

func TestARemoteEngineNeedsOnlyPodmanAndSkopeoOnThePath(t *testing.T) {
	f := newFx(t, "que-pasa")
	env := []string{"CONTAINER_HOST=unix:///run/podman/podman.sock", "REMOTE_GUARD=1"}
	tag := image + ":" + goldenHash
	f.inRegistry(image+":main", 2)
	hash := f.dev(env, "hash")
	ref := f.dev(env, "ref")
	local := f.dev(env, "local")
	ensure := f.dev(env, "ensure")
	for name, r := range map[string]result{"hash": hash, "ref": ref, "local": local, "ensure": ensure} {
		if r.code != 0 {
			t.Errorf("%s with CONTAINER_HOST and no machine file: expected exit 0, got %s", name, r)
		}
	}
	if local.out != tag+"\n" {
		t.Errorf("local: expected %q, got %q", tag, local.out)
	}
	calls := f.calls()
	if strings.Contains(calls, "--cgroup-parent") || strings.Contains(calls, "FORBIDDEN") {
		t.Errorf("the calls: expected no cgroup, systemd-run or user manager, got\n%s", calls)
	}
	allowed := regexp.MustCompile(`^(podman (image (exists|rm) |pull |build |tag |push )|skopeo (inspect|copy) |smoke )`)
	for _, l := range lines(calls) {
		if !allowed.MatchString(l) {
			t.Errorf("a call outside podman image exists, pull, build, tag, image rm and push, and skopeo: %q", l)
		}
	}
	if !strings.Contains(calls, "podman build") || !strings.Contains(calls, "podman push") {
		t.Errorf("the calls: expected a build and a push, got\n%s", calls)
	}
}

// --- the machine seam ---

func TestAMachineWithNoFileSaysOnceThatItHasNoCeiling(t *testing.T) {
	f := newFx(t, "que-pasa")
	local := f.dev(nil, "local")
	if n := count(local.err, "no ceiling"); n != 1 {
		t.Errorf("local that builds: expected stderr to say once that this machine has no ceiling, said %d times: %q", n, local.err)
	}
	if hash := f.dev(nil, "hash"); strings.Contains(hash.err, "no ceiling") {
		t.Errorf("hash starts no container: expected no word of a ceiling, got %q", hash.err)
	}
}

func TestAMachineWithAFileNeverSaysItHasNoCeiling(t *testing.T) {
	f := newFx(t, "que-pasa")
	slice := testSliceName()
	stopUserSlices(t, slice)
	file := filepath.Join(f.dir, "machine.toml")
	must(t, os.WriteFile(file, []byte("cpus = \"0-1\"\nmemory = \"512M\"\n\n[count]\ncheck = 2\npostsubmit = 1\nattempt = 4\n"), 0o644))
	r := f.dev([]string{"CRATE_MACHINE=" + file, "CRATE_SLICE=" + slice, "PATH=" + realSystemdPATH(t, f.bin)}, "local")
	if r.code != 0 || strings.Contains(r.err, "no ceiling") {
		t.Errorf("local that builds, with a machine file: expected exit 0 and no word of a ceiling, got %s", r)
	}
}

func TestBinCrateStaysTheShell(t *testing.T) {
	// bin/crate stays the shell here: the built binary goes to the release's folder and to go test, never to bin/.
	root, err := filepath.Abs("../..")
	must(t, err)
	b, err := os.ReadFile(filepath.Join(root, "bin", "crate"))
	must(t, err)
	if !strings.HasPrefix(string(b), "#!") {
		t.Errorf("bin/crate: expected the shell script, got %.20q", b)
	}
}
