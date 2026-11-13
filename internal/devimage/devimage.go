// Package devimage finds, builds and pushes a repo's dev image: one per hash of its inputs, built once
// wherever it is first needed.
package devimage

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/matta-kelly/crate/internal/limit"
	"github.com/matta-kelly/crate/internal/machine"
)

// Options says which repo and where the output of a build goes.
type Options struct {
	Root   string    // the checkout, or for Hash and Ref with a Rev any folder of a git repo that holds the commit
	Rev    string    // the commit Hash and Ref read, or "" for HEAD
	Stderr io.Writer // a build's, a pull's and a smoke's output
}

// Toplevel is the root of the checkout that holds dir.
func Toplevel(dir string) (string, error) {
	out, err := git(dir, "rev-parse", "--show-toplevel")
	return strings.TrimSpace(out), err
}

// RepoName is the repo's folder: the folder that holds the git common folder, the same for each worktree of
// a checkout. A bare repo is its own folder with .git taken away.
func RepoName(root string) (string, error) {
	out, err := git(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common := filepath.Clean(strings.TrimSpace(out))
	if filepath.Base(common) == ".git" {
		return filepath.Base(filepath.Dir(common)), nil
	}
	return strings.TrimSuffix(filepath.Base(common), ".git"), nil
}

// Hash is the hash of the inputs at the commit: 16 lowercase hex.
func Hash(o Options) (string, error) {
	p, err := open(o)
	if err != nil {
		return "", err
	}
	return p.hash()
}

// Ref is <name>@<digest> of <name>:<hash> if the registry has it, else of <name>:main.
func Ref(o Options) (string, error) {
	p, err := open(o)
	if err != nil {
		return "", err
	}
	hash, err := p.hash()
	if err != nil {
		return "", err
	}
	for _, tag := range []string{hash, "main"} {
		digest, found, err := p.digest(tag, false)
		if err != nil {
			return "", err
		}
		if found {
			return p.image + "@" + digest, nil
		}
	}
	return "", fmt.Errorf("neither %s:%s nor %s:main exists", p.image, hash, p.image)
}

// Ensure builds, smokes and pushes <image>:<hash> only when the registry lacks it, and returns its ref. Any
// change may run it: each set of inputs is built once.
func Ensure(o Options, placement machine.Placement) (string, error) {
	p, hash, err := openAtHash(o)
	if err != nil {
		return "", err
	}
	tag := p.image + ":" + hash
	_, found, err := p.digest(hash, true)
	if err != nil {
		return "", err
	}
	if found {
		fmt.Fprintf(p.stderr, "%s exists: not built\n", tag)
	} else {
		if err := p.buildTo(tag, placement); err != nil {
			return "", err
		}
		if err := p.podman("podman push", limit.Push(), p.stderr, "push", tag); err != nil {
			p.drop(tag)
			return "", err
		}
	}
	return p.refOf(hash)
}

// Promote points <image>:main at <image>:<hash>, also when nothing was built: a main that goes back to old
// inputs gets their image back.
func Promote(o Options) (string, error) {
	p, hash, err := openAtHash(o)
	if err != nil {
		return "", err
	}
	_, found, err := p.digest(hash, false)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("%s:%s is not in the registry: run ensure first", p.image, hash)
	}
	err = run(call{name: "skopeo copy", limit: limit.SkopeoCopy(), prog: "skopeo",
		args: []string{"copy", "--preserve-digests", "docker://" + p.image + ":" + hash, "docker://" + p.image + ":main"}},
		p.stderr, p.stderr)
	if err != nil {
		return "", err
	}
	return p.refOf(hash)
}

// Source is how Local got the image: the machine's copy, a pull, or a build.
type Source string

const (
	Stored Source = "stored"
	Pulled Source = "pulled"
	Built  Source = "built"
)

// Local puts <image>:<hash> in this machine's store and returns the tag and how it got there: the machine's copy,
// else a pull, else a build and a smoke with no push. The build runs in placement.
func Local(o Options, placement machine.Placement) (string, Source, error) {
	p, hash, err := openAtHash(o)
	if err != nil {
		return "", "", err
	}
	tag := p.image + ":" + hash
	present, err := p.stored(tag)
	if err != nil {
		return tag, "", err
	}
	if present {
		return tag, Stored, nil
	}
	source := Pulled
	if err = p.podman("podman pull", limit.Pull(), p.stderr, "pull", tag); err != nil {
		fmt.Fprintf(p.stderr, "crate: %v, so %s is built\n", err, tag)
		if err = p.buildTo(tag, placement); err != nil {
			return tag, "", err
		}
		source = Built
	}
	p.dropOlder(hash)
	return tag, source, nil
}

