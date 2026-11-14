package gate

import (
	"fmt"
	"io"
)

// after is what follows a move of main to commit: the copy to GitHub, then postsubmit when the repo has it, which
// releases main once it passes, else the release now. A failed copy leaves main as it is; the next move copies again.
func (r *Repo) after(commit string, out io.Writer) {
	if r.CopyMain(commit) != nil {
		fmt.Fprintln(out, "crate: the copy to github failed; main has moved, and the next move copies again")
	}
	if r.PostsubmitOf(commit) == "" {
		r.Release(commit, out)
		return
	}
	if err := r.startPostsubmit(); err != nil {
		fmt.Fprintf(out, "crate: postsubmit did not start: %v\n", err)
		return
	}
	fmt.Fprintf(out, "crate: postsubmit runs %s in the background; red reverts it through the queue\n", short(commit))
}
