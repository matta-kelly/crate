// Package gate is crate's guard of a repo's main on the server: the check before main moves, the copy to GitHub
// after, postsubmit, and the release. A hook runs in the bare repo. Its state lives beside it, in <bare>/crate:
//
//	work/              the check's checkout: a repo of its own that borrows the bare repo's objects, kept warm
//	post/              postsubmit's checkout, kept the same way
//	lock               one check at a time per repo: the merge queue
//	release.lock       one release at a time, so two moves of main are released in order
//	postsubmit.lock    one postsubmit at a time
//	postsubmit/        green (the last main that passed), <sha>.log (each run's output), run.log
//	log                one line per check, copy, release and postsubmit
package gate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Repo is one bare repo on the server.
type Repo struct {
	Bare       string   // the bare repo, absolute
	bareEnv    []string // the hook's env, which reads the pushed objects from git's quarantine
	quarantine string   // GIT_QUARANTINE_PATH while a pre-receive runs, else ""
}

// Open is the bare repo at bare. It takes the process's env as the bare repo's, then clears what points git at the
// bare repo, so every other program crate runs (a checkout's git, the dev image, podman) sees a plain env, with the
// quarantine as an alternate so a checkout reads the pushed objects.
func Open(bare string) (*Repo, error) {
	abs, err := filepath.Abs(bare)
	if err != nil {
		return nil, err
	}
	r := &Repo{Bare: abs, bareEnv: os.Environ(), quarantine: os.Getenv("GIT_QUARANTINE_PATH")}
	for _, key := range []string{"GIT_DIR", "GIT_QUARANTINE_PATH", "GIT_OBJECT_DIRECTORY"} {
		os.Unsetenv(key)
	}
	os.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", r.quarantine)
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

// checkout makes the checkout dir, borrowing the bare repo's objects, when it is not there yet.
func (r *Repo) checkout(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil
	}
	if out, err := work(".", "init", "-q", dir); err != nil {
		return fmt.Errorf("git init %s: %s", dir, out)
	}
	return os.WriteFile(filepath.Join(dir, ".git", "objects", "info", "alternates"), []byte(filepath.Join(r.Bare, "objects")+"\n"), 0o644)
}

// log adds one line to <bare>/crate/log.
func (r *Repo) log(format string, a ...any) {
	f, err := os.OpenFile(r.state("log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format("2006-01-02T15:04:05Z"), fmt.Sprintf(format, a...))
}

// lock takes the lock file name, waiting for it. The lock is freed when the process ends, or by the func it gives.
func (r *Repo) lock(name string) (func(), error) {
	f, err := os.OpenFile(r.state(name), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
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
	if _, err := r.git("push", "-q", "github", commit+":refs/heads/main"); err != nil {
		r.log("mirror %s %s fail", r.Name(), short(commit))
		return err
	}
	r.log("mirror %s %s pass", r.Name(), short(commit))
	return nil
}
