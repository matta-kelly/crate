package devimage

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/matta-kelly/crate/internal/limit"
	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/process"
)

// call is one ask of an outside program. It has a limit: passing it stops the call and says what it waited for.
type call struct {
	name  string // what the reason line calls the step
	limit time.Duration
	dir   string
	prog  string
	args  []string
	stdin io.Reader         // nil for none
	in    machine.Placement // the slice it runs in, so what it writes is the slice's; "" for the caller's own
}

// run runs the call by the runner (internal/process), so its limit, or the end of ctx, stops its whole process group.
// The error holds the step's name.
func run(ctx context.Context, c call, stdout, stderr io.Writer) error {
	prog, args, env := c.in.Command(c.prog, c.args...)
	return process.Run(ctx, process.Call{Name: c.name, Prog: prog, Args: args, Env: env, Dir: c.dir, Limit: c.limit,
		Stdin: c.stdin, Stdout: stdout, Stderr: stderr})
}

// git runs git in dir and gives its stdout as it is. A failure gives git's own message.
func git(dir string, args ...string) (string, error) {
	var out, errb strings.Builder
	err := run(context.Background(), call{name: "git " + args[0], limit: limit.Git(), dir: dir, prog: "git", args: args}, &out, &errb)
	if err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return "", fmt.Errorf("git %s: %s", args[0], msg)
		}
		return "", err
	}
	return out.String(), nil
}
