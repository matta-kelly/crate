package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/matta-kelly/crate/internal/limit"
)

const assetName = "crate-linux-amd64.tar.gz"

// buildAsset puts the asset in dist/ at the root of the repo: bin/crate (a static binary that says its version
// and commit), VERSION and COMMIT, at the root of the archive with no folder before them. It gives the path.
func buildAsset(root string, v version, commit string) (string, error) {
	scratch, err := os.MkdirTemp("", "crate-build-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(scratch)
	binary := filepath.Join(scratch, "crate")
	_, err = run(call{name: "go build", limit: limit.Build(), stream: true, prog: "go",
		env: []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"},
		args: []string{"build", "-trimpath", "-ldflags", fmt.Sprintf("-s -w -X main.version=%s -X main.commit=%s", v.number, commit),
			"-o", binary, "./cmd/crate"}})
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(binary)
	if err != nil {
		return "", err
	}
	dist := filepath.Join(root, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dist, assetName)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body []byte
		mode int64
	}{{"bin/crate", body, 0o755}, {"VERSION", []byte(v.number + "\n"), 0o644}, {"COMMIT", []byte(commit + "\n"), 0o644}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.body))}); err != nil {
			return "", err
		}
		if _, err := tw.Write(f.body); err != nil {
			return "", err
		}
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, buf.Bytes(), 0o644)
}

// hasHooks says the asset at path holds hooks/.
func hasHooks(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("%s: %w", path, err)
		}
		if strings.HasPrefix(h.Name, "hooks/") {
			return true, nil
		}
	}
}

// putAsset puts the file on the release of the tag: the release is made when it is not there, and its asset
// replaced when it is, so a run that failed goes on from the step that failed.
func putAsset(tag, path, notes string) error {
	if _, err := release("view", tag); err == nil {
		_, err = release("upload", tag, "--clobber", path)
		return err
	}
	_, err := release("create", tag, "--verify-tag", "--title", tag, "--notes", notes, path)
	return err
}
