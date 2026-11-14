package gate

import (
	"fmt"
	"io"
	"strings"
)

// The door: what a push to the bare repo may do. A change enters the queue as its own ref, refs/queue/<sha>, and
// leaves it by deleting that ref. Main is never pushed: only the queue moves it.

// Update is one ref a push moves: from old to new, zeros for none.
type Update struct{ Old, New, Ref string }

// Admit is pre-receive: each update it refuses, with why, and nothing when the push may go in.
func (r *Repo) Admit(updates []Update) []string {
	var refused []string
	for _, u := range updates {
		if why := r.admit(u); why != "" {
			refused = append(refused, why)
		}
	}
	return refused
}

func (r *Repo) admit(u Update) string {
	if tag, ok := strings.CutPrefix(u.Ref, "refs/tags/"); ok {
		if VersionTag(tag) {
			return fmt.Sprintf("%s is a version tag, and only crate makes those. Push no version tags. To set a first version, run on the server: %s",
				tag, r.FirstVersionCommand(tag))
		}
		return ""
	}
	if u.Ref == "refs/heads/main" {
		return "main moves only through the merge queue: crate land, or git push <server> HEAD:refs/queue/<its sha>"
	}
	if strings.HasPrefix(u.Ref, "refs/landed/") {
		return "only crate writes refs/landed: each is a change's verdict"
	}
	sha, ok := strings.CutPrefix(u.Ref, "refs/queue/")
	if Zero(u.New) {
		if ok {
			return ""
		}
		return fmt.Sprintf("%s is kept: of what is pushed, only a change in the queue is deleted", u.Ref)
	}
	if !ok {
		return ""
	}
	main := r.main()
	switch {
	case sha != u.New:
		return fmt.Sprintf("%s is not the commit it names: push HEAD:refs/queue/<its sha>", u.Ref)
	case main == "":
		return "main has no commit yet: crate apply seeds it from GitHub"
	}
	if _, err := r.git("merge-base", main, u.New); err != nil {
		return fmt.Sprintf("%s shares no history with main", short(u.New))
	}
	if _, err := r.git("merge-base", "--is-ancestor", u.New, main); err == nil {
		return fmt.Sprintf("%s is on main already", short(u.New))
	}
	return ""
}

// Received is post-receive: each change that came in enters the queue, each one deleted leaves it, and the worker
// starts. With wait, it then prints each change's output until its verdict, and says whether all landed.
func (r *Repo) Received(updates []Update, wait bool, out io.Writer) (bool, error) {
	var entered []string
	touched := false
	for _, u := range updates {
		sha, ok := strings.CutPrefix(u.Ref, "refs/queue/")
		if !ok {
			continue
		}
		touched = true
		if Zero(u.New) {
			if err := r.Leave(sha, "it left the queue: its ref was deleted"); err != nil {
				return false, err
			}
			continue
		}
		place, err := r.Enter(sha, false)
		if err != nil {
			return false, err
		}
		fmt.Fprintf(out, "crate: %s is in the queue, place %d\n", short(sha), place)
		entered = append(entered, sha)
	}
	if !touched {
		return true, nil
	}
	if err := r.StartWorker(); err != nil {
		return false, err
	}
	if !wait {
		return true, nil
	}
	all := true
	for _, sha := range entered {
		landed, err := r.Wait(sha, out)
		if err != nil {
			return false, err
		}
		all = all && landed
	}
	return all, nil
}
