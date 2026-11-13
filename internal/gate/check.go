package gate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/matta-kelly/crate/internal/declaration"
	"github.com/matta-kelly/crate/internal/devimage"
	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/member"
	"github.com/matta-kelly/crate/internal/record"
	"github.com/matta-kelly/crate/internal/system"
)

// ErrRefused is a check that did not pass: its reason is already in the output.
var ErrRefused = errors.New("refused")

// Check lets main move from old to new only when the repo's check passes on new, the tree main becomes. It holds
// the repo's lock for the whole check, so pushes are checked one at a time. The check is a run on the repo's record.
func (r *Repo) Check(old, new string, out io.Writer) error {
	rec := r.begin("check", new, out)
	refuse := func(format string, a ...any) error {
		note := fmt.Sprintf(format, a...)
		fmt.Fprintf(out, "crate: %s\n", note)
		rec.End(record.Refused, note)
		return ErrRefused
	}
	step := rec.Step(record.Lock)
	unlock, err := r.lock("lock")
	step.End(err, "")
	if err != nil {
		rec.End(record.Fail, "")
		return err
	}
	defer unlock()
	dir := r.state("work")
	now := r.main()
	if (Zero(old) && now != "") || (!Zero(old) && now != old) {
		return refuse("main moved while this push waited. Pull with rebase, and push again")
	}
	if !Zero(old) {
		if _, err := r.git("merge-base", "--is-ancestor", old, new); err != nil {
			return refuse("not a fast-forward of main. Pull with rebase, and push again")
		}
	}
	if err := r.checkout(dir); err != nil {
		rec.End(record.Fail, err.Error())
		return err
	}
	if _, err := work(dir, "checkout", "-q", "-f", "--detach", new); err != nil {
		return refuse("could not check out %s, so its tree is not checked. Push again", short(new))
	}
	_, _ = work(dir, "clean", "-q", "-fdx", "-e", "node_modules")
	decl, err := readDeclaration(dir)
	if err != nil {
		return refuse("%v", err)
	}
	var env []string
	if !Zero(old) {
		env = append(env, "CHECK_BASE="+old)
	}
	err = r.run(rec, "check", dir, decl, seconds(decl.Timeout, 900), env, nil, out)
	if err != nil && !Zero(old) {
		// A refused push takes its objects with it: point the checkout back at a commit that stays.
		_, _ = work(dir, "checkout", "-q", "-f", "--detach", old)
	}
	if err != nil {
		rec.End(record.Fail, "")
		return ErrRefused
	}
	rec.End(record.Pass, "")
	return nil
}

// run runs the repo's scripts/check.sh with args in a system of its kind, made from the checkout dir as a person's
// workspace is: its network, its services, its tools member on the repo's image, and its app. The tools member's
// volumes are kept between runs, so they stay warm. Up and the check together stop at limit. Each step goes on rec.
func (r *Repo) run(rec *record.Run, kind, dir string, decl declaration.Declaration, limit time.Duration, env, args []string, out io.Writer) error {
	step := rec.Step(record.Image)
	image, source, err := r.image(dir, decl, out)
	step.End(err, string(source))
	if err != nil {
		fmt.Fprintf(out, "crate: %v\n", err)
		return ErrRefused
	}
	head, _ := work(dir, "rev-parse", "--short=12", "HEAD")
	if kind == "check" {
		fmt.Fprintf(out, "crate: checking %s in %s\n", head, image)
	} else {
		fmt.Fprintf(out, "crate: %s of %s in %s\n", kind, head, image)
	}
	name := r.systemName(kind)
	folders, err := decl.Tools.Folders()
	if err != nil {
		fmt.Fprintf(out, "crate: %v\n", err)
		return ErrRefused
	}
	if len(folders) == 0 {
		folders = []string{"node_modules"}
	}
	var keep []string
	for _, folder := range folders {
		keep = append(keep, member.VolumeName(name, folder))
	}
	systemKind := system.Check
	if kind == "postsubmit" {
		systemKind = system.Postsubmit
	}
	s := system.New(machine.Default(), system.Spec{
		Kind:     systemKind,
		Name:     name,
		Checkout: dir,
		Image:    image,
		Mounts:   []system.Mount{{Path: r.Bare}},
		Env:      append([]string{"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + r.quarantine}, env...),
		Keep:     keep,
		Out:      out,
		Record:   rec,
	})
	if err := s.Hold(); err != nil {
		fmt.Fprintf(out, "crate: %v\n", err)
		return ErrRefused
	}
	if err := s.Image(); err != nil {
		s.Remove(keep)
		fmt.Fprintf(out, "crate: %v\n", err)
		return ErrRefused
	}
	defer func() {
		if err := s.Remove(keep); err != nil {
			fmt.Fprintf(out, "crate: %v\n", err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	past := func() error {
		fmt.Fprintf(out, "crate: the %s ran past its limit of %ds, and was stopped\n", kind, int(limit.Seconds()))
		return ErrRefused
	}
	if err := s.Up(ctx); err != nil {
		if ctx.Err() != nil {
			return past()
		}
		fmt.Fprintf(out, "crate: %v\n", err)
		return ErrRefused
	}
	step = rec.Step(record.Check)
	code, err := s.Run(ctx, append([]string{"scripts/check.sh"}, args...), nil, out)
	switch {
	case ctx.Err() != nil:
		step.End(ctx.Err(), "past the limit")
		return past()
	case err != nil:
		step.End(err, "")
		fmt.Fprintf(out, "crate: %v\n", err)
		return ErrRefused
	case code != 0:
		step.End(fmt.Errorf("exit %d", code), "")
		return ErrRefused
	}
	step.End(nil, "")
	return nil
}

// systemName is the name of the repo's system of kind: crate-<repo>-check or crate-<repo>-postsubmit.
func (r *Repo) systemName(kind string) string { return "crate-" + r.Name() + "-" + kind }

// image is the image the check runs in: "dev" is the repo's own dev image, put on this machine by crate dev-image
// local (its copy when the hash is unchanged, so warm); anything else is an image ref.
func (r *Repo) image(dir string, decl declaration.Declaration, out io.Writer) (string, devimage.Source, error) {
	switch decl.Image {
	case "":
		return "", "", errors.New("crate.toml names no image")
	case "dev":
		return devimage.Local(devimage.Options{Root: dir, Stderr: out}, "")
	default:
		return decl.Image, "", nil
	}
}

// readDeclaration reads crate.toml at the checkout dir; a checkout with none has an empty one.
func readDeclaration(dir string) (declaration.Declaration, error) {
	data, err := os.ReadFile(filepath.Join(dir, "crate.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return declaration.Declaration{}, nil
	}
	if err != nil {
		return declaration.Declaration{}, err
	}
	return declaration.Parse(data)
}

// declarationAt reads crate.toml at commit in the bare repo; none gives an empty one.
func (r *Repo) declarationAt(commit string) declaration.Declaration {
	text, err := r.git("show", commit+":crate.toml")
	if err != nil {
		return declaration.Declaration{}
	}
	d, _ := declaration.Parse([]byte(text))
	return d
}

// seconds is a crate.toml count of seconds, or the default when it names none.
func seconds(value string, def int) time.Duration {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		n = def
	}
	return time.Duration(n) * time.Second
}
