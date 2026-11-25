package gate

import (
	"github.com/matta-kelly/crate/internal/record"
	"github.com/matta-kelly/crate/internal/status"
)

// report writes a change's state on the repo's GitHub copy as crate's commit status, for a change from the intake,
// and records the post as a status run: pass with the state, or error with why. It never fails its caller: the
// change is decided as if no post had been tried. A change with no branch (a person's, a revert) gets none.
func (r *Repo) report(e Entry, state, description string) {
	if e.Branch == "" {
		return
	}
	rec := r.begin("status", "", e.Sha, nil)
	url, err := r.git("remote", "get-url", "github")
	if err == nil {
		var repo string
		if repo, err = status.Repo(url); err == nil {
			err = status.Post(repo, e.Sha, state, description)
		}
	}
	if err != nil {
		rec.End(record.Error, err.Error())
		return
	}
	rec.End(record.Pass, state)
}
