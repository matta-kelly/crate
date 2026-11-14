package gate

import (
	"fmt"
	"os"
	"strings"
)

// build makes a run's tree in its checkout dir: base, then each change on top, in order, and gives its tip. A change
// whose commit is on the tip already moves the tip to it. Any other has its own commits picked onto the tip, its
// author kept and crate the committer, and a commit the tip holds already dropped. A change that does not apply is
// left out, with why; the rest go on.
func (r *Repo) build(dir, base string, changes []Entry) (tip string, in []Entry, out map[string]string, err error) {
	if err := r.worktree(dir); err != nil {
		return "", nil, nil, err
	}
	if o, err := work(dir, "checkout", "-q", "-f", "--detach", base); err != nil {
		return "", nil, nil, fmt.Errorf("checkout %s: %s", short(base), o)
	}
	_, _ = work(dir, "clean", "-q", "-fdx", "-e", "node_modules")
	host, _ := os.Hostname()
	ident := []string{"-c", "user.name=crate", "-c", "user.email=crate@" + host}
	tip, out = base, map[string]string{}
	for _, c := range changes {
		from, err := work(dir, "merge-base", c.Sha, tip)
		if err != nil {
			out[c.Sha] = "it shares no history with main"
			continue
		}
		if from == tip {
			if o, err := work(dir, "checkout", "-q", "--detach", c.Sha); err != nil {
				return "", nil, nil, fmt.Errorf("checkout %s: %s", short(c.Sha), o)
			}
		} else if o, err := work(dir, append(ident, "cherry-pick", "--empty=drop", from+".."+c.Sha)...); err != nil {
			why := "it does not apply on main and the changes ahead of it"
			if files, _ := work(dir, "diff", "--name-only", "--diff-filter=U"); files != "" {
				why += " (" + strings.ReplaceAll(files, "\n", ", ") + ")"
			} else {
				why += ": " + firstOf(o)
			}
			_, _ = work(dir, "cherry-pick", "--abort")
			_, _ = work(dir, "checkout", "-q", "-f", "--detach", tip)
			out[c.Sha] = why + "; rebase onto server/main and land again"
			continue
		}
		if tip, err = work(dir, "rev-parse", "HEAD"); err != nil {
			return "", nil, nil, err
		}
		in = append(in, c)
	}
	return tip, in, out, nil
}

func firstOf(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
