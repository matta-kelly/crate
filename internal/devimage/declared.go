package devimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/matta-kelly/crate/internal/fault"
	"github.com/matta-kelly/crate/internal/limit"
	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/process"
)

// A declared image ([[images]] in crate.toml) is built as a dev image is, by a rootless podman build of the commit's own
// files, and named by what made it. Its context is the commit's tree as git holds it, never a checkout: each file's
// mode is the build's (0644, or 0755 for an executable), whatever the machine's umask, and the build makes no
// /etc/hostname of its own, so a RUN step that writes one keeps it. So the same commit builds the same image on any
// machine.

// InputsHash is the hash of the inputs at rev: git's tree entries of them, which hold each file's blob hash and path,
// so one commit gives one hash on every machine. 16 lowercase hex. A missing input is the change's fault.
func InputsHash(root, rev string, inputs []string) (string, error) {
	for _, in := range inputs {
		entry, err := git(root, "ls-tree", rev, "--", in)
		if err != nil {
			return "", err
		}
		if entry == "" {
			return "", fault.Change(fmt.Errorf("inputs: no %s at %s", in, rev))
		}
	}
	entries, err := git(root, append([]string{"ls-tree", "-r", rev, "--"}, inputs...)...)
	if err != nil {
		return "", err
	}
	return hashOf(entries), nil
}

// Changed says the commits from base to rev touch the inputs.
func Changed(root, base, rev string, inputs []string) (bool, error) {
	out, err := git(root, append([]string{"diff", "--name-only", base, rev, "--"}, inputs...)...)
	return strings.TrimSpace(out) != "", err
}

// BuildNumber is the tag of the image the inputs make at rev: the build number of the last first-parent commit of rev
// that changed them, the count of first-parent commits up to it. A later change to them has a larger number, so an
// update bot orders the tags as versions, and a change elsewhere in the repo leaves the tag as it is.
func BuildNumber(root, rev string, inputs []string) (string, error) {
	last, err := git(root, append([]string{"log", "-1", "--first-parent", "--format=%H", rev, "--"}, inputs...)...)
	if err != nil {
		return "", err
	}
	last = strings.TrimSpace(last)
	if last == "" {
		return "", fmt.Errorf("no commit of %s changed %s", rev, strings.Join(inputs, " "))
	}
	count, err := git(root, "rev-list", "--count", "--first-parent", last)
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(strings.TrimSpace(count))
	if err != nil {
		return "", fmt.Errorf("the build number of %s: %w", last, err)
	}
	return strconv.Itoa(n), nil
}

// BuildAt builds rev's tree from containerfile, a path in it, as tag in this machine's store, in placement, and the end
// of ctx stops it. A build that fails is the change's when the build itself said no (fault.Own).
func BuildAt(ctx context.Context, root, rev, containerfile, tag string, placement machine.Placement, out io.Writer) error {
	dir, err := os.MkdirTemp("", "crate-image-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	var tree bytes.Buffer
	// git's tar.umask of 0022: the modes are git's own, 0644 and 0755, never the checkout's
	if err := run(ctx, call{name: "git archive", limit: limit.Git(), dir: root, prog: "git",
		args: []string{"-c", "tar.umask=0022", "archive", rev}}, &tree, out); err != nil {
		return err
	}
	if err := run(ctx, call{name: "tar", limit: limit.Git(), dir: dir, prog: "tar", args: []string{"-x", "-C", dir},
		stdin: &tree}, out, out); err != nil {
		return err
	}
	args := []string{"build", "--no-hostname"}
	if placement != "" {
		args = append(args, "--cgroup-parent="+string(placement))
	}
	args = append(args, "-f", containerfile, "-t", tag, dir)
	if err := run(ctx, call{name: "podman build", limit: limit.Build(), dir: dir, prog: "podman", args: args, in: placement},
		out, out); err != nil {
		return fault.Own(err)
	}
	return nil
}

// InStore says this machine's store holds the tag. Exit 1 of `image exists` is its answer "no".
func InStore(tag string) (bool, error) {
	err := run(context.Background(), call{name: "podman image exists", limit: limit.Podman(), prog: "podman",
		args: []string{"image", "exists", tag}}, io.Discard, io.Discard)
	if process.ExitCode(err) == 1 {
		return false, nil
	}
	return err == nil, err
}

// Copy sends this machine's local image to the registry as dest, logged in by authfile.
func Copy(local, dest, authfile string, stderr io.Writer) error {
	return push(local, dest, authfile, stderr)
}

// hashOf is the 16 hex of the sha256 of s.
func hashOf(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s)))[:16] }
