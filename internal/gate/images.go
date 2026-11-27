package gate

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/matta-kelly/crate/internal/declaration"
	"github.com/matta-kelly/crate/internal/devimage"
	"github.com/matta-kelly/crate/internal/record"
	"github.com/matta-kelly/crate/internal/ship"
)

// Images: the images a repo declares in crate.toml ([[images]]), each built from its own inputs in a rootless build of
// the commit's tree (devimage.BuildAt). A check whose changes touch an image's inputs builds it, and keeps it on this
// machine by its inputs' hash, so the image a node runs is the one the gate built; a build that fails refuses the
// change. A green main publishes each image whose tag, the build number of the last main that changed its inputs, is
// not published yet, from a process of its own (crate images <bare>): the kept build, else one from that main. The
// same inputs keep their tag, so a change elsewhere in the repo publishes nothing, and no pin bump makes a new image.
// Its state is in <bare>/crate/images: published (each <publish>:<tag> pushed, one a line), run.log (the runs'
// output); images.lock holds one run at a time.

// keptImage is this machine's build of an image from inputs of hash.
func (r *Repo) keptImage(img declaration.Image, hash string) string {
	return "localhost/crate-" + r.Name() + "-image-" + path.Base(img.Publish) + ":" + hash
}

// buildImages builds, in a check of a run on base, each image whose inputs the run's changes touch, from the run's
// tree in dir, unless this machine holds that build already. Each build is a step on the record.
func (r *Repo) buildImages(ctx context.Context, dir, base string, decl declaration.Declaration, rec *record.Run, out io.Writer) error {
	for _, img := range decl.Images {
		changed, err := devimage.Changed(dir, base, "HEAD", img.Inputs)
		if err != nil {
			return err
		}
		if !changed {
			continue
		}
		hash, err := devimage.InputsHash(dir, "HEAD", img.Inputs)
		if err != nil {
			return err
		}
		kept := r.keptImage(img, hash)
		held, err := devimage.InStore(kept)
		if err != nil {
			return err
		}
		if held {
			continue
		}
		step := rec.Step(record.Image)
		fmt.Fprintf(out, "crate: building %s from its inputs (%s)\n", img.Publish, hash)
		if err := devimage.BuildAt(ctx, dir, "HEAD", img.Containerfile, kept, "", out); err != nil {
			step.End(err, img.Publish)
			fmt.Fprintf(out, "crate: the image %s did not build: %v\n", img.Publish, err)
			return err
		}
		step.End(nil, img.Publish+" "+hash)
	}
	return nil
}

// imagesWork is what the green main has to publish: each declared image whose <publish>:<tag> is not published yet.
func (r *Repo) imagesWork() (main string, todo []declaration.Image, tags []string, err error) {
	main = r.greenMain()
	if main == "" {
		return "", nil, nil, nil
	}
	decl := r.declarationAt(main)
	data, _ := os.ReadFile(r.state("images", "published"))
	published := strings.Fields(string(data))
	for _, img := range decl.Images {
		tag, err := devimage.BuildNumber(r.Bare, main, img.Inputs)
		if err != nil {
			return main, nil, nil, err
		}
		if !slices.Contains(published, img.Publish+":"+tag) {
			todo, tags = append(todo, img), append(tags, tag)
		}
	}
	return main, todo, tags, nil
}

// StartImages starts crate images <bare> when the green main has an image to publish and no run holds the lock. Its
// output goes to images/run.log.
func (r *Repo) StartImages() error {
	_, todo, _, err := r.imagesWork()
	if err != nil || len(todo) == 0 {
		return err
	}
	if err := os.MkdirAll(r.state("images"), 0o755); err != nil {
		return err
	}
	unlock, free, err := r.tryLock("images/images.lock")
	if err != nil || !free {
		return err
	}
	unlock()
	return r.detach("images/run.log", "images", r.Bare)
}

// Images publishes the green main's images until none is left, reading the green main again after each run, so a
// main that went green meanwhile is published too. A failure ends the run, recorded; the next start tries again.
func (r *Repo) Images(out io.Writer) error {
	if err := os.MkdirAll(r.state("images"), 0o755); err != nil {
		return err
	}
	unlock, err := r.lock("images/images.lock")
	if err != nil {
		return err
	}
	defer unlock()
	for {
		main, todo, tags, err := r.imagesWork()
		if err != nil {
			fmt.Fprintf(out, "crate: the images to publish: %v\n", err)
			return ErrRefused
		}
		if len(todo) == 0 {
			return nil
		}
		for i, img := range todo {
			if err := r.publishDeclared(main, img, tags[i], out); err != nil {
				return err
			}
		}
	}
}

// publishDeclared pushes main's build of img as <publish>:<tag>, and moves its point tag to it.
func (r *Repo) publishDeclared(main string, img declaration.Image, tag string, out io.Writer) error {
	rec := r.begin("image", "", main, nil)
	ref := img.Publish + ":" + tag
	fail := func(step *record.Step, err error) error {
		step.End(err, "")
		rec.End(record.Fail, err.Error())
		fmt.Fprintf(out, "crate: %s: %v; the next start tries again\n", ref, err)
		return ErrRefused
	}
	step := rec.Step(record.Image)
	hash, err := devimage.InputsHash(r.Bare, main, img.Inputs)
	if err != nil {
		return fail(step, err)
	}
	kept := r.keptImage(img, hash)
	held, err := devimage.InStore(kept)
	if err != nil {
		return fail(step, err)
	}
	if held {
		step.End(nil, "stored")
	} else {
		// a main that landed before its image was declared, or a build this machine no longer holds
		if err := devimage.BuildAt(context.Background(), r.Bare, main, img.Containerfile, kept, "", out); err != nil {
			return fail(step, err)
		}
		step.End(nil, "built")
	}
	step = rec.Step(record.Push)
	authfile := ship.AuthFile()
	if err := ship.Login(authfile); err != nil {
		return fail(step, err)
	}
	if err := devimage.Copy(kept, ref, authfile, out); err != nil {
		return fail(step, err)
	}
	if img.Point != "" {
		if err := devimage.Copy(kept, img.Publish+":"+img.Point, authfile, out); err != nil {
			return fail(step, err)
		}
	}
	if err := r.appendState("images", "published", ref); err != nil {
		return fail(step, err)
	}
	step.End(nil, ref)
	rec.End(record.Pass, ref)
	fmt.Fprintf(out, "crate: published %s\n", ref)
	return nil
}
