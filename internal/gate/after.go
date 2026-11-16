package gate

import (
	"fmt"
	"io"
)

// A main is green once it passed every set it has: the gate's, and postsubmit's when the repo names one. Only a green
// main is published, so GitHub, and everything that builds or deploys from it, never sees a main postsubmit failed.

// after is what follows a move of main to commit. A repo with no postsubmit is green as it lands, and is published now;
// one with postsubmit starts it, and its run publishes main once it is green (Postsubmit).
func (r *Repo) after(commit string, out io.Writer) {
	if r.PostsubmitOf(commit) == "" {
		r.publish(commit, out)
		return
	}
	if err := r.startPostsubmit(); err != nil {
		fmt.Fprintf(out, "crate: postsubmit did not start: %v\n", err)
		return
	}
	fmt.Fprintf(out, "crate: postsubmit runs %s in the background; GitHub gets main when it passes, and red reverts it through the queue\n", short(commit))
}

// publish is what follows a green main: the copy to GitHub, then the release. A failed copy leaves GitHub's main where
// it was, and the next green main copies again.
func (r *Repo) publish(commit string, out io.Writer) {
	if r.CopyMain(commit) != nil {
		fmt.Fprintln(out, "crate: the copy to github failed; the next green main copies again")
	}
	r.Release(commit, out)
}
