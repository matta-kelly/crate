package gate

// The worker on real git with a stand-in check: a tree fails when it holds a file named red, a file named slow makes
// its check take a while, and a file named sick makes crate unable to check it (an error, not a fail) while the
// fixture's sick count lasts, or for ever when lonely is set. Each case is a path through the queue.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/fault"
	"github.com/matta-kelly/crate/internal/machine"
)

type fixture struct {
	t      *testing.T
	repo   *Repo
	client string
	out    bytes.Buffer

	running, most atomic.Int32
	sick          atomic.Int32 // how many more checks of a sick tree crate cannot do
	lonely        bool         // a sick tree errs for ever, on a machine that is otherwise well
	mu            sync.Mutex
	checked       []string // each run's changes, as their subjects, in the order the runs started
}

func newFixture(t *testing.T, decl string) *fixture {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // the machine's health is the test's own
	dir := t.TempDir()
	f := &fixture{t: t, client: filepath.Join(dir, "client")}
	bare := filepath.Join(dir, "repo.git")
	f.git(dir, "init", "-q", "--bare", "-b", "main", bare)
	f.git(dir, "init", "-q", "-b", "main", f.client)
	f.write("crate.toml", decl)
	f.write("file", "one\n")
	f.git(f.client, "add", "-A")
	f.git(f.client, "commit", "-qm", "base")
	f.git(f.client, "push", "-q", bare, "main")
	r, err := Open(bare)
	if err != nil {
		t.Fatal(err)
	}
	f.repo = r
	return f
}

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *fixture) write(path, body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.client, path), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// change makes a commit on main as the client last fetched it, with the files given, and puts it in the queue.
func (f *fixture) change(subject string, files map[string]string) string {
	f.t.Helper()
	f.git(f.client, "checkout", "-q", "--detach", "main")
	for path, body := range files {
		f.write(path, body)
	}
	f.git(f.client, "add", "-A")
	f.git(f.client, "commit", "-qm", subject)
	sha := f.git(f.client, "rev-parse", "HEAD")
	f.git(f.client, "push", "-q", f.repo.Bare, "HEAD:"+queueRef(sha))
	if _, err := f.repo.Enter(sha, false); err != nil {
		f.t.Fatal(err)
	}
	return sha
}

// check is the stand-in: red fails, slow takes a while, and it counts how many run at once.
func (f *fixture) check(ctx context.Context, rn *run) error {
	n := f.running.Add(1)
	defer f.running.Add(-1)
	for m := f.most.Load(); n > m && !f.most.CompareAndSwap(m, n); m = f.most.Load() {
	}
	dir := f.repo.state("runs", itoa(rn.slot))
	subjects, _ := work(dir, "log", "--reverse", "--format=%s", rn.base+".."+rn.tip)
	f.mu.Lock()
	f.checked = append(f.checked, strings.ReplaceAll(subjects, "\n", ","))
	f.mu.Unlock()
	if _, err := os.Stat(filepath.Join(dir, "slow")); err == nil {
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	} else {
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "red")); err == nil {
		return fault.Change(ErrRefused)
	}
	if _, err := os.Stat(filepath.Join(dir, "sick")); err == nil {
		if f.lonely {
			_ = machine.Passed() // the rest of the machine passes between its runs
			return errors.New("podman exec exited 125: podman could not run scripts/check.sh")
		}
		if f.sick.Add(-1) >= 0 {
			return errors.New("podman images did not finish within 30s")
		}
	}
	return nil
}

func (f *fixture) work(window int) {
	f.t.Helper()
	done := make(chan error, 1)
	go func() { done <- f.repo.work(window, f.check, &f.out) }()
	select {
	case err := <-done:
		if err != nil {
			f.t.Fatalf("work: %v\n%s", err, f.out.String())
		}
	case <-time.After(60 * time.Second):
		f.t.Fatalf("the worker did not finish\n%s", f.out.String())
	}
}

func (f *fixture) landed() string {
	out := f.git(f.repo.Bare, "log", "--reverse", "--format=%s", "main")
	return strings.ReplaceAll(strings.TrimPrefix(out, "base\n"), "\n", ",")
}

