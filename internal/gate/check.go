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
	"github.com/matta-kelly/crate/internal/fault"
	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/member"
	"github.com/matta-kelly/crate/internal/record"
	"github.com/matta-kelly/crate/internal/ship"
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
		return fault.Change(err)
	}
	rec := r.begin("check", r.slotSystem(rn.slot), rn.tip, out)
	err = r.run(ctx, rec, system.Check, r.slotSystem(rn.slot), dir, decl, seconds(decl.Timeout, 900),
		[]string{"CHECK_BASE=" + rn.base}, nil, out)
	if err == nil {
		err = r.buildImages(ctx, dir, rn.base, decl, rec, out)
	}
	note := strings.Join(shorts(rn.shas()), " ")
	switch {
	case ctx.Err() != nil:
		rec.End(record.Fail, "stopped: "+note)
	case err == nil:
		rec.End(record.Pass, note)
	case fault.IsChange(err):
		rec.End(record.Fail, note)
	default:
		rec.End(record.Error, note)
	}
	return err
}

// run runs the repo's scripts/check.sh with args in a system of kind and name, made from the checkout dir as a
// person's workspace is: its network, its services, its tools member on the repo's image, and its app. The tools
// member's volumes are kept between runs: they hold its setup, which runs once per key (internal/system/warm.go). Up
// and the check together stop at limit, or when ctx ends. Each step goes on rec. Its error says whose fault it is
// (internal/fault): the change's own (its declaration, its build, its setup, its app, its check, its limit), or else
// crate's machine's.
func (r *Repo) run(ctx context.Context, rec *record.Run, kind, name, dir string, decl declaration.Declaration, limit time.Duration, env, args []string, out io.Writer) error {
	refuse := func(err error) error {
		fmt.Fprintf(out, "crate: %v\n", err)
		return err
	}
	image, err := imageOf(decl)
	if err != nil || image != "" {
		// A ref is the image as it is; the repo's own dev image is the system's step, in its slice (System.Image).
		rec.Step(record.Image).End(err, "")
	}
	if err != nil {
		return refuse(fault.Change(err))
	}
	folders, err := decl.Tools.Folders()
	if err != nil {
		return refuse(fault.Change(err))
	}
	if len(folders) == 0 {
		folders = []string{"node_modules"}
	}
	var keep []string
	for _, folder := range folders {
		keep = append(keep, member.VolumeName(name, folder))
	}
	source := ""
	if decl.ShipsImage() {
		if url, err := r.git("remote", "get-url", "github"); err == nil {
			source = ship.Source(url)
		}
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
		Source:   source,
	})
	if err := s.Hold(); err != nil {
		return refuse(err)
	}
	if err := s.Image(); err != nil {
		s.Remove(keep)
		return refuse(err)
	}
	// The dev image this check runs in goes to the registry by its hash, in the background (cache.go).
	if decl.Image == "dev" {
		if err := r.StartCache(); err != nil {
			fmt.Fprintf(out, "crate: the dev image's push did not start: %v\n", err)
		}
	}
	defer func() {
		if err := s.Remove(keep); err != nil {
			fmt.Fprintf(out, "crate: %v\n", err)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	// The limit is the change's own (crate.toml's timeout); a stop is the caller's, and no one's fault.
	stopped := func() error {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return refuse(fault.Change(fmt.Errorf("the %s ran past its limit of %ds, and was stopped", kind, int(limit.Seconds()))))
		}
		return ctx.Err()
	}
	if err := s.Up(ctx); err != nil {
		if ctx.Err() != nil {
			return stopped()
		}
		return refuse(err)
	}
	if info, err := os.Stat(filepath.Join(dir, "scripts", "check.sh")); err != nil || info.Mode().Perm()&0o111 == 0 {
		return refuse(fault.Change(errors.New("scripts/check.sh is missing, or not executable")))
	}
	step := rec.Step(record.Check)
	code, err := s.Run(ctx, append([]string{"scripts/check.sh"}, args...), nil, nil, out, out)
	switch {
	case ctx.Err() != nil:
		step.End(ctx.Err(), "stopped")
		return stopped()
	case err != nil:
		step.End(err, "")
		return refuse(err)
	case code >= 125 && code <= 127:
		// podman exec keeps 125 to 127 for its own failures: check.sh is there and executable (above)
		err := fmt.Errorf("podman exec exited %d: podman could not run scripts/check.sh", code)
		step.End(err, "")
		return refuse(err)
	case code != 0:
		step.End(fmt.Errorf("exit %d", code), "")
		return fault.Change(fmt.Errorf("scripts/check.sh exited %d", code))
	}
	step.End(nil, "")
	// A passed check of a repo that ships its image keeps the image it ran, by the commit it checked, until that main
	// is green and published (ship.go).
	if kind == system.Check && decl.ShipsImage() {
		commit, err := work(dir, "rev-parse", "HEAD")
		if err == nil {
			err = s.KeepApp(ctx, r.kept(commit))
		}
		if err != nil {
			return refuse(fmt.Errorf("the check passed, but its image is not kept to ship: %w", err))
		}
	}
	return nil
}

// imageOf is the image the check runs in: an image ref as it is, or "" for "dev", the repo's own dev image, which the
// system puts on this machine in its slice once it holds its slot (its copy when the hash is unchanged, so warm).
func imageOf(decl declaration.Declaration) (string, error) {
	switch decl.Image {
	case "":
		return "", errors.New("crate.toml names no image")
	case "dev":
		return "", nil
	default:
		return decl.Image, nil
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
