package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/matta-kelly/crate/internal/gate"
)

func init() {
	register("hook", hookVerb)
	register("postsubmit", postsubmitVerb)
}

const hookUsage = "crate hook <pre-receive|post-receive>"

// installRoot is the folder that holds this binary's bin/ and hooks/.
func installRoot() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return "", err
	}
	return filepath.Dir(filepath.Dir(self)), nil
}

// hookVerb runs one of crate's git hooks in the bare repo git runs it in, on the ref lines git gives on stdin.
func hookVerb(args []string) error {
	if len(args) != 1 {
		return usagef("%s", hookUsage)
	}
	r, err := gate.Open(".")
	if err != nil {
		return err
	}
	type update struct{ old, new, ref string }
	var updates []update
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		f := strings.Fields(scan.Text())
		if len(f) == 3 {
			updates = append(updates, update{f[0], f[1], f[2]})
		}
	}
	switch args[0] {
	case "pre-receive":
		// main moves only to a commit whose check passed on the exact tree main becomes. A branch other than main
		// takes no check, and a version tag is refused in every repo.
		refused := false
		for _, u := range updates {
			if tag, ok := strings.CutPrefix(u.ref, "refs/tags/"); ok {
				if gate.VersionTag(tag) {
					fmt.Fprintf(os.Stderr, "crate: %s is a version tag, and only crate makes those. Push no version tags. To set a first version, run on the server: %s\n",
						tag, r.FirstVersionCommand(tag))
					refused = true
				}
				continue
			}
			if u.ref != "refs/heads/main" {
				continue
			}
			if gate.Zero(u.new) {
				fmt.Fprintln(os.Stderr, "crate: main is never deleted")
				refused = true
				continue
			}
			if err := r.Check(u.old, u.new, os.Stdout); err != nil {
				refused = true
			}
		}
		if refused {
			return commandExit(1)
		}
		return nil
	case "post-receive":
		// After main moves: copy it to github, then release it. A repo with postsubmit runs it in the background
		// first, and is released only when it passes. A failed copy leaves main as it is; the next move copies again.
		moved := ""
		for _, u := range updates {
			if u.ref != "refs/heads/main" || gate.Zero(u.new) {
				continue
			}
			moved = u.new
			if r.CopyMain(u.new) != nil {
				fmt.Fprintln(os.Stderr, "crate: the copy to github failed; main has moved, and the next move copies again")
			}
		}
		if moved == "" {
			return nil
		}
		if r.PostsubmitOf(moved) == "" {
			r.Release("", os.Stdout)
			return nil
		}
		if err := startPostsubmit(r.Bare); err != nil {
			return err
		}
		fmt.Printf("crate: postsubmit runs %s in the background; red reverts it on main\n", moved[:12])
		return nil
	default:
		return usagef("%s", hookUsage)
	}
}

// startPostsubmit starts `crate postsubmit <bare>` in its own session, with no tie to the push, so the pusher does
// not wait and a closed connection does not stop it. Its output goes to <bare>/crate/postsubmit/run.log.
func startPostsubmit(bare string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(bare, "crate", "postsubmit"), 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(bare, "crate", "postsubmit", "run.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	// git hands its hooks more open files than stdin, stdout and stderr, and waits for the push until every copy of
	// them is closed: the child must not keep one.
	if fds, err := os.ReadDir("/proc/self/fd"); err == nil {
		for _, fd := range fds {
			if n, err := strconv.Atoi(fd.Name()); err == nil && n > 2 {
				syscall.CloseOnExec(n)
			}
		}
	}
	cmd := exec.Command(self, "postsubmit", bare)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// postsubmitVerb runs the repo's postsubmit on main as it stands. Green releases main; red reverts the commits since
// the last green main.
func postsubmitVerb(args []string) error {
	if len(args) != 1 {
		return usagef("crate postsubmit <bare repo>")
	}
	r, err := gate.Open(args[0])
	if err != nil {
		return err
	}
	if err := r.Postsubmit(os.Stdout); err != nil {
		return commandExit(1)
	}
	return nil
}
