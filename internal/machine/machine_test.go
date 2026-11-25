package machine_test

// The machine file: crate's keys alone, [count] and [slice] memory. A key it does not know, the old cpus and memory
// among them, is refused. A kind outside the four is refused.

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

// The counts come from [count], the line from [slice] memory, else a quarter of the machine's memory, and the router's
// address from [router] address, else the loopback.
func TestTheMachineFileReadsTheCountsAndTheLine(t *testing.T) {
	counts := "[count]\ncheck = 2\npostsubmit = 1\nattempt = 4\n"
	rows := []struct {
		name   string
		body   string
		line   machine.Line
		router string
	}{
		{"counts alone", counts, machine.Line{Percent: 25}, "127.0.0.1"},
		{"a share", counts + "\n[slice]\nmemory = \"40%\"\n", machine.Line{Percent: 40}, "127.0.0.1"},
		{"a size", counts + "\n[slice]\nmemory = \"8G\"\n", machine.Line{Bytes: 8 << 30}, "127.0.0.1"},
		{"a router address", counts + "\n[router]\naddress = \"100.64.0.11\"\n", machine.Line{Percent: 25}, "100.64.0.11"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got, err := machine.Read(write(t, row.body))
			if err != nil || got == nil {
				t.Fatalf("expected the file, got %v, %v", got, err)
			}
			want := machine.Config{Count: machine.Count{Check: 2, Postsubmit: 1, Attempt: 4}, Line: row.line, RouterAddress: row.router}
			if !reflect.DeepEqual(*got, want) {
				t.Errorf("expected %+v, got %+v", want, *got)
			}
		})
	}
}

// A key crate does not know is refused with its name: the old cpus and memory, and a line that cannot be one.
func TestTheMachineFileRefusesAKeyOrALineItCannotUse(t *testing.T) {
	counts := "[count]\ncheck = 1\npostsubmit = 1\nattempt = 1\n"
	rows := []struct {
		name, body, holds string
	}{
		{"the old cpus", "cpus = \"2-15\"\n" + counts, "cpus"},
		{"the old memory", "memory = \"24G\"\n" + counts, "memory"},
		{"a slice key crate does not know", counts + "[slice]\ncpus = \"2\"\n", "slice.cpus"},
		{"a share of 0", counts + "[slice]\nmemory = \"0%\"\n", "0%"},
		{"a share past 100", counts + "[slice]\nmemory = \"150%\"\n", "150%"},
		{"a size with no unit", counts + "[slice]\nmemory = \"8\"\n", `"8"`},
		{"a size of 0", counts + "[slice]\nmemory = \"0G\"\n", "0G"},
		{"a word", counts + "[slice]\nmemory = \"lots\"\n", "lots"},
		{"a router address that is not an IP", counts + "[router]\naddress = \"mesh\"\n", "mesh"},
		{"a router key crate does not know", counts + "[router]\nport = 80\n", "router.port"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			_, err := machine.Read(write(t, row.body))
			if err == nil || !strings.Contains(err.Error(), row.holds) {
				t.Errorf("expected an error that holds %q, got %v", row.holds, err)
			}
		})
	}
}

// A line is its size, or its share of the machine's memory.
func TestALineIsItsSizeOrItsShareOfTheMemory(t *testing.T) {
	const total = 32 << 30
	for _, row := range []struct {
		line machine.Line
		want uint64
	}{
		{machine.Line{Percent: 25}, 8 << 30},
		{machine.Line{Percent: 100}, total},
		{machine.Line{Bytes: 3 << 30}, 3 << 30},
		{machine.DefaultLine, 8 << 30},
	} {
		if got := row.line.Of(total); got != row.want {
			t.Errorf("%+v of 32G: expected %d, got %d", row.line, row.want, got)
		}
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

// The window of a kind is its count in the machine file, and the defaults' 1 with no file.
func TestTheWindowIsTheKindsCount(t *testing.T) {
	t.Setenv("CRATE_MACHINE", write(t, "[count]\ncheck = 3\npostsubmit = 1\nattempt = 2\n"))
	if got := machine.Window(machine.Check); got != 3 {
		t.Errorf("check: expected 3, got %d", got)
	}
	t.Setenv("CRATE_MACHINE", filepath.Join(t.TempDir(), "absent.toml"))
	if got := machine.Window(machine.Check); got != 1 {
		t.Errorf("no file: expected 1, got %d", got)
	}
}

// crate's own lasting processes run in crate's slice itself, CRATE_SLICE's or crate's, and behind a remote engine in
// no slice of this machine.
func TestCratesOwnProcessesRunInItsSlice(t *testing.T) {
	t.Setenv("CONTAINER_HOST", "")
	t.Setenv("CRATE_SLICE", "")
	if got := machine.Own(); got != "crate.slice" {
		t.Errorf("with no CRATE_SLICE: expected crate.slice, got %q", got)
	}
	t.Setenv("CRATE_SLICE", "crate-test")
	if got := machine.Own(); got != "crate-test.slice" {
		t.Errorf("with CRATE_SLICE=crate-test: expected crate-test.slice, got %q", got)
	}
	t.Setenv("CONTAINER_HOST", "unix:///run/podman/podman.sock")
	if got := machine.Own(); got != "" {
		t.Errorf("behind a remote engine: expected none, got %q", got)
	}
}
