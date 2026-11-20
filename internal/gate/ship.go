package gate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/matta-kelly/crate/internal/declaration"
	"github.com/matta-kelly/crate/internal/limit"
	"github.com/matta-kelly/crate/internal/process"
	"github.com/matta-kelly/crate/internal/record"
	"github.com/matta-kelly/crate/internal/ship"
)

// Ship: a repo whose crate.toml says ship = "image" publishes each green main's app, the image its check built and
// ran, so a node runs what crate tested. A check that passes keeps its app's image by its commit (keep); a green
// main publishes it, beside the copy to GitHub, from a process of its own (crate ship <bare>), so an upload never
// holds the queue. The rule is a node's: the green main is published. A publish that fails is tried again by the next
// start (the next green main, the worker's start, crate apply), with the same kept image, and a newer green main
// takes the place of an older one not yet published.
// Its state is in <bare>/crate/ship: published (the last commit published), run.log (the runs' output); ship.lock
// holds one run at a time.

// kept is the local image a check keeps for commit.
func (r *Repo) kept(commit string) string { return r.keptRepository() + ":" + commit }

func (r *Repo) keptRepository() string { return "localhost/crate-" + r.Name() + "-ship" }

// greenMain is the main to publish: main, for a repo with no postsubmit, else the last main postsubmit passed. ""
// for none.
func (r *Repo) greenMain() string {
	main := r.main()
	if main == "" || r.PostsubmitOf(main) == "" {
		return main
	}
	data, _ := os.ReadFile(r.state("postsubmit", "green"))
	return strings.TrimSpace(string(data))
}

// published is the last commit a publish pushed; "" for none.
func (r *Repo) published() string {
	data, _ := os.ReadFile(r.state("ship", "published"))
	return strings.TrimSpace(string(data))
}

// StartShip starts crate ship <bare> when the green main ships its image, is not published, has a kept image, and no
// publish runs. Its output goes to ship/run.log. A green main with no kept image (one that landed before its repo
// shipped its image) has nothing to publish: the next main that lands has.
func (r *Repo) StartShip() error {
	target := r.greenMain()
	if target == "" || target == r.published() || !r.declarationAt(target).ShipsImage() {
		return nil
	}
	if ok, err := podmanExists(r.kept(target)); err != nil || !ok {
		return err
	}
	if err := os.MkdirAll(r.state("ship"), 0o755); err != nil {
		return err
	}
	unlock, free := r.tryLock("ship/ship.lock")
	if !free {
		return nil
	}
	unlock()
	return r.detach("ship/run.log", "ship", r.Bare)
}

// Ship publishes the green main until it is published, reading it again after each publish, so a main that went green
// meanwhile is published too. A failed publish ends the run unless a newer main went green meanwhile; the next start
// tries it again. A main that went green as the run ended found the lock held and started nothing, so the run starts
// once more after it, when the green main is not the one that failed.
func (r *Repo) Ship(out io.Writer) error {
	failed, err := r.shipAll(out)
	if g := r.greenMain(); err == nil || g != failed {
		if serr := r.StartShip(); serr != nil {
			fmt.Fprintf(out, "crate: the next publish did not start: %v\n", serr)
		}
	}
	return err
}

// shipAll is Ship's run under the lock. It gives the green main whose publish failed, with the error.
func (r *Repo) shipAll(out io.Writer) (string, error) {
	if err := os.MkdirAll(r.state("ship"), 0o755); err != nil {
		return "", err
	}
	unlock, err := r.lock("ship/ship.lock")
	if err != nil {
		return "", err
	}
	defer unlock()
	for {
		target := r.greenMain()
		if target == "" || target == r.published() {
			return "", nil
		}
		decl := r.declarationAt(target)
		if !decl.ShipsImage() {
			return "", nil
		}
		err := r.publishImage(target, decl, out)
		r.dropKept(target, out)
		if err != nil && r.greenMain() == target {
			return target, ErrRefused
		}
	}
}

