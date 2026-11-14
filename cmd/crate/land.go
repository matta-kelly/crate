package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

func init() { register("land", landVerb) }

const landUsage = "crate [-C <path>] land [--no-wait]"

// landVerb puts HEAD on main through the repo's merge queue, on its remote server: HEAD enters the queue as
// refs/queue/<sha>, and the push waits for crate's verdict, printing the check's output as it comes. The land is done
// when the server keeps refs/landed/<sha>, at the main commit it landed as; HEAD's branch then moves to main, and the
// repo's main checkout fast-forwards. --no-wait returns once HEAD is queued. Not landed exits 1, crate's verdict line
// above it.
func landVerb(args []string) error {
	flags := flag.NewFlagSet("land", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	noWait := flags.Bool("no-wait", false, "")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		return usagef("%s", landUsage)
	}
	if _, err := gitOut("remote", "get-url", "server"); err != nil {
		return errors.New("this checkout has no remote named server: a repo on crate lands through its server's merge queue")
	}
	if err := gitRun(io.Discard, "fetch", "-q", "server", "main"); err != nil {
		return err
	}
	sha, err := gitOut("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if ahead, _ := gitOut("rev-list", "server/main..HEAD"); ahead == "" {
		return errors.New("HEAD has nothing that server/main lacks")
	}
	ref := "refs/queue/" + sha
	switch {
	case queued(ref):
		fmt.Printf("crate land: %s is in the queue already\n", sha[:7])
		if *noWait {
			return nil
		}
		fmt.Println("crate land: waiting for its verdict")
		for queued(ref) {
			time.Sleep(10 * time.Second)
		}
	case *noWait:
		if err := gitRun(os.Stderr, "push", "-q", "--no-verify", "-o", "crate.wait=no", "server", "HEAD:"+ref); err != nil {
			return fmt.Errorf("the server refused %s; its reason is above", sha[:7])
		}
		fmt.Printf("crate land: %s is queued; it lands when its check passes\n", sha[:7])
		return nil
	default:
		if err := gitRun(os.Stderr, "push", "--no-verify", "server", "HEAD:"+ref); err != nil {
			return fmt.Errorf("the server refused %s; its reason is above", sha[:7])
		}
	}
	// The verdict is the server's: refs/landed/<sha>, at the main commit the change landed as.
	if gitRun(io.Discard, "ls-remote", "--exit-code", "server", "refs/landed/"+sha) != nil {
		fmt.Fprintf(os.Stderr, "crate land: %s did not land; crate's verdict is above. Fix it, commit, and land again\n", sha[:7])
		return commandExit(1)
	}
	if err := gitRun(io.Discard, "fetch", "-q", "server", "main"); err != nil {
		return err
	}
	main, _ := gitOut("rev-parse", "--short", "server/main")
	fmt.Printf("crate land: %s landed; main is %s on the server, and crate copies it to GitHub\n", sha[:7], main)
	if err := gitRun(io.Discard, "reset", "-q", "--keep", "server/main"); err != nil {
		fmt.Fprintln(os.Stderr, "crate land: HEAD not moved to server/main: uncommitted changes in the same files")
	}
	fastForwardMain()
	return nil
}

// queued says the server's queue holds the ref.
func queued(ref string) bool { return gitRun(io.Discard, "ls-remote", "--exit-code", "server", ref) == nil }

// fastForwardMain moves the repo's main checkout, its first worktree, to server/main when it has main checked out and
// the move is a clean fast-forward. One with its own commits, or an uncommitted change in the same files, stays.
func fastForwardMain() {
	list, err := gitOut("worktree", "list", "--porcelain")
	if err != nil {
		return
	}
	first, _, _ := strings.Cut(list, "\n")
	dir, ok := strings.CutPrefix(first, "worktree ")
	if !ok {
		return
	}
	if head, _ := gitOut("-C", dir, "symbolic-ref", "-q", "HEAD"); head != "refs/heads/main" {
		return
	}
	if err := gitRun(io.Discard, "-C", dir, "merge", "-q", "--ff-only", "server/main"); err != nil {
		fmt.Fprintf(os.Stderr, "crate land: %s not moved: it has its own commits, or uncommitted changes in the same files\n", dir)
		return
	}
	at, _ := gitOut("-C", dir, "rev-parse", "--short", "HEAD")
	fmt.Printf("crate land: %s is at %s\n", dir, at)
}

// gitOut runs git and gives its stdout, trimmed.
func gitOut(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

// gitRun runs git with its stdout and stderr to out.
func gitRun(out io.Writer, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}
