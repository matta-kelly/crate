// Package record keeps crate's record: one JSON line for each step of a run, appended to a file as the step ends,
// and one total line when the run ends. A run is one thing crate does to a system or a repo: a check, a postsubmit,
// a person's up, a mirror or a release. The gate's record is <bare>/crate/log.jsonl; a person's is
// $XDG_STATE_HOME/crate/log.jsonl. The record never fails a run: a line it cannot write is dropped.
package record

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The steps of a run, in the order a run takes them. A step that does not run writes no line.
const (
	Lock     = "lock"     // the wait for the repo's lock: the gate checks one push at a time
	Hold     = "hold"     // the wait for the system's slot on its machine
	Image    = "image"    // the dev image on this machine; its note says stored, pulled or built
	Services = "services" // the network and each service's pod
	Tools    = "tools"    // the tools member, made or started
	Setup    = "setup"    // [tools] setup, on a new tools member only
	Build    = "build"    // the built app's image, for a check or postsubmit
	Needs    = "needs"    // the proof that the built app refuses to start without its needs
	App      = "app"      // the app, started until healthy, and its routes
	Check    = "check"    // scripts/check.sh
	Revert   = "revert"   // postsubmit's revert of a red main
	Total    = "total"    // the whole run; its result is the run's
)

// The results of a step, and of a run.
const (
	Pass    = "pass"
	Fail    = "fail"
	Refused = "refused" // a push the gate refused before its check ran
)

// Line is one line of the record.
type Line struct {
	At      string  `json:"at"`
	Run     string  `json:"run"`
	Kind    string  `json:"kind"`
	Repo    string  `json:"repo,omitempty"`
	System  string  `json:"system,omitempty"`
	Commit  string  `json:"commit,omitempty"`
	Step    string  `json:"step"`
	Seconds float64 `json:"seconds"`
	Result  string  `json:"result"`
	Note    string  `json:"note,omitempty"`
}

// Run is one run, being recorded. A nil Run records nothing, so a caller with no record needs no stand-in.
type Run struct {
	path  string
	out   io.Writer
	base  Line
	start time.Time
	mu    sync.Mutex
	done  []Line
	ended bool
}

// Start begins a run that appends to path, and prints its summary to out when it ends; a nil out prints none.
// base gives the run's kind, repo, system and commit.
func Start(path string, out io.Writer, base Line) *Run {
	base.Run = id()
	return &Run{path: path, out: out, base: base, start: time.Now()}
}

// SetCommit names the commit the run is on, once it is known.
func (r *Run) SetCommit(commit string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.base.Commit = commit
	r.mu.Unlock()
}

// Step is one step of a run, timed from its start.
type Step struct {
	run   *Run
	name  string
	start time.Time
}

// Step starts the named step.
func (r *Run) Step(name string) *Step {
	if r == nil {
		return nil
	}
	return &Step{run: r, name: name, start: time.Now()}
}

// End writes the step's line: pass when err is nil, else fail with err as its note when note is "".
func (s *Step) End(err error, note string) {
	if s == nil {
		return
	}
	result := Pass
	if err != nil {
		result = Fail
		if note == "" {
			note = err.Error()
		}
	}
	s.run.write(s.name, time.Since(s.start), result, note)
}

// End writes the run's total line and prints its summary. A second End does nothing.
func (r *Run) End(result, note string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.ended {
		r.mu.Unlock()
		return
	}
	r.ended = true
	r.mu.Unlock()
	line := r.write(Total, time.Since(r.start), result, note)
	if r.out != nil {
		fmt.Fprintln(r.out, Summary(append(r.steps(), line)))
	}
}

func (r *Run) steps() []Line {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Line(nil), r.done...)
}

func (r *Run) write(step string, d time.Duration, result, note string) Line {
	r.mu.Lock()
	line := r.base
	r.mu.Unlock()
	line.At = time.Now().UTC().Format(time.RFC3339)
	line.Step = step
	line.Seconds = math.Round(d.Seconds()*10) / 10
	line.Result = result
	line.Note = firstLine(note)
	if step != Total {
		r.mu.Lock()
		r.done = append(r.done, line)
		r.mu.Unlock()
	}
	appendLine(r.path, line)
	return line
}

// Summary is a run's lines as one line of output, in the order the steps ended:
// "crate: hold 0s, image 2s (stored), setup 61s (fail); total 63s, fail".
func Summary(lines []Line) string {
	var steps []string
	total := ""
	for _, l := range lines {
		text := fmt.Sprintf("%s %ds", l.Step, int(math.Round(l.Seconds)))
		if l.Step == Total {
			total = text + ", " + l.Result
			continue
		}
		if l.Result == Fail {
			text += " (fail)"
		} else if l.Note != "" {
			text += " (" + l.Note + ")"
		}
		steps = append(steps, text)
	}
	out := "crate: " + strings.Join(steps, ", ")
	if total != "" {
		if len(steps) > 0 {
			out += "; "
		}
		out += total
	}
	return out
}

// Person is a person's record file: $XDG_STATE_HOME/crate/log.jsonl, ~/.local/state when it is unset.
func Person() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "crate", "log.jsonl")
}

// appendLine writes the line as one append, so two runs on one file never mix their lines. A path of "" writes
// nothing.
func appendLine(path string, line Line) {
	if path == "" {
		return
	}
	data, err := json.Marshal(line)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func id() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
