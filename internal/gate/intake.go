package gate

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/matta-kelly/crate/internal/record"
	"github.com/matta-kelly/crate/internal/status"
)

// Intake: a change an update bot proposes as a branch on the repo's GitHub copy enters the queue as if it were pushed
// to it, and lands only as its check says. crate apply fetches the branches the server's INTAKE names (git refspec
// sources under refs/heads/, and ^ ones left out) into refs/intake/, and enters each head the queue has never held. A
// head enters once: its change's log in queue/ is kept, so a head that landed or was refused is not checked again,
// and a new push to the branch is a new head. The door's rules hold: the head shares history with main, and is not on
// main already. The copy to GitHub moves only main, so it never touches the bot's branches. crate's commit status on
// the head tells the bot and its pull request what became of it: pending as it enters, then its verdict.

// IntakeSpecs is the fetch's refspecs for patterns: refs/heads/<name> fetched to refs/intake/<name>, a ^ one as is.
func IntakeSpecs(patterns []string) ([]string, error) {
	var specs []string
	for _, p := range patterns {
		if strings.HasPrefix(p, "^refs/heads/") {
			specs = append(specs, p)
			continue
		}
		name, ok := strings.CutPrefix(p, "refs/heads/")
		if !ok || name == "" {
			return nil, fmt.Errorf("INTAKE %s: a pattern is refs/heads/<name>, or ^refs/heads/<name> to leave out", p)
		}
		specs = append(specs, "+"+p+":refs/intake/"+name)
	}
	return specs, nil
}

// Intake fetches the branches patterns name from GitHub and enters each new head in the queue. It prints what entered.
// A fetch that fails is its error, so the apply that ran it fails.
func (r *Repo) Intake(patterns []string, out io.Writer) error {
	specs, err := IntakeSpecs(patterns)
	if err != nil || len(specs) == 0 {
		return err
	}
	if _, err := r.git(append([]string{"fetch", "-q", "--prune", "--no-tags", "github"}, specs...)...); err != nil {
		return fmt.Errorf("the intake's fetch from GitHub: %w", err)
	}
	heads, err := r.git("for-each-ref", "--format=%(objectname) %(refname)", "refs/intake/")
	if err != nil {
		return err
	}
	main := r.main()
	if main == "" {
		return nil
	}
	for _, line := range lines(heads) {
		sha, ref, _ := strings.Cut(line, " ")
		branch := strings.TrimPrefix(ref, "refs/intake/")
		if _, err := os.Stat(r.entryLog(sha)); err == nil {
			continue
		}
		if _, err := r.git("merge-base", "--is-ancestor", sha, main); err == nil {
			continue
		}
		if _, err := r.git("merge-base", main, sha); err != nil {
			continue
		}
		rec := r.begin("intake", "", sha, nil)
		if _, err := r.git("update-ref", queueRef(sha), sha); err != nil {
			rec.End(record.Fail, err.Error())
			return err
		}
		r.report(Entry{Sha: sha, Branch: branch}, status.Pending, "in crate's queue")
		place, err := r.enter(sha, false, branch)
		if err != nil {
			rec.End(record.Fail, err.Error())
			return err
		}
		rec.End(record.Pass, branch)
		fmt.Fprintf(out, "crate: %s: %s from GitHub's %s entered the queue, place %d\n", r.Name(), short(sha), branch, place)
	}
	return nil
}
