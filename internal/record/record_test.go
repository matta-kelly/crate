package record

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, path string) []Line {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []Line
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		var l Line
		if err := json.Unmarshal(scan.Bytes(), &l); err != nil {
			t.Fatalf("not a JSON line: %q: %v", scan.Text(), err)
		}
		lines = append(lines, l)
	}
	return lines
}

func TestStepsAndTotalInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crate", "log.jsonl")
	var out bytes.Buffer
	r := Start(path, &out, Line{Kind: "check", Repo: "que-pasa", System: "crate-que-pasa-check"})
	r.SetCommit("5859002abcde")
	r.Step(Hold).End(nil, "")
	r.Step(Image).End(nil, "stored")
	r.Step(Setup).End(errors.New("npm ci: exit 1\nmore"), "")
	r.End(Fail, "")
	r.End(Pass, "") // a second End does nothing

	lines := read(t, path)
	var steps []string
	for _, l := range lines {
		steps = append(steps, l.Step+":"+l.Result)
		if l.Run != lines[0].Run || l.Kind != "check" || l.Repo != "que-pasa" || l.Commit != "5859002abcde" || l.At == "" {
			t.Errorf("a line lost the run's fields: %+v", l)
		}
	}
	if got := strings.Join(steps, " "); got != "hold:pass image:pass setup:fail total:fail" {
		t.Fatalf("steps = %q", got)
	}
	if lines[1].Note != "stored" || lines[2].Note != "npm ci: exit 1" {
		t.Errorf("notes = %q, %q", lines[1].Note, lines[2].Note)
	}
	if got := strings.TrimSpace(out.String()); got != "crate: hold 0s, image 0s (stored), setup 0s (fail); total 0s, fail" {
		t.Errorf("summary = %q", got)
	}
}

func TestEachRunHasItsOwnID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	Start(path, nil, Line{Kind: "mirror"}).End(Pass, "")
	Start(path, nil, Line{Kind: "mirror"}).End(Pass, "")
	lines := read(t, path)
	if len(lines) != 2 || lines[0].Run == lines[1].Run {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestNilRunRecordsNothing(t *testing.T) {
	var r *Run
	r.SetCommit("x")
	r.Step(Hold).End(errors.New("no"), "")
	r.End(Pass, "")
}

func TestUnwritablePathIsDropped(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r := Start(filepath.Join(blocker, "log.jsonl"), &out, Line{Kind: "work"})
	r.Step(Tools).End(nil, "")
	r.End(Pass, "")
	if !strings.Contains(out.String(), "tools 0s; total 0s, pass") {
		t.Errorf("the summary should print with no file: %q", out.String())
	}
}

func TestSummaryWithNoSteps(t *testing.T) {
	if got := Summary([]Line{{Step: Total, Seconds: 2.6, Result: Fail}}); got != "crate: total 3s, fail" {
		t.Errorf("summary = %q", got)
	}
}

func TestPerson(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/s")
	if got := Person(); got != "/s/crate/log.jsonl" {
		t.Errorf("Person() = %q", got)
	}
}
