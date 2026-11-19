package ship

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
)

func TestStampPutsTheTagInEachImageLine(t *testing.T) {
	files, err := Stamp([]File{
		{"app.container", []byte("[Container]\nImage=ghcr.io/o/app:VERSION\nExec=serve\n")},
		{"app-migrate.container", []byte("[Container]\nImage=ghcr.io/o/app:VERSION  \n")},
		{"app.network", []byte("[Network]\n")},
	}, "sha-abc1234")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	if got := strings.Join(names, " "); got != "app-migrate.container app.container app.network" {
		t.Fatalf("files in order: %s", got)
	}
	if got := string(files[1].Data); got != "[Container]\nImage=ghcr.io/o/app:sha-abc1234\nExec=serve\n" {
		t.Fatalf("stamped: %q", got)
	}
	if got := string(files[0].Data); got != "[Container]\nImage=ghcr.io/o/app:sha-abc1234\n" {
		t.Fatalf("stamped, trailing space: %q", got)
	}
}

func TestStampRefusesAVersionLeftOver(t *testing.T) {
	for name, files := range map[string][]File{
		"in the middle of an image": {{"app.container", []byte("Image=ghcr.io/o/app:VERSION-x\n")}},
		"in another key":            {{"app.container", []byte("Image=ghcr.io/o/app:VERSION\nEnvironment=V=VERSION\n")}},
		"in a file that is no unit": {{"app.container", []byte("Image=x:VERSION\n")}, {"notes.txt", []byte("VERSION\n")}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Stamp(files, "sha-abc1234"); err == nil || !strings.Contains(err.Error(), "still holds VERSION") {
				t.Fatalf("expected a refusal that names VERSION, got %v", err)
			}
		})
	}
	if _, err := Stamp(nil, "sha-1"); err == nil {
		t.Fatal("no files: expected a refusal")
	}
}

func TestPackIsTheSameBytesForTheSameFiles(t *testing.T) {
	a, err := Pack([]File{{"b.container", []byte("B")}, {"a.container", []byte("A")}})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	b, err := Pack([]File{{"a.container", []byte("A")}, {"b.container", []byte("B")}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("two packs of the same files differ")
	}
	zr, err := gzip.NewReader(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	if zr.Name != "" || !zr.ModTime.IsZero() {
		t.Fatalf("the gzip header has a name %q or a time %v", zr.Name, zr.ModTime)
	}
	tr := tar.NewReader(zr)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" || h.ModTime.Unix() != 0 || h.Mode != 0o644 {
			t.Fatalf("%s: uid %d gid %d uname %q gname %q time %v mode %o", h.Name, h.Uid, h.Gid, h.Uname, h.Gname, h.ModTime, h.Mode)
		}
		names = append(names, h.Name)
	}
	if strings.Join(names, " ") != "a.container b.container" {
		t.Fatalf("names: %v", names)
	}
}

// The node's side of the contract is monkeydo's lanes/releases: fetchArtifact fetches the manifest by its pinned
// digest as application/vnd.oci.image.manifest.v1+json, and needs one layer or more, each with a sha256 digest, saved
// by its org.opencontainers.image.title; the unpack in releases.go opens a layer whose title ends in .tar.gz. This
// test holds the manifest to exactly those fields, and to the rest of the shape the brief names.
func TestTheManifestIsTheShapeMonkeydoFetches(t *testing.T) {
	ctx := context.Background()
	layer, err := Pack([]File{{"app.container", []byte("Image=x:sha-1\n")}})
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 11, 18, 12, 0, 0, 0, time.FixedZone("x", -8*3600))
	store := memory.New()
	desc, err := Package(ctx, store, layer, "ghcr.io/o/landl-dash", "https://github.com/o/landl-dash", created, "sha-1")
	if err != nil {
		t.Fatal(err)
	}
	data, err := content.FetchAll(ctx, store, desc)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if desc.Digest.String() != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatal("the pin's digest is not the manifest's bytes'")
	}
	var m struct {
		MediaType    string `json:"mediaType"`
		ArtifactType string `json:"artifactType"`
		Config       struct {
			MediaType string `json:"mediaType"`
		} `json:"config"`
		Layers []struct {
			MediaType   string            `json:"mediaType"`
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
		} `json:"layers"`
		Annotations map[string]string `json:"annotations"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	// monkeydo's contract.
	if m.MediaType != "application/vnd.oci.image.manifest.v1+json" {
		t.Errorf("mediaType %q", m.MediaType)
	}
	if len(m.Layers) != 1 {
		t.Fatalf("%d layers, want 1", len(m.Layers))
	}
	lsum := sha256.Sum256(layer)
	if m.Layers[0].Digest != "sha256:"+hex.EncodeToString(lsum[:]) {
		t.Errorf("the layer's digest %q is not its bytes' sha256", m.Layers[0].Digest)
	}
	if title := m.Layers[0].Annotations["org.opencontainers.image.title"]; title != "quadlets.tar.gz" {
		t.Errorf("the layer's title %q", title)
	}
	// The rest of the shape.
	if m.ArtifactType != "application/vnd.landl-dash.quadlets.v1" {
		t.Errorf("artifactType %q", m.ArtifactType)
	}
	if m.Layers[0].MediaType != "application/vnd.oci.image.layer.v1.tar+gzip" {
		t.Errorf("the layer's media type %q", m.Layers[0].MediaType)
	}
	if m.Config.MediaType != "application/vnd.oci.empty.v1+json" {
		t.Errorf("config media type %q", m.Config.MediaType)
	}
	if m.Annotations["org.opencontainers.image.source"] != "https://github.com/o/landl-dash" {
		t.Errorf("source %q", m.Annotations["org.opencontainers.image.source"])
	}
	if m.Annotations["org.opencontainers.image.created"] != "2026-11-18T20:00:00Z" {
		t.Errorf("created %q", m.Annotations["org.opencontainers.image.created"])
	}
	// The same commit packs the same manifest.
	again, err := Package(ctx, memory.New(), layer, "ghcr.io/o/landl-dash", "https://github.com/o/landl-dash", created, "sha-1")
	if err != nil {
		t.Fatal(err)
	}
	if again.Digest != desc.Digest {
		t.Error("two packs of the same commit differ")
	}
}

func TestTheNamesAndThePin(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:matta-kelly/landl-dash.git":     "https://github.com/matta-kelly/landl-dash",
		"https://github.com/matta-kelly/landl-dash.git": "https://github.com/matta-kelly/landl-dash",
		"ssh://git@github.com/matta-kelly/landl-dash":   "https://github.com/matta-kelly/landl-dash",
	} {
		if got := Source(in); got != want {
			t.Errorf("Source(%q) = %q", in, got)
		}
	}
	if Tag("0123456789abcdef") != "sha-0123456" {
		t.Error(Tag("0123456789abcdef"))
	}
	if Artifact("ghcr.io/o/x") != "ghcr.io/o/x-quadlets" || ArtifactType("ghcr.io/o/x") != "application/vnd.x.quadlets.v1" {
		t.Error("artifact names")
	}
	if got := Pin("x", "sha-0123456", "ghcr.io/o/x-quadlets", "sha256:ab"); got != "x sha-0123456 ghcr.io/o/x-quadlets sha256:ab" {
		t.Error(got)
	}
	if !Plain("localhost:5000/x") || !Plain("127.0.0.1:5000/x") || Plain("ghcr.io/o/x") {
		t.Error("Plain")
	}
}