func (f *fixture) verdict(sha string) string {
	data, _ := os.ReadFile(f.repo.entryLog(sha))
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	return lines[len(lines)-1]
}

func (f *fixture) empty() {
	f.t.Helper()
	q, err := f.repo.readQueue()
	if err != nil || len(q) != 0 {
		f.t.Errorf("expected the queue empty, got %+v, %v", q, err)
	}
	if refs := f.git(f.repo.Bare, "for-each-ref", "refs/queue/"); refs != "" {
		f.t.Errorf("expected no queue refs, got %s", refs)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// Two changes land in order, checked side by side: the second on top of the first.
func TestTwoChangesLandInOrderCheckedSideBySide(t *testing.T) {
	f := newFixture(t, "batch = 1\n")
	a := f.change("a", map[string]string{"a": "a\n", "slow": "x\n"})
	b := f.change("b", map[string]string{"b": "b\n"})
	f.work(2)
	if got := f.landed(); got != "a,b" {
		t.Errorf("main: expected a then b, got %q", got)
	}
	if f.most.Load() != 2 {
		t.Errorf("expected two checks at once, got at most %d", f.most.Load())
	}
	if !strings.HasPrefix(f.verdict(a), landedPrefix) || !strings.HasPrefix(f.verdict(b), landedPrefix) {
		t.Errorf("verdicts: %q, %q", f.verdict(a), f.verdict(b))
	}
	f.empty()
}

// A red change at the head leaves the queue with its verdict; the change behind it lands without it.
func TestARedHeadLeavesAndTheNextLandsWithoutIt(t *testing.T) {
	f := newFixture(t, "batch = 1\n")
	a := f.change("a", map[string]string{"red": "x\n"})
	b := f.change("b", map[string]string{"b": "b\n"})
	f.work(2)
	if got := f.landed(); got != "b" {
		t.Errorf("main: expected only b, got %q", got)
	}
	if !strings.HasPrefix(f.verdict(a), notLandedPrefix) || !strings.HasPrefix(f.verdict(b), landedPrefix) {
		t.Errorf("verdicts: %q, %q", f.verdict(a), f.verdict(b))
	}
	if refs := f.git(f.repo.Bare, "for-each-ref", "--format=%(refname)", "refs/landed/"); refs != landedRef(b) {
		t.Errorf("refs/landed: expected b's alone, got %q", refs)
	}
	f.empty()
}

// A red batch is split in halves until the red change is alone; every other change lands, in order.
func TestARedBatchIsBisectedToItsCulprit(t *testing.T) {
	f := newFixture(t, "batch = 4\n")
	a := f.change("a", map[string]string{"a": "a\n"})
	b := f.change("b", map[string]string{"b": "b\n"})
	c := f.change("c", map[string]string{"red": "x\n"})
	d := f.change("d", map[string]string{"d": "d\n"})
	f.work(1)
	if got := f.landed(); got != "a,b,d" {
		t.Errorf("main: expected a, b, d, got %q", got)
	}
	if got := strings.Join(f.checked, " | "); got != "a,b,c,d | a,b | c,d | c | d" {
		t.Errorf("the runs: got %q", got)
	}
	for _, sha := range []string{a, b, d} {
		if !strings.HasPrefix(f.verdict(sha), landedPrefix) {
			t.Errorf("%s: %q", sha, f.verdict(sha))
		}
	}
	if !strings.HasPrefix(f.verdict(c), notLandedPrefix) {
		t.Errorf("c: %q", f.verdict(c))
	}
	f.empty()
}

// A run that fails behind a run still checking waits for it: the head lands, then the failure is decided.
func TestAFailBehindAPendingHeadWaitsForIt(t *testing.T) {
	f := newFixture(t, "batch = 1\n")
	f.change("a", map[string]string{"a": "a\n", "slow": "x\n"})
	b := f.change("b", map[string]string{"red": "x\n"})
	f.work(2)
	if got := f.landed(); got != "a" {
		t.Errorf("main: expected a, got %q", got)
	}
	if !strings.HasPrefix(f.verdict(b), notLandedPrefix) {
		t.Errorf("b: %q", f.verdict(b))
	}
	f.empty()
}

// A change that leaves while it is checked never lands; the change behind it is checked again without it.
func TestAChangeThatLeavesNeverLands(t *testing.T) {
	f := newFixture(t, "batch = 1\n")
	a := f.change("a", map[string]string{"a": "a\n", "slow": "x\n"})
	f.change("b", map[string]string{"b": "b\n"})
	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := f.repo.Leave(a, "it left"); err != nil {
			t.Error(err)
		}
	}()
	f.work(2)
	if got := f.landed(); got != "b" {
		t.Errorf("main: expected only b, got %q", got)
	}
	if f.verdict(a) != notLandedPrefix+"it left" {
		t.Errorf("a: %q", f.verdict(a))
	}
	f.empty()
}

// A jump puts a change first, checked alone; the runs start again behind it.
func TestAJumpLandsFirst(t *testing.T) {
	f := newFixture(t, "batch = 1\n")
	f.change("a", map[string]string{"a": "a\n", "slow": "x\n"})
	b := f.change("b", map[string]string{"b": "b\n"})
	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := f.repo.Jump(b); err != nil {
			t.Error(err)
		}
	}()
	f.work(1)
	if got := f.landed(); got != "b,a" {
		t.Errorf("main: expected b then a, got %q", got)
	}
	f.empty()
}

