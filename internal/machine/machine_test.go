package machine_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
)

var kinds = []string{"work", "check", "postsubmit", "attempt"}

// Here the default machine places nothing and holds nothing: one system that calls Hold twice and Place three
// times gets one slot and never waits on itself. With no machine file, it holds nothing at all.
func TestTheDefaultMachinePlacesNothingAndHoldsNothing(t *testing.T) {
	t.Setenv("CRATE_MACHINE", filepath.Join(t.TempDir(), "no-machine.toml"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			m := machine.Default()
			done := make(chan struct{})
			go func() {
				defer close(done)
				first, err := m.Hold(kind, "que-pasa-main")
				if err != nil || first == nil {
					t.Errorf("Hold(%q): expected a slot and no error, got %v, %v", kind, first, err)
					return
				}
				second, err := m.Hold(kind, "que-pasa-main")
				if err != nil || second == nil {
					t.Errorf("a second Hold(%q) for the same system: expected a slot and no error, got %v, %v", kind, second, err)
					return
				}
				first.Bind()
				second.Release()
				if entries, err := os.ReadDir(filepath.Join(os.Getenv("XDG_STATE_HOME"), "crate", "slots")); err == nil && len(entries) > 0 {
					t.Errorf("a machine with no file: expected no slots, got %v", entries)
				}
				for i := 0; i < 3; i++ {
					if p, err := m.Place(kind, "que-pasa-main"); err != nil || p != "" {
						t.Errorf("Place(%q) %d: expected no placement and no error, got %q, %v", kind, i+1, p, err)
					}
				}
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("a system that holds a slot twice waited on itself")
			}
		})
	}
}

func TestAKindOutsideTheFourIsAnError(t *testing.T) {
	for _, kind := range []string{"", "bogus", "Work", "checks"} {
		t.Run(kind, func(t *testing.T) {
			m := machine.Default()
			_, holdErr := m.Hold(kind, "s")
			if holdErr == nil || !strings.Contains(holdErr.Error(), kind) {
				t.Errorf("Hold(%q): expected an error that names the kind, got %v", kind, holdErr)
			}
			_, placeErr := m.Place(kind, "s")
			if placeErr == nil || !strings.Contains(placeErr.Error(), kind) {
				t.Errorf("Place(%q): expected an error that names the kind, got %v", kind, placeErr)
			}
		})
	}
}

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "machine.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A path with no file gives none and no error, beside a file that is there.
func TestTheMachineFileReadsItsKeys(t *testing.T) {
	t.Cleanup(func() {
		if got, err := machine.Read(filepath.Join(t.TempDir(), "absent.toml")); err != nil || got != nil {
			t.Errorf("a path with no file: expected none and no error, got %v, %v", got, err)
		}
	})
	rows := []struct {
		name string
		body string
		want machine.Config
	}{
		{"a range, gigabytes and counts",
			"cpus = \"2-15\"\nmemory = \"24G\"\n\n[count]\ncheck = 2\npostsubmit = 1\nattempt = 4\n",
			machine.Config{CPUs: "2-15", Memory: "24G", Count: machine.Count{Check: 2, Postsubmit: 1, Attempt: 4}}},
		{"a list with a range, megabytes",
			"cpus = \"0,2-5\"\nmemory = \"23552M\"\n\n[count]\ncheck = 1\npostsubmit = 1\nattempt = 1\n",
			machine.Config{CPUs: "0,2-5", Memory: "23552M", Count: machine.Count{Check: 1, Postsubmit: 1, Attempt: 1}}},
		{"one cpu, kilobytes",
			"cpus = \"3\"\nmemory = \"1048576K\"\n\n[count]\ncheck = 10\npostsubmit = 10\nattempt = 10\n",
			machine.Config{CPUs: "3", Memory: "1048576K", Count: machine.Count{Check: 10, Postsubmit: 10, Attempt: 10}}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got, err := machine.Read(write(t, row.body))
			if err != nil || got == nil {
				t.Fatalf("expected the file, got %v, %v", got, err)
			}
			if !reflect.DeepEqual(*got, row.want) {
				t.Errorf("expected %+v, got %+v", row.want, *got)
			}
		})
	}
}

func TestTheMachineFileRefusesAnyOtherFormWithTheKeyAndTheValue(t *testing.T) {
	const counts = "\n[count]\ncheck = 1\npostsubmit = 1\nattempt = 1\n"
	rows := []struct {
		name, body, key, value string
	}{
		{"a word for cpus", "cpus = \"all\"\nmemory = \"24G\"\n" + counts, "cpus", "all"},
		{"a range with no end", "cpus = \"2-\"\nmemory = \"24G\"\n" + counts, "cpus", "2-"},
		{"a cpu list with a space", "cpus = \"0, 2\"\nmemory = \"24G\"\n" + counts, "cpus", "0, 2"},
		{"memory with no suffix", "cpus = \"2-15\"\nmemory = \"24\"\n" + counts, "memory", "24"},
		{"memory with another suffix", "cpus = \"2-15\"\nmemory = \"24T\"\n" + counts, "memory", "24T"},
		{"memory not whole", "cpus = \"2-15\"\nmemory = \"1.5G\"\n" + counts, "memory", "1.5G"},
		{"memory negative", "cpus = \"2-15\"\nmemory = \"-4G\"\n" + counts, "memory", "-4G"},
		{"a check count of 0", "cpus = \"2-15\"\nmemory = \"24G\"\n\n[count]\ncheck = 0\npostsubmit = 1\nattempt = 1\n", "check", "0"},
		{"a postsubmit count below 0", "cpus = \"2-15\"\nmemory = \"24G\"\n\n[count]\ncheck = 1\npostsubmit = -2\nattempt = 1\n", "postsubmit", "-2"},
		{"an attempt count not whole", "cpus = \"2-15\"\nmemory = \"24G\"\n\n[count]\ncheck = 1\npostsubmit = 1\nattempt = 1.5\n", "attempt", "1.5"},
		{"a count that is a string", "cpus = \"2-15\"\nmemory = \"24G\"\n\n[count]\ncheck = \"two\"\npostsubmit = 1\nattempt = 1\n", "check", "two"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			_, err := machine.Read(write(t, row.body))
			if err == nil || !strings.Contains(err.Error(), row.key) || !strings.Contains(err.Error(), row.value) {
				t.Errorf("expected an error that holds the key %q and the value %q, got %v", row.key, row.value, err)
			}
		})
	}
}

func TestAMachineFileThatCannotBeReadIsAFailureThatNamesTheFile(t *testing.T) {
	dir := t.TempDir()
	notToml := write(t, "cpus = \"2-15\nthis is not toml")
	asDir := filepath.Join(dir, "machine.toml")
	if err := os.Mkdir(asDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"text that does not parse": notToml, "a folder where the file should be": asDir} {
		got, err := machine.Read(path)
		if err == nil || got != nil || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: expected an error that names %s and no machine, got %v, %v", name, path, got, err)
		}
	}
}
