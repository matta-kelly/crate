package devimage_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matta-kelly/crate/internal/devimage"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return string(b)
}

// Of names the image and the hash Local makes, so the cache and the check speak of one tag.
func TestOfNamesTheTagLocalMakes(t *testing.T) {
	root, _ := given(t)
	o := devimage.Options{Root: root, Stderr: &bytes.Buffer{}}
	image, hash, err := devimage.Of(o)
	if err != nil {
		t.Fatal(err)
	}
	tag, _, err := devimage.Local(context.Background(), o, "")
	if err != nil || tag != image+":"+hash {
		t.Errorf("expected Local's tag %s:%s, got %q, %v", image, hash, tag, err)
	}
}

// Held lists the hashes of the image in the store, and nothing else: no candidate, no other tag, no other image.
func TestHeldListsTheImagesHashesAlone(t *testing.T) {
	_, _ = given(t)
	image := "ghcr.io/matta-kelly/que-pasa-dev"
	local := strings.Join([]string{
		image + ":0123456789abcdef",
		image + ":0123456789abcdef-candidate",
		image + ":main",
		"ghcr.io/matta-kelly/other-dev:fedcba9876543210",
	}, "\n") + "\n"
	must(t, os.WriteFile(filepath.Join(os.Getenv("T"), "local"), []byte(local), 0o644))
	got, err := devimage.Held(image)
	if err != nil || !slices.Equal(got, []string{"0123456789abcdef"}) {
		t.Errorf("expected [0123456789abcdef], got %v, %v", got, err)
	}
}

// Push sends the image by its hash, and Point sends it again as :main, each with the login it is given and with podman
// alone: no skopeo on the machine that writes.
func TestPushAndPointSendTheImageWithPodmanAlone(t *testing.T) {
	_, calls := given(t)
	image, hash, auth := "ghcr.io/matta-kelly/que-pasa-dev", "0123456789abcdef", "/run/push-auth.json"
	must(t, devimage.Push(image, hash, auth, &bytes.Buffer{}))
	must(t, devimage.Point(image, hash, auth, &bytes.Buffer{}))
	want := "podman push --quiet --authfile " + auth + " " + image + ":" + hash + " " + image + ":" + hash + "\n" +
		"podman push --quiet --authfile " + auth + " " + image + ":" + hash + " " + image + ":main\n"
	if got := read(t, calls); got != want {
		t.Errorf("the calls: expected\n%s\ngot\n%s", want, got)
	}
}

// A registry on this machine is spoken to over plain HTTP; any other over TLS.
func TestALocalRegistryIsPlainHTTP(t *testing.T) {
	_, calls := given(t)
	must(t, devimage.Push("localhost:5000/o/x-dev", "0123456789abcdef", "", &bytes.Buffer{}))
	must(t, devimage.Push("ghcr.io/o/x-dev", "0123456789abcdef", "", &bytes.Buffer{}))
	for _, l := range strings.Split(read(t, calls), "\n") {
		plain := strings.Contains(l, "--tls-verify=false")
		if strings.Contains(l, "localhost:5000") != plain {
			t.Errorf("expected --tls-verify=false only for localhost: %q", l)
		}
	}
}

// A registry with a port keeps it: the tag is what follows the last colon.
func TestHeldKeepsARegistrysPort(t *testing.T) {
	_, _ = given(t)
	image := "localhost:5000/o/x-dev"
	must(t, os.WriteFile(filepath.Join(os.Getenv("T"), "local"), []byte(image+":0123456789abcdef\n"), 0o644))
	got, err := devimage.Held(image)
	if err != nil || !slices.Equal(got, []string{"0123456789abcdef"}) {
		t.Errorf("expected [0123456789abcdef], got %v, %v", got, err)
	}
}

// A store that cannot be listed is an error that carries podman's own message, so its cause is read, not guessed.
func TestHeldFailsWithPodmansMessage(t *testing.T) {
	_, _ = given(t)
	t.Setenv("IMAGES_ERR", "Error: cannot open the store")
	if _, err := devimage.Held("ghcr.io/o/x-dev"); err == nil || !strings.Contains(err.Error(), "cannot open the store") {
		t.Errorf("expected an error with podman's message, got %v", err)
	}
}