// A change that does not apply on the changes ahead of it leaves the queue, and its verdict names the file.
func TestAChangeThatDoesNotApplyLeaves(t *testing.T) {
	f := newFixture(t, "batch = 1\n")
	f.change("a", map[string]string{"file": "a\n"})
	b := f.change("b", map[string]string{"file": "b\n"})
	f.work(2)
	if got := f.landed(); got != "a" {
		t.Errorf("main: expected a, got %q", got)
	}
	if v := f.verdict(b); !strings.HasPrefix(v, notLandedPrefix) || !strings.Contains(v, "file") {
		t.Errorf("b: %q", v)
	}
	f.empty()
}

// A change based on an older main is picked onto main: it lands on top, with its author kept.
func TestAChangeOnAnOlderMainLandsOnTop(t *testing.T) {
	f := newFixture(t, "batch = 1\n")
	f.change("a", map[string]string{"a": "a\n"})
	b := f.change("b", map[string]string{"b": "b\n"})
	f.work(1)
	if got := f.landed(); got != "a,b" {
		t.Errorf("main: expected a then b, got %q", got)
	}
	if f.git(f.repo.Bare, "rev-parse", "main") == b {
		t.Errorf("b was on the old main: expected it picked onto a, as a new commit")
	}
	if got := f.git(f.repo.Bare, "log", "-1", "--format=%an %cn", "main"); got != "t crate" {
		t.Errorf("author and committer: expected t crate, got %q", got)
	}
	if got, main := f.git(f.repo.Bare, "rev-parse", landedRef(b)), f.git(f.repo.Bare, "rev-parse", "main"); got != main {
		t.Errorf("refs/landed/<b>: expected main, %s, got %s", main, got)
	}
}

// Main moved under the queue: no run lands on a main it was not built on; the runs start again on the new main.
func TestMainMovedUnderTheQueueStartsAgain(t *testing.T) {
	f := newFixture(t, "batch = 1\n")
	f.change("a", map[string]string{"a": "a\n", "slow": "x\n"})
	go func() {
		time.Sleep(500 * time.Millisecond)
		f.git(f.client, "checkout", "-q", "--detach", "main")
		f.write("other", "o\n")
		f.git(f.client, "add", "-A")
		f.git(f.client, "commit", "-qm", "other")
		f.git(f.client, "push", "-q", f.repo.Bare, "HEAD:main")
	}()
	f.work(1)
	if got := f.landed(); got != "other,a" {
		t.Errorf("main: expected other then a, got %q", got)
	}
}

