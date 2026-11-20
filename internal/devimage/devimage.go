// Package devimage finds, builds and caches a repo's dev image: one per hash of its inputs, built where it is first
// needed and kept in the registry by that hash (cache.go).
package devimage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/matta-kelly/crate/internal/fault"
	"github.com/matta-kelly/crate/internal/limit"
	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/process"
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

// Source is how Local got the image: the machine's copy, a pull, or a build.
type Source string

const (
	Stored Source = "stored"
	Pulled Source = "pulled"
	Built  Source = "built"
)

// Local puts <image>:<hash> in this machine's store and returns the tag and how it got there: the machine's copy,
// else a pull, else a build and a smoke with no push. The pull, the build and the smoke run in placement, and the end of
// ctx stops each of them.
func Local(ctx context.Context, o Options, placement machine.Placement) (string, Source, error) {
	p, hash, err := openAtHash(o)
	if err != nil {
		return "", "", err
	}
	p.ctx = ctx
	tag := p.image + ":" + hash
	present, err := p.stored(tag)
	if err != nil {
		return tag, "", err
	}
	if present {
		return tag, Stored, nil
	}
	source := Pulled
	pull := append(append([]string{"pull"}, registryFlags(tag, "")...), tag)
	if err = p.podmanIn(placement, "podman pull", limit.Pull(), p.stderr, pull...); err != nil {
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
	if err := run(p.ctx, call{name: "podman images", limit: limit.Podman(), dir: p.root, prog: "podman",
		args: []string{"images", "--format", "{{.Repository}}:{{.Tag}}", "--filter", "reference=" + p.image}}, &out, io.Discard); err != nil {
		return
	}
	for _, ref := range strings.Fields(out.String()) {
		repo, tag, ok := cutTag(ref)
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

// digest is the manifest digest of <image>:<tag>, read with the registry's own login.
func (p *plan) digest(tag string, deniedIsMissing bool) (digest string, found bool, err error) {
	return digestOf(p.image+":"+tag, "", deniedIsMissing)
}

// digestOf is the manifest digest of ref, read with authfile ("" for skopeo's own login). A registry that says the tag
// or the name is not there ("manifest unknown", "name unknown") gives found false. ghcr says "denied" for a package
// that does not exist yet, so a push, which makes the package, reads it as missing too (deniedIsMissing). Any other
// failure of the ask is an error, never read as a missing tag.
func digestOf(ref, authfile string, deniedIsMissing bool) (digest string, found bool, err error) {
	var out, errb bytes.Buffer
	args := append([]string{"inspect", "--format", "{{.Digest}}"}, registryFlags(ref, authfile)...)
	err = run(context.Background(), call{name: "skopeo inspect", limit: limit.SkopeoInspect(), prog: "skopeo",
		args: append(args, "docker://"+ref)}, &out, &errb)
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

// stored says this machine's store holds the tag. Exit 1 of `image exists` is its answer "no".
func (p *plan) stored(tag string) (bool, error) {
	err := p.podman("podman image exists", limit.Podman(), io.Discard, "image", "exists", tag)
	if process.ExitCode(err) == 1 {
		return false, nil
	}
	return err == nil, err
}

// buildTo builds to <tag>-candidate and smokes that, both in placement, then moves it to <tag>. The move is the last step, so
// no <tag> reaches this machine's store unless its smoke passed. The candidate is dropped either way.
//
// The smoke is [tools] setup, run in the candidate with no network on a copy of the commit's tree: it proves the image
// holds what the lockfiles name, so a workspace's setup needs no download. A repo with no setup has no smoke.
func (p *plan) buildTo(tag string, placement machine.Placement) error {
	candidate := tag + "-candidate"
	defer p.drop(candidate)
	build := []string{"build"}
	if placement != "" {
		build = append(build, "--cgroup-parent="+string(placement))
	}
	build = append(build, "-f", p.containerfile, "-t", candidate, ".")
	if err := p.podmanIn(placement, "podman build", limit.Build(), p.stderr, build...); err != nil {
		return fault.Own(err)
	}
	if p.setup != "" {
		tree, err := git(p.root, "archive", p.rev)
		if err != nil {
			return err
		}
		smoke := "mkdir -p /tmp/smoke && tar -x -C /tmp/smoke && cd /tmp/smoke && " + p.setup
		args := []string{"run", "-i", "--rm", "--network", "none"}
		if placement != "" {
			args = append(args, "--cgroup-parent="+string(placement))
		}
		args = append(args, candidate, "sh", "-c", smoke)
		if err := run(p.ctx, call{name: "the smoke ([tools] setup with no network)", limit: limit.Smoke(), dir: p.root, prog: "podman",
			args: args, stdin: strings.NewReader(tree), in: placement}, p.stderr, p.stderr); err != nil {
			return fault.Own(err)
		}
	}
	return p.podman("podman tag", limit.Podman(), p.stderr, "tag", candidate, tag)
}

// drop takes a tag out of this machine's store, whether or not it is there.
func (p *plan) drop(tag string) {
	_ = p.podman("podman image rm", limit.Podman(), io.Discard, "image", "rm", tag)
}

// podman runs the engine from the PATH in the checkout, with all its output in out.
func (p *plan) podman(name string, d time.Duration, out io.Writer, args ...string) error {
	return p.podmanIn("", name, d, out, args...)
}

// podmanIn is podman in placement's slice, for a call that writes the store: what it pulls or builds is the slice's.
func (p *plan) podmanIn(placement machine.Placement, name string, d time.Duration, out io.Writer, args ...string) error {
	return run(p.ctx, call{name: name, limit: d, dir: p.root, prog: "podman", args: args, in: placement}, out, out)
}
