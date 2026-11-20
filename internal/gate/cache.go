package gate

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/matta-kelly/crate/internal/devimage"
	"github.com/matta-kelly/crate/internal/record"
	"github.com/matta-kelly/crate/internal/ship"
)

// The dev image cache: the server's queue is the one writer of the registry's dev images (devimage/cache.go). Each
// dev image a check or postsubmit makes on this machine is pushed by its hash at once, from a process of its own
// (crate cache <bare>), so an upload never holds a check; and each green main points <image>:main at its own. A push
// or a move that fails is tried again by the next start (the next check, the next green main, the worker's start,
// crate apply). A hash that never lands is pushed too: the registry is addressed by hash, so it costs a few layers and
// misleads no one.
// Its state is in <bare>/crate/cache: pushed (each hash pushed, one a line), main (the hash :main is), run.log (the
// runs' output); cache.lock holds one run at a time.

// devImage is the dev image's name and hash at commit, for a repo whose check runs in its own dev image; ok false for
// any other, or when commit is "".
func (r *Repo) devImage(commit string) (image, hash string, ok bool) {
	if commit == "" || r.declarationAt(commit).Image != "dev" {
		return "", "", false
	}
	image, hash, err := devimage.Of(devimage.Options{Root: r.Bare, Rev: commit})
	return image, hash, err == nil
}

// cacheState reads a file of the cache's state as its lines.
func (r *Repo) cacheState(name string) []string {
	data, _ := os.ReadFile(r.state("cache", name))
	return strings.Fields(string(data))
}

// cacheWork is what a run has to do: the hashes this machine holds that are not pushed yet, and the green main's hash
// when :main is not at it yet and this machine holds its image ("" for none).
func (r *Repo) cacheWork() (image string, push []string, point string, err error) {
	image, mainHash, ok := r.devImage(r.greenMain())
	if !ok {
		return "", nil, "", nil
	}
	held, err := devimage.Held(image)
	if err != nil {
		return image, nil, "", err
	}
	pushed := r.cacheState("pushed")
	for _, hash := range held {
		if !slices.Contains(pushed, hash) {
			push = append(push, hash)
		}
	}
	if current := r.cacheState("main"); (len(current) == 0 || current[0] != mainHash) && slices.Contains(held, mainHash) {
		point = mainHash
	}
	return image, push, point, nil
}

// StartCache starts crate cache <bare> when there is work and no run holds the lock. Its output goes to cache/run.log.
func (r *Repo) StartCache() error {
	_, push, point, err := r.cacheWork()
	if err != nil || (len(push) == 0 && point == "") {
		return err
	}
	if err := os.MkdirAll(r.state("cache"), 0o755); err != nil {
		return err
	}
	unlock, free, err := r.tryLock("cache/cache.lock")
	if err != nil || !free {
		return err
	}
	unlock()
	return r.detach("cache/run.log", "cache", r.Bare)
}

// Cache pushes each dev image this machine holds that is not pushed yet, then the green main's as :main, until
// nothing is left. A failure ends the run, recorded; the next start tries again.
func (r *Repo) Cache(out io.Writer) error {
	if err := os.MkdirAll(r.state("cache"), 0o755); err != nil {
		return err
	}
	unlock, err := r.lock("cache/cache.lock")
	if err != nil {
		return err
	}
	defer unlock()
	for {
		image, push, point, err := r.cacheWork()
		if err != nil {
			fmt.Fprintf(out, "crate: the dev images on this machine: %v\n", err)
			return ErrRefused
		}
		if len(push) == 0 && point == "" {
			return nil
		}
		if err := r.cacheOnce(image, push, point, out); err != nil {
			return err
		}
	}
}

// cacheOnce is one run of the cache: each push, then the move of :main, each a step of the record.
func (r *Repo) cacheOnce(image string, push []string, point string, out io.Writer) error {
	rec := r.begin("cache", "", r.greenMain(), nil)
	authfile := ship.AuthFile()
	fail := func(step *record.Step, err error) error {
		step.End(err, "")
		rec.End(record.Fail, err.Error())
		fmt.Fprintf(out, "crate: %v; the next start tries again\n", err)
		return ErrRefused
	}
	for _, hash := range push {
		step := rec.Step(record.Push)
		if err := ship.Login(authfile); err != nil {
			return fail(step, err)
		}
		if err := devimage.Push(image, hash, authfile, out); err != nil {
			return fail(step, fmt.Errorf("the dev image %s:%s: %w", image, hash, err))
		}
		if err := r.appendState("cache", "pushed", hash); err != nil {
			return fail(step, err)
		}
		step.End(nil, image+":"+hash)
		fmt.Fprintf(out, "crate: pushed %s:%s\n", image, hash)
	}
	if point != "" {
		step := rec.Step(record.Point)
		if err := ship.Login(authfile); err != nil {
			return fail(step, err)
		}
		if err := devimage.Point(image, point, authfile, out); err != nil {
			return fail(step, fmt.Errorf("%s:main: %w", image, err))
		}
		if err := os.WriteFile(r.state("cache", "main"), []byte(point+"\n"), 0o644); err != nil {
			return fail(step, err)
		}
		step.End(nil, image+":main is "+point)
		fmt.Fprintf(out, "crate: %s:main is %s\n", image, point)
	}
	rec.End(record.Pass, "")
	return nil
}

// appendState adds line to the state file name in folder.
func (r *Repo) appendState(folder, name, line string) error {
	f, err := os.OpenFile(r.state(folder, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(f, line); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
