package machine_test

// The machine file: [count] is what crate reads, and cpus and memory from the old form are read and dropped.
// A kind outside the four is refused.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/matta-kelly/crate/internal/machine"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "machine.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The counts come from [count], whether or not the file still holds the old cpus and memory.
func TestTheMachineFileReadsTheCountsAndDropsTheRest(t *testing.T) {
	rows := []struct {
		name string
		body string
	}{
		{"counts alone", "[count]\ncheck = 2\npostsubmit = 1\nattempt = 4\n"},
		{"counts beside a cpus and memory of the old form",
			"cpus = \"2-15\"\nmemory = \"24G\"\n\n[count]\ncheck = 2\npostsubmit = 1\nattempt = 4\n"},
		{"counts beside a cpus and memory the old form refused",
			"cpus = \"all\"\nmemory = \"lots\"\n\n[count]\ncheck = 2\npostsubmit = 1\nattempt = 4\n"},
	}
	want := machine.Config{Count: machine.Count{Check: 2, Postsubmit: 1, Attempt: 4}}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got, err := machine.Read(write(t, row.body))
			if err != nil || got == nil {
				t.Fatalf("expected the file, got %v, %v", got, err)
			}
			if !reflect.DeepEqual(*got, want) {
				t.Errorf("expected %+v, got %+v", want, *got)
			}
		})
	}
}

// A path with no file gives none and no error.
func TestAMachineFileThatIsNotThereIsNone(t *testing.T) {
	got, err := machine.Read(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil || got != nil {
		t.Errorf("a path with no file: expected none and no error, got %v, %v", got, err)
	}
}

// A count crate cannot use is refused with the key and the value.
func TestTheMachineFileRefusesACountItCannotUse(t *testing.T) {
	rows := []struct {
		name, body, key, value string
	}{
		{"a check count of 0", "[count]\ncheck = 0\npostsubmit = 1\nattempt = 1\n", "check", "0"},
		{"a postsubmit count below 0", "[count]\ncheck = 1\npostsubmit = -2\nattempt = 1\n", "postsubmit", "-2"},
		{"an attempt count not whole", "[count]\ncheck = 1\npostsubmit = 1\nattempt = 1.5\n", "attempt", "1.5"},
		{"a count that is a string", "[count]\ncheck = \"two\"\npostsubmit = 1\nattempt = 1\n", "check", "two"},
		{"a count crate does not know", "[count]\ncheck = 1\npostsubmit = 1\nattempt = 1\nbuild = 2\n", "build", ""},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			_, err := machine.Read(write(t, row.body))
			if err == nil || !strings.Contains(err.Error(), row.key) {
				t.Errorf("expected an error that holds the key %q, got %v", row.key, err)
				return
			}
			if row.value != "" && !strings.Contains(err.Error(), row.value) {
				t.Errorf("expected an error that holds the value %q, got %v", row.value, err)
			}
		})
	}
}

// A machine file that cannot be read is a failure that names the file.
func TestAMachineFileThatCannotBeReadNamesTheFile(t *testing.T) {
	dir := t.TempDir()
	notToml := write(t, "this is not toml")
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

// A kind outside the four is an error that names it.
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
