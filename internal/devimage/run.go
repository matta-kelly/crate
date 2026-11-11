package devimage

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/matta-kelly/crate/internal/limit"
)

// call is one ask of an outside program. It has a limit: passing it stops the call and says what it waited for.
type call struct {
	name  string // what the reason line calls the step
	limit time.Duration
	dir   string
	prog  string
	args  []string
}

// run runs the call and ends its whole process group at the limit. The error holds the step's name.
func run(c call, stdout, stderr io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.prog, c.args...)
	cmd.Dir = c.dir
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s did not finish within %v", c.name, c.limit)
	}
	if err != nil {
		return fmt.Errorf("%s failed: %w", c.name, err)
	}
	return nil
}

// git runs git in dir and gives its stdout as it is. A failure gives git's own message.
func git(dir string, args ...string) (string, error) {
	var out, errb strings.Builder
	err := run(call{name: "git " + args[0], limit: limit.Git(), dir: dir, prog: "git", args: args}, &out, &errb)
	if err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return "", fmt.Errorf("git %s: %s", args[0], msg)
		}
		return "", err
	}
	return out.String(), nil
}
