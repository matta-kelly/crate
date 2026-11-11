package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/matta-kelly/crate/internal/limit"
)

// publish: from a clean main equal to GitHub's, run crate's check, tag v<version>, move the major tag to it,
// and put the asset on the releases of both. A run that failed goes on from the step that failed: a tag at this
// commit is kept (its check passed), and a release already made gets its asset replaced.
// A dry run builds the asset and does nothing else.
func publish(v version, dryRun bool) error {
	root, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if err := os.Chdir(root); err != nil {
		return err
	}
	commit, err := git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if dryRun {
		path, err := buildAsset(root, v, commit)
		if err == nil {
			fmt.Println(path)
		}
		return err
	}
	if err := requireCleanMain(); err != nil {
		return err
	}
	if err := requirePublic(); err != nil {
		return err
	}
	tagged, err := tagAt(v.tag)
	if err != nil {
		return err
	}
	switch {
	case tagged == commit: // a run that failed after its check kept the tag
	case tagged != "":
		return fmt.Errorf("%s exists at %s, not at this commit", v.tag, tagged)
	default:
		if _, err := run(call{name: "just check", limit: limit.Check(), stream: true, prog: "just", args: []string{"check"}}); err != nil {
			return err
		}
		if _, err := git("tag", v.tag); err != nil {
			return err
		}
	}
	if _, err := git("tag", "-f", v.major, v.tag); err != nil {
		return err
	}
	if _, err := git("push", "-q", "origin", v.tag); err != nil {
		return err
	}
	if _, err := git("push", "-q", "-f", "origin", v.major); err != nil {
		return err
	}
	path, err := buildAsset(root, v, commit)
	if err != nil {
		return err
	}
	if err := putAsset(v.tag, path, "crate "+v.number); err != nil {
		return err
	}
	if err := putAsset(v.major, path, "crate "+v.major+": the latest "+v.major+" version"); err != nil {
		return err
	}
	fmt.Printf("release: %s, and %s points at it\n", v.tag, v.major)
	return nil
}

func requireCleanMain() error {
	branch, err := git("symbolic-ref", "--short", "HEAD")
	if err != nil {
		return err
	}
	if branch != "main" {
		return fmt.Errorf("not on main")
	}
	dirty, err := git("status", "--porcelain")
	if err != nil {
		return err
	}
	if dirty != "" {
		return fmt.Errorf("the checkout has uncommitted changes")
	}
	if _, err := git("fetch", "-q", "--force", "origin", "main", "--tags"); err != nil {
		return err
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	github, err := git("rev-parse", "origin/main")
	if err != nil {
		return err
	}
	if head != github {
		return fmt.Errorf("main is not GitHub's main")
	}
	return nil
}

// requirePublic: the asset is read with no token, so the repo must be public.
func requirePublic() error {
	visibility, err := gh("repo", "view", repo, "--json", "visibility", "--jq", ".visibility")
	if err != nil {
		return err
	}
	if strings.TrimSpace(visibility) != "PUBLIC" {
		return fmt.Errorf("%s is %s: the asset needs no token only when the repo is PUBLIC", repo, visibility)
	}
	return nil
}

// tagAt is the commit a tag names, or "" when there is no such tag.
func tagAt(tag string) (string, error) {
	if _, err := git("rev-parse", "-q", "--verify", "refs/tags/"+tag); err != nil {
		return "", nil
	}
	return git("rev-parse", "refs/tags/"+tag+"^{commit}")
}
