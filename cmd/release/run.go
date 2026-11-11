package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/matta-kelly/crate/internal/limit"
)

// call is one ask of an outside program, with its limit. Passing the limit stops the call and says what it
// waited for.
type call struct {
	name   string
	limit  time.Duration
	env    []string
	stream bool // its output goes to stderr as it comes, for a step that runs minutes
	prog   string
	args   []string
}

// run runs the call in the current folder and gives its stdout. A failure holds the step's name and, when the
// output was not streamed, the program's own message.
func run(c call) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.prog, c.args...)
	cmd.Env = append(os.Environ(), c.env...)
	var out, errb bytes.Buffer
	if c.stream {
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	} else {
		cmd.Stdout, cmd.Stderr = &out, &errb
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s did not finish within %v", c.name, c.limit)
	}
	if err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return "", fmt.Errorf("%s failed: %s", c.name, msg)
		}
		return "", fmt.Errorf("%s failed: %w", c.name, err)
	}
	return strings.TrimSpace(out.String()), nil
}

func git(args ...string) (string, error) {
	return run(call{name: "git " + args[0], limit: limit.Git(), prog: "git", args: args})
}

func gh(args ...string) (string, error) {
	name := "gh " + args[0] + " " + args[1]
	if len(args) > 2 && !strings.HasPrefix(args[2], "-") {
		name += " " + args[2]
	}
	return run(call{name: name, limit: limit.Gh(), prog: "gh", args: args})
}

// release is `gh release <sub> <tag> ...` on crate's repo. The asset, when there is one, comes last.
func release(sub, tag string, rest ...string) (string, error) {
	return gh(append([]string{"release", sub, tag, "--repo", repo}, rest...)...)
}
