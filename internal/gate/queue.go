package gate

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/matta-kelly/crate/internal/record"
)

// The queue: the changes waiting to land on main, in order. A change is a commit, held by its ref refs/queue/<sha>
// until its verdict, so its objects stay. Its state is in queue.json, which only a holder of queue.lock writes.

// Entry is one change in the queue.
type Entry struct {
	Sha     string    `json:"sha"`
	At      time.Time `json:"at"`               // when it entered
	Started time.Time `json:"started,omitzero"` // when a run first took it
	Limit   int       `json:"limit,omitempty"`  // the most changes it may be checked with, after a bisect; 0 for the repo's batch
	Slot    *int      `json:"slot,omitempty"`   // the run checking it now; none while it waits
}

// The two shapes of a change's last line: its verdict.
const (
	landedPrefix    = "crate: landed "
	notLandedPrefix = "crate: not landed: "
)

func queueRef(sha string) string { return "refs/queue/" + sha }

func (r *Repo) entryLog(sha string) string { return r.state("queue", sha+".log") }

// readQueue reads queue.json; none is an empty queue.
func (r *Repo) readQueue() ([]Entry, error) {
	data, err := os.ReadFile(r.state("queue.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var q []Entry
	if err := json.Unmarshal(data, &q); err != nil {
		return nil, fmt.Errorf("%s: %w", r.state("queue.json"), err)
	}
	return q, nil
}

// changeQueue changes the queue under queue.lock and writes it back whole, by a rename, so a reader never sees half.
func (r *Repo) changeQueue(change func([]Entry) ([]Entry, error)) error {
	unlock, err := r.lock("queue.lock")
	if err != nil {
		return err
	}
	defer unlock()
	q, err := r.readQueue()
	if err != nil {
		return err
	}
	if q, err = change(q); err != nil {
		return err
	}
	data, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.state("queue.json.new")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.state("queue.json"))
}

func position(q []Entry, sha string) int {
	return slices.IndexFunc(q, func(e Entry) bool { return e.Sha == sha })
}

// Enter puts sha at the back of the queue, or at its front when jump, and says its place, from 1. Its ref must hold
// it already. A change in the queue already keeps its place.
func (r *Repo) Enter(sha string, jump bool) (int, error) {
	if err := os.MkdirAll(r.state("queue"), 0o755); err != nil {
		return 0, err
	}
	place := 0
	err := r.changeQueue(func(q []Entry) ([]Entry, error) {
		if i := position(q, sha); i >= 0 {
			place = i + 1
			return q, nil
		}
		if err := os.WriteFile(r.entryLog(sha), nil, 0o644); err != nil {
			return nil, err
		}
		e := Entry{Sha: sha, At: time.Now().UTC()}
		if jump {
			e.Limit = 1
			place = 1
			return append([]Entry{e}, q...), nil
		}
		place = len(q) + 1
		return append(q, e), nil
	})
	return place, err
}

// Jump puts a change already in the queue first, to be checked alone. Every run starts again behind it.
func (r *Repo) Jump(sha string) error {
	return r.changeQueue(func(q []Entry) ([]Entry, error) {
		i := position(q, sha)
		if i < 0 {
			return nil, fmt.Errorf("%s is not in the queue", short(sha))
		}
		e := q[i]
		e.Limit = 1
		return append([]Entry{e}, slices.Delete(q, i, i+1)...), nil
	})
}

// Leave takes sha out of the queue with no land: its verdict says why.
func (r *Repo) Leave(sha, why string) error {
	var gone *Entry
	err := r.changeQueue(func(q []Entry) ([]Entry, error) {
		if i := position(q, sha); i >= 0 {
			e := q[i]
			gone = &e
			q = slices.Delete(q, i, i+1)
		}
		return q, nil
	})
	if err != nil || gone == nil {
		return err
	}
	r.verdict(*gone, false, why)
	return nil
}

// verdict ends a change: its last line, its ref gone, and its land run on the record: the wait from entering to its
// first run, and the whole of it.
func (r *Repo) verdict(e Entry, landed bool, text string) {
	line, result := notLandedPrefix+text, record.Fail
	if landed {
		line, result = landedPrefix+short(e.Sha)+" as "+text, record.Pass
	}
	appendTo(r.entryLog(e.Sha), line)
	_, _ = r.git("update-ref", "-d", queueRef(e.Sha))
	path := r.state("log.jsonl")
	run := record.Line{Run: record.ID(), Kind: "land", Repo: r.Name(), Commit: e.Sha}
	if !e.Started.IsZero() {
		record.Append(path, run, record.Wait, e.Started.Sub(e.At), record.Pass, "")
	}
	record.Append(path, run, record.Total, time.Since(e.At), result, text)
}

// Wait prints the change's output as it comes, until its verdict, and says whether it landed. While it waits, it
// starts the repo's worker when none runs, so a worker that stopped does not leave it waiting.
func (r *Repo) Wait(sha string, out io.Writer) (bool, error) {
	f, err := os.Open(r.entryLog(sha))
	if err != nil {
		return false, err
	}
	defer f.Close()
	read := bufio.NewReader(f)
	var partial strings.Builder
	checked := time.Now()
	for {
		chunk, err := read.ReadString('\n')
		partial.WriteString(chunk)
		if err == nil {
			line := partial.String()
			partial.Reset()
			fmt.Fprint(out, line)
			switch {
			case strings.HasPrefix(line, landedPrefix):
				return true, nil
			case strings.HasPrefix(line, notLandedPrefix):
				return false, nil
			}
			continue
		}
		if !errors.Is(err, io.EOF) {
			return false, err
		}
		if time.Since(checked) > 5*time.Second {
			checked = time.Now()
			if err := r.StartWorker(); err != nil {
				return false, err
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Queue writes the queue as a person reads it: each change in order, what it is doing and for how long.
func (r *Repo) Queue(out io.Writer) error {
	q, err := r.readQueue()
	if err != nil {
		return err
	}
	if len(q) == 0 {
		fmt.Fprintln(out, "the queue is empty")
		return nil
	}
	for i, e := range q {
		what := "waiting " + since(e.At)
		if e.Slot != nil {
			what = fmt.Sprintf("checking in run %d, %s in all", *e.Slot, since(e.At))
		}
		subject, _ := r.git("log", "-1", "--format=%s", e.Sha)
		fmt.Fprintf(out, "%d  %s  %s  %s\n", i+1, short(e.Sha), what, subject)
	}
	return nil
}

func since(t time.Time) string { return time.Since(t).Round(time.Second).String() }

// Resolve is the full sha of a change in the queue, from its sha or a prefix of it.
func (r *Repo) Resolve(prefix string) (string, error) {
	q, err := r.readQueue()
	if err != nil {
		return "", err
	}
	var found []string
	for _, e := range q {
		if strings.HasPrefix(e.Sha, prefix) {
			found = append(found, e.Sha)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("no change in the queue is %s", prefix)
	default:
		return "", fmt.Errorf("%s names %d changes in the queue: give more of it", prefix, len(found))
	}
}

// entryLogs is one writer to the logs of the changes, and the func that closes them. A log it cannot open is skipped.
func (r *Repo) entryLogs(shas []string) (io.Writer, func()) {
	var files []*os.File
	var writers []io.Writer
	for _, sha := range shas {
		if f, err := os.OpenFile(r.entryLog(sha), os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			files = append(files, f)
			writers = append(writers, f)
		}
	}
	return io.MultiWriter(writers...), func() {
		for _, f := range files {
			f.Close()
		}
	}
}
