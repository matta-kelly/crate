package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// rollback moves the major tag to v<version> and copies that release's asset onto the major's. It refuses a
// version whose asset lacks hooks/ when the major's asset has it, so a rollback never takes the hooks away
// from an install.
func rollback(v version) error {
	if _, err := git("fetch", "-q", "--force", "origin", "--tags"); err != nil {
		return err
	}
	if tagged, err := tagAt(v.tag); err != nil || tagged == "" {
		return fmt.Errorf("no tag %s", v.tag)
	}
	scratch, err := os.MkdirTemp("", "crate-rollback-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)

	back, err := download(v.tag, filepath.Join(scratch, "back"))
	if err != nil {
		return err
	}
	if _, err := release("view", v.major); err == nil {
		now, err := download(v.major, filepath.Join(scratch, "now"))
		if err != nil {
			return err
		}
		hadHooks, err := hasHooks(now)
		if err != nil {
			return err
		}
		hasThem, err := hasHooks(back)
		if err != nil {
			return err
		}
		if hadHooks && !hasThem {
			return fmt.Errorf("the asset of %s has no hooks/, which the asset of %s has: a rollback never takes the hooks away from an install", v.tag, v.major)
		}
	}
	if _, err := git("tag", "-f", v.major, v.tag); err != nil {
		return err
	}
	if _, err := git("push", "-q", "-f", "origin", v.major); err != nil {
		return err
	}
	if err := putAsset(v.major, back, "crate "+v.major+": the latest "+v.major+" version"); err != nil {
		return err
	}
	fmt.Printf("release: %s points at %s\n", v.major, v.tag)
	return nil
}

// download puts the asset of the release in dir, under the name it has on GitHub.
func download(tag, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, assetName)
	_, err := release("download", tag, "--pattern", assetName, "--output", path, "--clobber")
	return path, err
}
