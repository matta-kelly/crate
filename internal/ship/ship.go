// Package ship publishes a green main's app: the image its check built and ran, and its run files beside it, each
// tagged with the main's build number, so what a node runs is what crate tested. The image goes by podman push; the run files go as an
// OCI artifact, one deterministic tar.gz layer, packed and pushed by oras-go, the library under the oras CLI.
// The node side of the artifact is monkeydo's (lanes/releases: fetchArtifact, and the unpack in releases.go).
package ship

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"
)

// The artifact's parts. The layer's title is the name a node saves it by, and its .tar.gz says to unpack it.
const (
	LayerTitle     = "quadlets.tar.gz"
	LayerMediaType = ocispec.MediaTypeImageLayerGzip
	SourceKey      = "org.opencontainers.image.source"
	RevisionKey    = "org.opencontainers.image.revision"
)

// Tag is the tag of a main's image and run files: its build number, the count of main's first-parent commits up to
// it. Main only moves forward, so a later main has a larger number, and an update bot orders the tags as versions.
// The commit itself is the run files' revision.
func Tag(build int) string { return strconv.Itoa(build) }

// Artifact is the repository of the run files: the image's, then -quadlets.
func Artifact(publish string) string { return publish + "-quadlets" }

// ArtifactType is the run files' artifact type, from the image's last name: application/vnd.<name>.quadlets.v1.
func ArtifactType(publish string) string {
	return "application/vnd." + path.Base(publish) + ".quadlets.v1"
}

// Source is the https URL of a GitHub remote, as the image's source names it: git@github.com:o/r.git and
// https://github.com/o/r.git both give https://github.com/o/r.
func Source(remote string) string {
	s := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	if rest, ok := strings.CutPrefix(s, "git@"); ok {
		host, p, _ := strings.Cut(rest, ":")
		return "https://" + host + "/" + p
	}
	if rest, ok := strings.CutPrefix(s, "ssh://git@"); ok {
		return "https://" + rest
	}
	return s
}

// Pin is the line a node's releases file takes for the run files: name, tag, repository, manifest digest.
func Pin(name, tag, artifact, digest string) string {
	return fmt.Sprintf("%s %s %s %s", name, tag, artifact, digest)
}

var version = regexp.MustCompile(`:VERSION$`)

// File is one run file: its name in the folder and its text.
type File struct {
	Name string
	Data []byte
}

// Stamp puts tag in place of the :VERSION that ends each Image= line of the .container files. A VERSION left anywhere
// after is an error that names its file and line, so a release names only the image it ships. The files come back
// sorted by name.
func Stamp(files []File, tag string) ([]File, error) {
	if len(files) == 0 {
		return nil, errors.New("the run files: the folder holds none")
	}
	stamped := make([]File, 0, len(files))
	for _, f := range files {
		lines := strings.Split(string(f.Data), "\n")
		for i, line := range lines {
			if strings.HasSuffix(f.Name, ".container") && strings.HasPrefix(strings.TrimSpace(line), "Image=") {
				lines[i] = version.ReplaceAllString(strings.TrimRight(line, " \t\r"), ":"+tag)
			}
			if strings.Contains(lines[i], "VERSION") {
				return nil, fmt.Errorf("the run files: %s line %d still holds VERSION: %s", f.Name, i+1, strings.TrimSpace(lines[i]))
			}
		}
		stamped = append(stamped, File{Name: f.Name, Data: []byte(strings.Join(lines, "\n"))})
	}
	sort.Slice(stamped, func(i, j int) bool { return stamped[i].Name < stamped[j].Name })
	return stamped, nil
}

// Pack is the files as a tar.gz that is the same bytes for the same files: sorted by name, owner and group 0, mode
// 0644, time 0, and a gzip header with no name and no time.
func Pack(files []File) ([]byte, error) {
	sorted := append([]File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(zw)
	for _, f := range sorted {
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     f.Name,
			Mode:     0o644,
			Size:     int64(len(f.Data)),
			ModTime:  time.Unix(0, 0),
			Format:   tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.Data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Pushed is the artifact a push made: its manifest's digest, and the manifest itself.
type Pushed struct {
	Digest   string
	Manifest []byte
}

// Package packs the artifact in store, and tags it: one layer, the run files, titled LayerTitle; the artifact type
// from publish; the source; the commit as its revision; and the commit's time as its created time, so the same commit
// packs the same manifest.
func Package(ctx context.Context, store *memory.Store, layer []byte, publish, source, revision string, created time.Time, tag string) (ocispec.Descriptor, error) {
	desc := content.NewDescriptorFromBytes(LayerMediaType, layer)
	desc.Annotations = map[string]string{ocispec.AnnotationTitle: LayerTitle}
	if err := store.Push(ctx, desc, bytes.NewReader(layer)); err != nil {
		return ocispec.Descriptor{}, err
	}
	annotations := map[string]string{ocispec.AnnotationCreated: created.UTC().Format(time.RFC3339), RevisionKey: revision}
	if source != "" {
		annotations[SourceKey] = source
	}
	manifest, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, ArtifactType(publish), oras.PackManifestOptions{
		Layers:              []ocispec.Descriptor{desc},
		ManifestAnnotations: annotations,
	})
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	return manifest, store.Tag(ctx, manifest, tag)
}

// PushArtifact packs the run files and pushes them to <publish>-quadlets:tag, with the login for its registry in
// authfile (podman's auth.json, the same format as docker's config).
func PushArtifact(ctx context.Context, files []File, publish, source, revision string, created time.Time, tag, authfile string) (Pushed, error) {
	layer, err := Pack(files)
	if err != nil {
		return Pushed{}, err
	}
	store := memory.New()
	desc, err := Package(ctx, store, layer, publish, source, revision, created, tag)
	if err != nil {
		return Pushed{}, err
	}
	repo, err := remote.NewRepository(Artifact(publish))
	if err != nil {
		return Pushed{}, err
	}
	repo.PlainHTTP = Plain(publish)
	creds, err := credentials.NewFileStore(authfile)
	if err != nil {
		return Pushed{}, fmt.Errorf("the push login %s: %w", authfile, err)
	}
	repo.Client = &auth.Client{Client: retry.DefaultClient, Cache: auth.NewCache(), Credential: credentials.Credential(creds)}
	if _, err := oras.Copy(ctx, store, tag, repo, tag, oras.DefaultCopyOptions); err != nil {
		return Pushed{}, err
	}
	manifest, err := content.FetchAll(ctx, store, desc)
	if err != nil {
		return Pushed{}, err
	}
	return Pushed{Digest: desc.Digest.String(), Manifest: manifest}, nil
}

// Plain says the registry of ref is this machine's own, reached over plain HTTP: localhost or 127.0.0.1, as docker
// treats them. Every other registry is reached over TLS.
func Plain(ref string) bool {
	host, _, _ := strings.Cut(ref, "/")
	host, _, _ = strings.Cut(host, ":")
	return host == "localhost" || host == "127.0.0.1"
}

// Login says authfile is there to push with: a file, not a folder. A missing one is the error the publish names.
func Login(authfile string) error {
	info, err := os.Stat(authfile)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no push login at %s: the registry's write token goes there (podman's auth.json format)", authfile)
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("the push login %s is a folder", authfile)
	}
	return nil
}

// AuthFile is the push login: $XDG_CONFIG_HOME/crate/push-auth.json, ~/.config when it is unset. Only the publish
// reads it; a check, a postsubmit and a workspace never mount it or see it.
func AuthFile() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = home + "/.config"
	}
	return dir + "/crate/push-auth.json"
}
