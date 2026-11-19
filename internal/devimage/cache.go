package devimage

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/matta-kelly/crate/internal/limit"
	"github.com/matta-kelly/crate/internal/ship"
)

// The cache: a dev image is addressed by the hash of its inputs, so the registry's copy is the same image on every
// machine. The server's merge queue is its one writer: each image a check makes there is pushed at once by its hash
// (Push), and each green main's is pushed again as <image>:main (Point), the image Ref falls back to. Every other
// machine only reads: Local pulls by hash, and builds only an image no check has made yet. The writes need podman
// alone.

// Of is the dev image's name and the hash of its inputs at o.Rev. o.Root may be a bare repo.
func Of(o Options) (image, hash string, err error) {
	p, hash, err := openAtHash(o)
	if err != nil {
		return "", "", err
	}
	return p.image, hash, nil
}

// Held is the hash of each of image's tags this machine's store holds.
func Held(image string) ([]string, error) {
	var out, errb bytes.Buffer
	if err := run(call{name: "podman images", limit: limit.Podman(), prog: "podman",
		args: []string{"images", "--format", "{{.Repository}}:{{.Tag}}", "--filter", "reference=" + image}}, &out, &errb); err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	var hashes []string
	for _, ref := range strings.Fields(out.String()) {
		repo, tag, ok := cutTag(ref)
		if ok && repo == image && hashTag.MatchString(tag) {
			hashes = append(hashes, tag)
		}
	}
	return hashes, nil
}

// Push sends this machine's <image>:<hash> to the registry, logged in by authfile. It builds nothing: the image is the
// one a check made here. A push of what the registry holds sends only its manifest again, so no ask comes first.
func Push(image, hash, authfile string, stderr io.Writer) error {
	return push(image+":"+hash, image+":"+hash, authfile, stderr)
}

// Point sends this machine's <image>:<hash> to the registry as <image>:main, logged in by authfile.
func Point(image, hash, authfile string, stderr io.Writer) error {
	return push(image+":"+hash, image+":main", authfile, stderr)
}

func push(local, dest, authfile string, stderr io.Writer) error {
	args := append([]string{"push", "--quiet"}, registryFlags(dest, authfile)...)
	return run(call{name: "podman push", limit: limit.Push(), prog: "podman", args: append(args, local, dest)}, stderr, stderr)
}

// registryFlags are the flags podman and skopeo take for ref's registry: the login, when one is given, and plain HTTP
// for a registry on this machine.
func registryFlags(ref, authfile string) []string {
	var flags []string
	if authfile != "" {
		flags = append(flags, "--authfile", authfile)
	}
	if ship.Plain(ref) {
		flags = append(flags, "--tls-verify=false")
	}
	return flags
}

// cutTag parts a ref into its repository and its tag at the last colon, so a registry's port stays in the repository.
func cutTag(ref string) (repo, tag string, ok bool) {
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.Contains(ref[i:], "/") {
		return ref, "", false
	}
	return ref[:i], ref[i+1:], true
}
