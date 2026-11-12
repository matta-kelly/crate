package main_test

// The binary's part of the machine: a remote engine and a machine with no file are said on stderr, once.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestARemoteEngineSaysOnceThatItPlacesNothing(t *testing.T) {
	f := newFx(t, "que-pasa")
	slice := testSliceName()
	stopUserSlices(t, slice)
	file := filepath.Join(f.dir, "machine.toml")
	must(t, os.WriteFile(file, []byte("cpus = \"0-1\"\nmemory = \"512M\"\n\n[count]\ncheck = 1\npostsubmit = 1\nattempt = 1\n"), 0o644))
	r := f.dev([]string{"CRATE_MACHINE=" + file, "CRATE_SLICE=" + slice, "CONTAINER_HOST=unix:///run/user/1000/podman/podman.sock"}, "ensure")
	if r.code != 0 {
		t.Fatalf("ensure with CONTAINER_HOST and a machine file: expected exit 0, got %s", r)
	}
	calls := f.calls()
	if strings.Contains(calls, "--cgroup-parent") {
		t.Errorf("the calls: expected no placement on a remote engine, got\n%s", calls)
	}
	if strings.Contains(calls, "FORBIDDEN") {
		t.Errorf("the calls: expected no systemd or user manager call for a remote engine, got\n%s", calls)
	}
	reasons := 0
	for _, line := range lines(r.err) {
		if strings.HasPrefix(line, "crate:") {
			reasons++
		}
	}
	if reasons != 1 {
		t.Errorf("stderr: expected one `crate:` line about the remote engine, got %d: %q", reasons, r.err)
	}
}
