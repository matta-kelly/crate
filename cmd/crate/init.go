package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/matta-kelly/crate/internal/gate"
)

func init() {
	register("init", initVerb)
	register("apply", applyVerb)
}

const (
	initUsage  = "crate init <bare> [<github url>]"
	applyUsage = "crate apply <file|->"
)

// initVerb makes or takes a bare repo and puts crate's hooks and rules on it.
func initVerb(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return usagef("%s", initUsage)
	}
	github := ""
	if len(args) == 2 {
		github = args[1]
	}
	return initRepo(args[0], github, os.Stdout, true)
}

// installRoot is the folder that holds this binary's bin/ and hooks/.
func installRoot() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return "", err
	}
	return filepath.Dir(filepath.Dir(self)), nil
}

// initRepo makes or takes the bare repo, sets its hooks to this install's, refuses non-fast-forwards, and hands push
// options to the hooks (crate.wait=no). Deletions are the door's to judge: only a queue entry may be deleted. With a
// GitHub url, main is copied there after each move: an ssh url gets the repo's own deploy key, made here once
// (~/.ssh/crate-<repo>), and its public half is printed.
func initRepo(bare, github string, out io.Writer, say bool) error {
	root, err := installRoot()
	if err != nil {
		return err
	}
	git := func(args ...string) (string, error) {
		o, err := exec.Command("git", append([]string{"-C", bare}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(o)), err
	}
	if _, err := os.Stat(bare); os.IsNotExist(err) {
		if o, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", bare).CombinedOutput(); err != nil {
			return fmt.Errorf("git init %s: %s", bare, o)
		}
	}
	for _, kv := range [][2]string{{"core.hooksPath", filepath.Join(root, "hooks")}, {"receive.denyNonFastForwards", "true"},
		{"receive.denyDeletes", "false"}, {"receive.advertisePushOptions", "true"}} {
		if o, err := git("config", kv[0], kv[1]); err != nil {
			return fmt.Errorf("git config %s: %s", kv[0], o)
		}
	}
	if err := os.MkdirAll(filepath.Join(bare, "crate"), 0o755); err != nil {
		return err
	}
	if github != "" {
		if _, err := git("remote", "get-url", "github"); err == nil {
			_, err = git("remote", "set-url", "github", github)
			if err != nil {
				return err
			}
		} else if o, err := git("remote", "add", "github", github); err != nil {
			return fmt.Errorf("git remote add: %s", o)
		}
		if strings.HasPrefix(github, "git@") || strings.HasPrefix(github, "ssh://") {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			key := filepath.Join(home, ".ssh", "crate-"+strings.TrimSuffix(filepath.Base(bare), ".git"))
			if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
				return err
			}
			if _, err := os.Stat(key); os.IsNotExist(err) {
				comment := "crate " + strings.TrimSuffix(filepath.Base(bare), ".git")
				if o, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", comment, "-f", key).CombinedOutput(); err != nil {
					return fmt.Errorf("ssh-keygen: %s", o)
				}
				pub, _ := os.ReadFile(key + ".pub")
				fmt.Fprintf(out, "crate: the deploy key to add, with write access: %s", pub)
			}
			if _, err := git("config", "core.sshCommand", "ssh -i "+key+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"); err != nil {
				return err
			}
		}
	}
	if say {
		fmt.Fprintf(out, "crate: %s keeps main through its merge queue\n", bare)
	}
	return nil
}

// applyVerb makes this server match its declaration, KEY=value lines: GIT_HOME, where the bare repos live;
// GITHUB_BASE, the url each repo's copy goes to, before /<repo>.git; REPOS, the repos. Safe to run again: it inits
// each repo, seeds a new one's main from GitHub, and starts the worker of a repo whose queue holds changes, so a
// worker that stopped is started again. The binary's own version is its install's.
func applyVerb(args []string) error {
	if len(args) != 1 {
		return usagef("%s", applyUsage)
	}
	var in io.Reader = os.Stdin
	if args[0] != "-" {
		f, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}
	values := map[string]string{}
	scan := bufio.NewScanner(in)
	for scan.Scan() {
		key, value, ok := strings.Cut(scan.Text(), "=")
		if !ok {
			continue
		}
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	gitHome, base := values["GIT_HOME"], values["GITHUB_BASE"]
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(gitHome, "~") {
		gitHome = home + gitHome[1:]
	}
	if gitHome == "" || base == "" {
		return usagef("crate apply: GIT_HOME and GITHUB_BASE are needed")
	}
	if err := os.MkdirAll(gitHome, 0o755); err != nil {
		return err
	}
	failed := false
	for _, repo := range strings.Fields(values["REPOS"]) {
		bare := filepath.Join(gitHome, repo+".git")
		if err := initRepo(bare, base+"/"+repo+".git", os.Stdout, false); err != nil {
			return err
		}
		if exec.Command("git", "-C", bare, "rev-parse", "-q", "--verify", "refs/heads/main").Run() == nil {
			if r, err := gate.Open(bare); err == nil {
				if err := r.StartWorker(); err != nil {
					fmt.Fprintf(os.Stderr, "crate: %s: its worker did not start: %v\n", repo, err)
					failed = true
				}
			}
			continue
		}
		if exec.Command("git", "-C", bare, "fetch", "-q", "github", "main:refs/heads/main").Run() == nil {
			sha, _ := exec.Command("git", "-C", bare, "rev-parse", "--short", "main").Output()
			fmt.Printf("crate: %s seeded from GitHub's main (%s)\n", repo, strings.TrimSpace(string(sha)))
			continue
		}
		home, _ := os.UserHomeDir()
		pub, _ := os.ReadFile(filepath.Join(home, ".ssh", "crate-"+repo+".pub"))
		fmt.Fprintf(os.Stderr, "crate: %s has no main: GitHub refused the fetch. Add its deploy key, with write access, then apply again: %s\n",
			repo, strings.TrimSpace(string(pub)))
		failed = true
	}
	if failed {
		return commandExit(1)
	}
	return nil
}
