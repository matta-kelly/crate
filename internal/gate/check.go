package gate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/matta-kelly/crate/internal/declaration"
	"github.com/matta-kelly/crate/internal/devimage"
	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/member"
	"github.com/matta-kelly/crate/internal/record"
	"github.com/matta-kelly/crate/internal/system"
)

// ErrRefused is a run that did not pass: its reason is already in its output.
var ErrRefused = errors.New("refused")

// slotSystem is the system of the queue's run slot: crate-<repo>-check-<slot>.
func (r *Repo) slotSystem(slot int) string {
	return "crate-" + r.Name() + "-check-" + strconv.Itoa(slot)
}

// postsubmitSystem is postsubmit's system: crate-<repo>-postsubmit.
func (r *Repo) postsubmitSystem() string { return "crate-" + r.Name() + "-postsubmit" }

// checkRun is the queue's check of a run: the repo's check on the run's tree, in its slot's checkout, with
// CHECK_BASE the run's base, so a check plans its sets from what the run's changes touch. Its output goes to each
// change's log, and it is a run on the record.
func (r *Repo) checkRun(ctx context.Context, rn *run) error {
	out, closeLogs := r.entryLogs(rn.shas())
	defer closeLogs()
	fmt.Fprintf(out, "crate: run %d checks %s: %s on main %s\n", rn.slot, short(rn.tip),
		strings.Join(shorts(rn.shas()), " "), short(rn.base))
	dir := r.state("runs", strconv.Itoa(rn.slot))
	decl, err := readDeclaration(dir)
	if err != nil {
		fmt.Fprintf(out, "crate: %v\n", err)
		return ErrRefused
	}
	rec := r.begin("check", r.slotSystem(rn.slot), rn.tip, out)
	err = r.run(ctx, rec, system.Check, r.slotSystem(rn.slot), dir, decl, seconds(decl.Timeout, 900),
		[]string{"CHECK_BASE=" + rn.base}, nil, out)
	note := strings.Join(shorts(rn.shas()), " ")
	switch {
	case ctx.Err() != nil:
		rec.End(record.Fail, "stopped: "+note)
	case err != nil:
		rec.End(record.Fail, note)
	default:
		rec.End(record.Pass, note)
	}
	return err
}

// run runs the repo's scripts/check.sh with args in a system of kind and name, made from the checkout dir as a
// person's workspace is: its network, its services, its tools member on the repo's image, and its app. The tools
// member's volumes are kept between runs, so they stay warm. Up and the check together stop at limit, or when ctx
// ends. Each step goes on rec.
func (r *Repo) run(ctx context.Context, rec *record.Run, kind, name, dir string, decl declaration.Declaration, limit time.Duration, env, args []string, out io.Writer) error {
	step := rec.Step(record.Image)
	image, source, err := r.image(dir, decl, out)
	step.End(err, string(source))
	if err != nil {
		fmt.Fprintf(out, "crate: %v\n", err)
		return ErrRefused
	}
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
	s := system.New(machine.Default(), system.Spec{
		Kind:     kind,
		Name:     name,
		Checkout: dir,
		Image:    image,
		Mounts:   []system.Mount{{Path: r.Bare}},
		Env:      env,
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
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	stopped := func() error {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			fmt.Fprintf(out, "crate: the %s ran past its limit of %ds, and was stopped\n", kind, int(limit.Seconds()))
		}
		return ErrRefused
	}
	if err := s.Up(ctx); err != nil {
		if ctx.Err() != nil {
			return stopped()
		}
		fmt.Fprintf(out, "crate: %v\n", err)
		return ErrRefused
	}
	step = rec.Step(record.Check)
	code, err := s.Run(ctx, append([]string{"scripts/check.sh"}, args...), nil, out)
	switch {
	case ctx.Err() != nil:
		step.End(ctx.Err(), "stopped")
		return stopped()
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