// A run that crate could not check decides nothing: its change stays, unsplit, is checked again, and lands once crate
// can check it. Its log says it waits.
func TestAnErrorKeepsTheChangeQueuedAndUnsplitAndItLandsLater(t *testing.T) {
	f := newFixture(t, "batch = 2\n")
	f.sick.Store(1)
	a := f.change("a", map[string]string{"a": "a\n", "sick": "x\n"})
	b := f.change("b", map[string]string{"b": "b\n"})
	f.work(1)
	if got := f.landed(); got != "a,b" {
		t.Errorf("main: expected a then b, got %q", got)
	}
	if len(f.checked) != 2 || f.checked[0] != "a,b" || f.checked[1] != "a,b" {
		t.Errorf("expected the batch checked twice, whole, got %q", f.checked)
	}
	log, _ := os.ReadFile(f.repo.entryLog(a))
	if !strings.Contains(string(log), "crate could not check it (podman images did not finish within 30s); it waits") {
		t.Errorf("expected a's log to say it waits, got:\n%s", log)
	}
	if !strings.HasPrefix(f.verdict(a), landedPrefix) || !strings.HasPrefix(f.verdict(b), landedPrefix) {
		t.Errorf("verdicts: %q, %q", f.verdict(a), f.verdict(b))
	}
	if h, _ := machine.ReadHealth(); h.Errors != 0 || h.Paused != "" {
		t.Errorf("expected the pass to set the machine's health back, got %+v", h)
	}
	f.empty()
}

// Errors in a row pause the machine: the worker stops, no change leaves, and the next worker starts none. Once the
// machine is well (the probe passed), the changes land.
func TestErrorsInARowPauseTheMachineAndNothingIsRefused(t *testing.T) {
	f := newFixture(t, "batch = 1\n")
	f.sick.Store(100)
	a := f.change("a", map[string]string{"a": "a\n", "sick": "x\n"})
	f.change("b", map[string]string{"b": "b\n"})
	f.work(1)
	h, _ := machine.ReadHealth()
	if h.Paused != "podman images did not finish within 30s" {
		t.Fatalf("expected the machine paused on the error, got %+v", h)
	}
	if len(f.checked) != machine.PauseAfter {
		t.Errorf("expected %d checks before the pause, got %q", machine.PauseAfter, f.checked)
	}
	q, _ := f.repo.readQueue()
	if len(q) != 2 || q[0].Sha != a || q[0].Errors != machine.PauseAfter {
		t.Fatalf("expected both changes still queued, a first with %d errors, got %+v", machine.PauseAfter, q)
	}
	if state, err := f.repo.startWorker(); err != nil || state != WorkerPaused {
		t.Errorf("expected no worker to start on a paused machine, got %q, %v", state, err)
	}
	f.sick.Store(0)
	if err := machine.Passed(); err != nil {
		t.Fatal(err)
	}
	f.work(1)
	if got := f.landed(); got != "a,b" {
		t.Errorf("main: expected a then b once the machine is well, got %q", got)
	}
	f.empty()
}

// A change that crate cannot check, on a machine that checks the rest, leaves after PauseAfter errors with the
// verdict error, which asks its pusher to land it again; the change behind it lands.
func TestAChangeThatErrsAloneLeavesWithTheVerdictError(t *testing.T) {
	f := newFixture(t, "batch = 1\n")
	f.lonely = true
	a := f.change("a", map[string]string{"a": "a\n", "sick": "x\n"})
	f.change("b", map[string]string{"b": "b\n"})
	f.work(1)
	if got := f.landed(); got != "b" {
		t.Errorf("main: expected b alone, got %q", got)
	}
	want := notLandedPrefix + "error: crate could not check it 3 times (podman exec exited 125: podman could not run scripts/check.sh); land it again"
	if got := f.verdict(a); got != want {
		t.Errorf("a's verdict:\n got %q\nwant %q", got, want)
	}
	if h, _ := machine.ReadHealth(); h.Paused != "" {
		t.Errorf("expected the machine not paused, got %+v", h)
	}
	f.empty()
}