// publishImage pushes commit's kept image as <publish>:<build>, then its run files, stamped with that tag, as
// <publish>-quadlets:<build>, and prints and records the pin line a node's releases file takes. The build is the count
// of main's first-parent commits up to commit (ship.Tag).
func (r *Repo) publishImage(commit string, decl declaration.Declaration, out io.Writer) error {
	rec := r.begin("release", "", commit, nil)
	authfile := ship.AuthFile()
	fail := func(step *record.Step, err error) error {
		step.End(err, "")
		rec.End(record.Fail, err.Error())
		fmt.Fprintf(out, "crate: %s is not published: %v; the next start tries again\n", short(commit), err)
		return err
	}

	step := rec.Step(record.Push)
	count, err := r.git("rev-list", "--count", "--first-parent", commit)
	if err != nil {
		return fail(step, err)
	}
	build, err := strconv.Atoi(count)
	if err != nil {
		return fail(step, fmt.Errorf("the build number of %s: %w", short(commit), err))
	}
	tag := ship.Tag(build)
	if err := ship.Login(authfile); err != nil {
		return fail(step, err)
	}
	kept := r.kept(commit)
	if ok, err := podmanExists(kept); err != nil {
		return fail(step, err)
	} else if !ok {
		return fail(step, fmt.Errorf("no image of %s is kept; the next main that lands is kept", short(commit)))
	}
	image := decl.App.Publish + ":" + tag
	args := []string{"push", "--authfile", authfile, "--quiet"}
	if ship.Plain(decl.App.Publish) {
		args = append(args, "--tls-verify=false")
	}
	if _, err := podman(limit.Push(), append(args, kept, image)...); err != nil {
		return fail(step, fmt.Errorf("the image's push to %s: %w", image, err))
	}
	step.End(nil, image)

	step = rec.Step(record.RunFiles)
	files, err := r.runFiles(commit, decl.App.Quadlets)
	if err != nil {
		return fail(step, err)
	}
	if files, err = ship.Stamp(files, tag); err != nil {
		return fail(step, err)
	}
	created, err := r.git("log", "-1", "--format=%cI", commit)
	if err != nil {
		return fail(step, err)
	}
	when, err := time.Parse(time.RFC3339, created)
	if err != nil {
		return fail(step, err)
	}
	source := ""
	if url, err := r.git("remote", "get-url", "github"); err == nil {
		source = ship.Source(url)
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit.Push())
	defer cancel()
	pushed, err := ship.PushArtifact(ctx, files, decl.App.Publish, source, commit, when, tag, authfile)
	if err != nil {
		return fail(step, fmt.Errorf("the run files' push to %s:%s: %w", ship.Artifact(decl.App.Publish), tag, err))
	}
	step.End(nil, ship.Artifact(decl.App.Publish)+"@"+pushed.Digest)

	name := decl.Name
	if name == "" {
		name = r.Name()
	}
	pin := ship.Pin(name, tag, ship.Artifact(decl.App.Publish), pushed.Digest)
	if err := os.WriteFile(r.state("ship", "published"), []byte(commit+"\n"), 0o644); err != nil {
		return fail(step, err)
	}
	rec.End(record.Pass, pin)
	fmt.Fprintf(out, "crate: published %s as %s; the pin line:\n%s\n", short(commit), image, pin)
	return nil
}

// runFiles is the files of folder at commit, read from the bare repo. The folder holds only files.
func (r *Repo) runFiles(commit, folder string) ([]ship.File, error) {
	folder = path.Clean(folder)
	list, err := r.git("ls-tree", commit+":"+folder)
	if err != nil {
		return nil, fmt.Errorf("the run files: [app] quadlets %s is not a folder at %s", folder, short(commit))
	}
	var files []ship.File
	for _, line := range lines(list) {
		meta, name, _ := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if len(fields) != 3 || fields[1] != "blob" {
			return nil, fmt.Errorf("the run files: %s/%s is not a file; the folder holds only files", folder, name)
		}
		var blob bytes.Buffer
		err := process.Run(context.Background(), process.Call{Prog: "git", Args: []string{"-C", r.Bare, "cat-file", "blob", fields[2]},
			Env: r.bareEnv, Limit: limit.Git(), Stdout: &blob})
		data := blob.Bytes()
		if err != nil {
			return nil, fmt.Errorf("the run files: %s/%s: %w", folder, name, err)
		}
		files = append(files, ship.File{Name: name, Data: data})
	}
	return files, nil
}

// dropKept removes each kept image a publish no longer needs: all but target's, and those of the mains after it,
// which wait for their own turn (a newer main in postsubmit, a run that passed behind the head). So what was
// published goes, and so does what will never be: an older main a newer one superseded, a run that never landed.
func (r *Repo) dropKept(target string, out io.Writer) {
	tags, err := podman(limit.Podman(), "images", "--filter", "reference="+r.keptRepository(), "--format", "{{.Tag}}")
	if err != nil {
		fmt.Fprintf(out, "crate: the kept images: %v\n", err)
		return
	}
	published := r.published()
	for _, commit := range strings.Fields(tags) {
		if commit == target && published != target {
			continue // not yet published: the next start pushes it
		}
		if commit != target {
			if _, err := r.git("merge-base", "--is-ancestor", target, commit); err == nil {
				continue
			}
		}
		if _, err := podman(limit.Podman(), "image", "rm", r.kept(commit)); err != nil {
			fmt.Fprintf(out, "crate: the kept image %s is still there: %v\n", r.kept(commit), err)
		}
	}
}

// podman runs podman with args within d. Its stdout, trimmed; on a failure, its stderr in the error.
func podman(d time.Duration, args ...string) (string, error) {
	return process.Output(context.Background(), process.Call{Prog: "podman", Args: args, Limit: d})
}

// podmanExists says the image ref is in the store.
func podmanExists(ref string) (bool, error) {
	_, err := podman(limit.Podman(), "image", "exists", ref)
	switch {
	case err == nil:
		return true, nil
	case process.ExitCode(err) == 1:
		return false, nil
	default:
		return false, err
	}
}
