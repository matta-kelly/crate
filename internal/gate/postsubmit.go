package gate

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Postsubmit: the slow sets, too slow to hold a land, run on each new main after it moves. crate.toml's postsubmit
// names them, as arguments to scripts/check.sh, and postsubmit_timeout their limit (3600 seconds when it names
// none). A repo with postsubmit is released only after it passes, so no version is made from a main it failed.

// PostsubmitOf is the sets the commit's crate.toml names for postsubmit; "" for none.
func (r *Repo) PostsubmitOf(commit string) string { return r.declarationAt(commit).Postsubmit }

// Postsubmit runs the slow sets on main as it stands, one run at a time, so a run that waited tests every land since
// the last. Green: main is the new green, and is released. Red: the commits since the last green are reverted.
func (r *Repo) Postsubmit(out io.Writer) error {
	dir := r.state("post")
	if err := os.MkdirAll(r.state("postsubmit"), 0o755); err != nil {
		return err
	}
	if err := r.checkout(dir); err != nil {
		return err
	}
	unlock, err := r.lock("postsubmit.lock")
	if err != nil {
		return err
	}
	defer unlock()
	main := r.main()
	if main == "" {
		return nil
	}
	data, _ := os.ReadFile(r.state("postsubmit", "green"))
	green := strings.TrimSpace(string(data))
	if main == green {
		return nil
	}
	// The tree a green main had passes again: a revert back to it, or a change of nothing.
	if green != "" {
		a, _ := r.git("rev-parse", main+"^{tree}")
		b, _ := r.git("rev-parse", green+"^{tree}")
		if a == b {
			r.setGreen(main)
			r.log("postsubmit %s %s pass: the tree of green %s", r.Name(), short(main), short(green))
			return nil
		}
	}
	_, _ = work(dir, "checkout", "-q", "-f", "--detach", main)
	_, _ = work(dir, "clean", "-q", "-fdx", "-e", "node_modules")
	decl, err := readDeclaration(dir)
	if err != nil {
		return err
	}
	path := r.state("postsubmit", short(main)+".log")
	logFile, err := os.Create(path)
	if err != nil {
		return err
	}
	r.log("postsubmit %s %s start", r.Name(), short(main))
	err = r.run("postsubmit", dir, decl, seconds(decl.PostsubmitTimeout, 3600), nil, strings.Fields(decl.Postsubmit), logFile)
	logFile.Close()
	if err == nil {
		r.setGreen(main)
		r.log("postsubmit %s %s pass", r.Name(), short(main))
		r.Release(main, out)
		return nil
	}
	r.log("postsubmit %s %s fail", r.Name(), short(main))
	if green == "" {
		r.log("postsubmit %s %s not reverted: no green main to go back to", r.Name(), short(main))
		return ErrRefused
	}
	return r.revertSince(green, main, path)
}

func (r *Repo) setGreen(commit string) {
	_ = os.WriteFile(r.state("postsubmit", "green"), []byte(commit+"\n"), 0o644)
}

// revertSince makes one commit on main that undoes green..red, landed through the check like any push, with the
// failure's last lines in its message. When main moved meanwhile, it goes again on the new main, three times.
func (r *Repo) revertSince(green, red, path string) error {
	dir := r.state("post")
	author, _ := r.git("log", "-1", "--format=%an <%ae>", red)
	count, _ := r.git("rev-list", "--count", green+".."+red)
	commits, _ := r.git("log", "--format=- %h %s", green+".."+red)
	message := fmt.Sprintf("revert: %s commits, red in postsubmit\n\nReverts %s..%s, which failed postsubmit:\n%s\n\nThe failure, its last lines (%s):\n%s",
		count, short(green), short(red), commits, path, tail(path, 30))
	host, _ := os.Hostname()
	ident := []string{"-c", "user.name=crate", "-c", "user.email=crate@" + host}
	for tries := 0; tries < 3; tries++ {
		_, _ = work(dir, "checkout", "-q", "-f", "--detach", r.main())
		if _, err := work(dir, append(ident, "revert", "--no-commit", green+".."+red)...); err != nil {
			_, _ = work(dir, "revert", "--abort")
			r.log("postsubmit %s %s not reverted: the revert does not apply; it needs a hand", r.Name(), short(red))
			return ErrRefused
		}
		if _, err := work(dir, append(ident, "commit", "-q", "--author="+author, "-m", message)...); err != nil {
			_, _ = work(dir, "revert", "--abort")
			r.log("postsubmit %s %s not reverted: the revert does not apply; it needs a hand", r.Name(), short(red))
			return ErrRefused
		}
		pushed, err := work(dir, "push", "-q", r.Bare, "HEAD:refs/heads/main")
		appendTo(path, pushed)
		if err == nil {
			head, _ := work(dir, "rev-parse", "--short=12", "HEAD")
			r.log("postsubmit %s %s reverted by %s", r.Name(), short(red), head)
			return nil
		}
	}
	r.log("postsubmit %s %s not reverted: main kept moving, or the revert's check failed", r.Name(), short(red))
	return ErrRefused
}

func tail(path string, n int) string {
	data, _ := os.ReadFile(path)
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return strings.Join(all, "\n")
}

func appendTo(path, text string) {
	if text == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, text)
}