// hashTag is the shape of a dev image's tag: its inputs' hash.
var hashTag = regexp.MustCompile(`^[0-9a-f]{16}$`)

// dropOlder removes from this machine's store the repo's dev images of every other hash, once a new one is here, so
// the store keeps one per repo. An image a container still uses, a workspace's made from an older commit, stays:
// podman refuses to remove it, and that refusal is no error.
func (p *plan) dropOlder(hash string) {
	var out bytes.Buffer
	if err := run(call{name: "podman images", limit: limit.Podman(), dir: p.root, prog: "podman",
		args: []string{"images", "--format", "{{.Repository}}:{{.Tag}}", "--filter", "reference=" + p.image}}, &out, io.Discard); err != nil {
		return
	}
	for _, ref := range strings.Fields(out.String()) {
		repo, tag, ok := strings.Cut(ref, ":")
		if !ok || repo != p.image || tag == hash || !hashTag.MatchString(tag) {
			continue
		}
		_ = p.podman("podman image rm", limit.Podman(), io.Discard, "image", "rm", ref)
	}
}

func openAtHash(o Options) (*plan, string, error) {
	p, err := open(o)
	if err != nil {
		return nil, "", err
	}
	hash, err := p.hash()
	return p, hash, err
}

// digest is the manifest digest of <image>:<tag>. A registry that says the tag or the name is not there
// ("manifest unknown", "name unknown") gives found false. ghcr says "denied" for a package that does not exist
// yet, so ensure, which makes the package, reads it as missing too (deniedIsMissing). Any other failure of the
// ask is an error, never read as a missing tag.
func (p *plan) digest(tag string, deniedIsMissing bool) (digest string, found bool, err error) {
	ref := p.image + ":" + tag
	var out, errb bytes.Buffer
	err = run(call{name: "skopeo inspect", limit: limit.SkopeoInspect(), prog: "skopeo",
		args: []string{"inspect", "--format", "{{.Digest}}", "docker://" + ref}}, &out, &errb)
	if err == nil {
		return strings.TrimSpace(out.String()), true, nil
	}
	msg := strings.TrimSpace(errb.String())
	if strings.Contains(msg, "manifest unknown") || strings.Contains(msg, "name unknown") ||
		(deniedIsMissing && strings.Contains(msg, "denied")) {
		return "", false, nil
	}
	if msg == "" {
		msg = err.Error()
	}
	return "", false, fmt.Errorf("the registry, for %s: %s", ref, msg)
}

func (p *plan) refOf(hash string) (string, error) {
	digest, found, err := p.digest(hash, false)
	if err == nil && !found {
		err = fmt.Errorf("%s:%s is not in the registry after its push", p.image, hash)
	}
	return p.image + "@" + digest, err
}

// stored says this machine's store holds the tag. Exit 1 of `image exists` is its answer "no".
func (p *plan) stored(tag string) (bool, error) {
	err := p.podman("podman image exists", limit.Podman(), io.Discard, "image", "exists", tag)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// buildTo builds to <tag>-candidate and smokes that, then moves it to <tag>. The move is the last step, so
// no <tag> reaches this machine's store unless its smoke passed. The candidate is dropped either way.
func (p *plan) buildTo(tag string, placement machine.Placement) error {
	candidate := tag + "-candidate"
	defer p.drop(candidate)
	build := []string{"build"}
	if placement != "" {
		build = append(build, "--cgroup-parent="+string(placement))
	}
	build = append(build, "-f", p.containerfile, "-t", candidate, ".")
	if err := p.podman("podman build", limit.Build(), p.stderr, build...); err != nil {
		return err
	}
	smoke := filepath.Join(p.root, p.smoke)
	if err := run(call{name: "smoke " + p.smoke, limit: limit.Smoke(), dir: p.root, prog: smoke, args: []string{candidate}},
		p.stderr, p.stderr); err != nil {
		return err
	}
	return p.podman("podman tag", limit.Podman(), p.stderr, "tag", candidate, tag)
}

// drop takes a tag out of this machine's store, whether or not it is there.
func (p *plan) drop(tag string) {
	_ = p.podman("podman image rm", limit.Podman(), io.Discard, "image", "rm", tag)
}

// podman runs the engine from the PATH in the checkout, with all its output in out.
func (p *plan) podman(name string, d time.Duration, out io.Writer, args ...string) error {
	return run(call{name: name, limit: d, dir: p.root, prog: "podman", args: args}, out, out)
}
