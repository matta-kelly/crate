// Package gate is crate's merge queue on a repo's main on the server, and what follows a move of main: the copy to
// GitHub, postsubmit and the release. Main moves only to a tree that passed its check, and only the queue moves it.
// A hook runs in the bare repo. The queue's state lives beside it, in <bare>/crate:
//
//	queue.json         the queue: its changes in order
//	queue.lock         one writer of queue.json at a time
//	queue/<sha>.log    each change's output, from every run it is in, ending in its verdict
//	worker.lock        held by the repo's one worker while it runs
//	worker.log         the worker's own output
//	runs/<n>/          run n's checkout: a worktree of the bare repo, kept warm
//	post/              postsubmit's checkout, kept the same way
//	release.lock       one release at a time, so two moves of main are released in order
//	postsubmit.lock    one postsubmit at a time
//	postsubmit/        green (the last main that passed), <sha>.log (each run's output), run.log
//	log.jsonl          the record: one JSON line per step of each run: check, land, postsubmit, mirror, release
package gate

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/matta-kelly/crate/internal/record"
)

// Repo is one bare repo on the server.
type Repo struct {
	Bare    string   // the bare repo, absolute
	bareEnv []string // the hook's env: in pre-receive, it reads the pushed objects from git's quarantine
}

// Open is the bare repo at bare. It takes the process's env as the bare repo's, then clears what points git at the
// bare repo, so every other program crate runs (a checkout's git, the dev image, podman) sees a plain env.
func Open(bare string) (*Repo, error) {
	abs, err := filepath.Abs(bare)
	if err != nil {
		return nil, err
	}
	r := &Repo{Bare: abs, bareEnv: os.Environ()}
	for _, key := range []string{"GIT_DIR", "GIT_QUARANTINE_PATH", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES"} {
		os.Unsetenv(key)
	}
	return r, nil
}

// Name is the repo's name, from its folder: que-pasa.git gives que-pasa.
func (r *Repo) Name() string { return strings.TrimSuffix(filepath.Base(r.Bare), ".git") }

func (r *Repo) state(parts ...string) string {
	return filepath.Join(append([]string{r.Bare, "crate"}, parts...)...)
}

// git runs git in the bare repo, with the hook's env. Its stdout, trimmed.
func (r *Repo) git(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", r.Bare}, args...)...)
	cmd.Env = r.bareEnv
	var errb strings.Builder
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("git %s: %s: %w", args[0], strings.TrimSpace(errb.String()), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// main is main's commit, or "" when there is none.
func (r *Repo) main() string {
	out, err := r.git("rev-parse", "--verify", "-q", "refs/heads/main")
	if err != nil {
		return ""
	}
	return out
}

// hasGithub says the bare repo copies to the remote github.
func (r *Repo) hasGithub() bool {
	_, err := r.git("remote", "get-url", "github")
	return err == nil
}

// work runs git in a checkout of the bare repo, with the plain env. Its combined output.
func work(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// worktree makes dir a worktree of the bare repo, detached, when it is not one yet. A worktree shares the bare repo's
// objects and refs, so a commit made in it is the bare repo's at once.
func (r *Repo) worktree(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil
	}
	_ = os.RemoveAll(dir)
	_, _ = r.git("worktree", "prune")
	if _, err := r.git("worktree", "add", "-q", "--detach", dir); err != nil {
		return err
	}
	return nil
}

// dropWorktree removes the worktree at dir, and what git keeps of it.
func (r *Repo) dropWorktree(dir string) {
	_, _ = r.git("worktree", "remove", "--force", dir)
	_ = os.RemoveAll(dir)
	_, _ = r.git("worktree", "prune")
}

// begin starts a run of kind on commit, on the repo's record, <bare>/crate/log.jsonl. Its summary goes to out; a nil
// out prints none.
func (r *Repo) begin(kind, system, commit string, out io.Writer) *record.Run {
	return record.Start(r.state("log.jsonl"), out, record.Line{Kind: kind, Repo: r.Name(), System: system, Commit: commit})
}

// lock takes the lock file name, waiting for it. The lock is freed when the process ends, or by the func it gives.
func (r *Repo) lock(name string) (func(), error) { return r.flock(name, syscall.LOCK_EX) }

// tryLock takes the lock file name when it is free, and says whether it took it.
func (r *Repo) tryLock(name string) (func(), bool) {
	unlock, err := r.flock(name, syscall.LOCK_EX|syscall.LOCK_NB)
	return unlock, err == nil
}

func (r *Repo) flock(name string, how int) (func(), error) {
	if err := os.MkdirAll(r.state(), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(r.state(name), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// Zero says sha is git's all-zeros id: no commit, a new ref or a deletion.
func Zero(sha string) bool { return regexp.MustCompile(`^0+$`).MatchString(sha) }

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// VersionTag says the tag is shaped like a version: v or <folder>-v, then a digit. Only the release makes those.
func VersionTag(tag string) bool {
	return regexp.MustCompile(`^v[0-9]`).MatchString(tag) || regexp.MustCompile(`-v[0-9]`).MatchString(tag)
}

// FirstVersionCommand is the command, run on the server, that sets a package's first version in the bare repo.
func (r *Repo) FirstVersionCommand(tag string) string {
	return fmt.Sprintf("git -C %s tag %s <commit>", r.Bare, tag)
}

// CopyMain pushes commit to github's main, when the bare repo has the remote, and logs the copy.
func (r *Repo) CopyMain(commit string) error {
	if !r.hasGithub() {
		return nil
	}
	rec := r.begin("mirror", "", commit, nil)
	if _, err := r.git("push", "-q", "github", commit+":refs/heads/main"); err != nil {
		rec.End(record.Fail, err.Error())
		return err
	}
	rec.End(record.Pass, "")
	return nil
}
