package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/matta-kelly/crate/internal/gate"
)

func init() {
	register("hook", hookVerb)
	register("queue", queueVerb)
	register("postsubmit", postsubmitVerb)
}

const (
	hookUsage  = "crate hook <pre-receive|post-receive>"
	queueUsage = "crate queue <bare repo> [jump <sha> | run]"
)

// hookVerb runs one of crate's git hooks in the bare repo git runs it in, on the ref lines git gives on stdin.
// pre-receive is the queue's door; post-receive puts what came in on the queue and, unless the push asks with
// -o crate.wait=no, prints each change's output until its verdict.
func hookVerb(args []string) error {
	if len(args) != 1 {
		return usagef("%s", hookUsage)
	}
	r, err := gate.Open(".")
	if err != nil {
		return err
	}
	var updates []gate.Update
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		if f := strings.Fields(scan.Text()); len(f) == 3 {
			updates = append(updates, gate.Update{Old: f[0], New: f[1], Ref: f[2]})
		}
	}
	switch args[0] {
	case "pre-receive":
		refused := r.Admit(updates)
		for _, why := range refused {
			fmt.Fprintf(os.Stderr, "crate: %s\n", why)
		}
		if len(refused) > 0 {
			return commandExit(1)
		}
		return nil
	case "post-receive":
		_, err := r.Received(updates, !pushOption("crate.wait=no"), os.Stdout)
		return err
	default:
		return usagef("%s", hookUsage)
	}
}

// pushOption says the push gave the option, as git hands push options to its hooks.
func pushOption(option string) bool {
	n, _ := strconv.Atoi(os.Getenv("GIT_PUSH_OPTION_COUNT"))
	for i := range n {
		if os.Getenv("GIT_PUSH_OPTION_"+strconv.Itoa(i)) == option {
			return true
		}
	}
	return false
}

// queueVerb shows a repo's queue, puts a change first, or runs the repo's worker.
func queueVerb(args []string) error {
	if len(args) < 1 {
		return usagef("%s", queueUsage)
	}
	r, err := gate.Open(args[0])
	if err != nil {
		return err
	}
	switch {
	case len(args) == 1:
		return r.Queue(os.Stdout)
	case len(args) == 2 && args[1] == "run":
		return r.Work(os.Stdout)
	case len(args) == 3 && args[1] == "jump":
		sha, err := r.Resolve(args[2])
		if err != nil {
			return err
		}
		if err := r.Jump(sha); err != nil {
			return err
		}
		fmt.Printf("crate: %s is first in the queue; every run starts again behind it\n", sha[:12])
		return r.StartWorker()
	default:
		return usagef("%s", queueUsage)
	}
}

// postsubmitVerb runs the repo's postsubmit on main as it stands. Green releases main; red reverts the commits since
// the last green main, through the queue.
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
