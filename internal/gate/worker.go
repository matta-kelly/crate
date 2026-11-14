package gate

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
)

// The worker: the one process per repo that checks the queue's changes and moves main. It keeps a window of runs,
// as many as the machine checks at once, each on its own slot. Run n's tree is run n-1's tip and its own changes on
// top, so the runs check side by side what main will be, in order. The head run that passes moves main to its tip;
// a run that fails is decided once every run ahead of it has passed, and the runs behind it start again without it.

// run is one check of a batch of changes, on the tree of base and those changes.
type run struct {
	slot    int
	changes []Entry
	base    string
	tip     string
	cancel  context.CancelFunc
	done    bool
	err     error // nil when its check passed
}

func (rn *run) shas() []string { return shasOf(rn.changes) }

// checker checks a run's tree in the checkout of its slot; nil is a pass. The repo's own runs the repo's check.
type checker func(ctx context.Context, rn *run) error

type worker struct {
	r       *Repo
	window  int
	check   checker
	runs    []*run // in queue order; runs[0] is the head
	busy    []bool // a slot is busy until its check returns, cancelled or not
	results chan *run
	out     io.Writer
	marked  string // the runs' changes as queue.json last showed them
}

// Work runs the repo's worker until the queue is empty. When another worker runs, it returns at once.
func (r *Repo) Work(out io.Writer) error {
	return r.work(machine.Window(machine.Check), r.checkRun, out)
}

func (r *Repo) work(window int, check checker, out io.Writer) error {
	unlock, ok := r.tryLock("worker.lock")
	if !ok {
		return nil
	}
	w := &worker{r: r, window: max(window, 1), check: check, busy: make([]bool, max(window, 1)),
		results: make(chan *run), out: out}
	for {
		w.loop()
		unlock()
		// A change that entered as the loop ended finds no worker: look once more, after the lock is free.
		if q, err := r.readQueue(); err != nil || len(q) == 0 {
			return err
		}
		if unlock, ok = r.tryLock("worker.lock"); !ok {
			return nil
		}
	}
}

func (w *worker) loop() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		q, err := w.r.readQueue()
		if err != nil {
			fmt.Fprintf(w.out, "crate: %v\n", err)
		} else {
			w.align(q)
			if w.settle() {
				continue
			}
			w.fill(q)
			w.mark()
			if len(w.runs) == 0 && len(q) == 0 && !slices.Contains(w.busy, true) {
				return
			}
		}
		select {
		case rn := <-w.results:
			w.finish(rn)
		case <-tick.C:
		}
	}
}

// align keeps the runs on the queue's front, in its order: from the first run whose changes are not the queue's
// next, every run is cancelled. A change that left, or one that jumped ahead, ends the runs from there on.
func (w *worker) align(q []Entry) {
	at := 0
	for i, rn := range w.runs {
		for _, sha := range rn.shas() {
			if at >= len(q) || q[at].Sha != sha {
				w.cancel(i, "the queue changed ahead of it")
				return
			}
			at++
		}
	}
}

// settle decides the head run while it is done: a pass moves main and lands its changes, a fail ejects or bisects.
// It says whether the queue changed.
func (w *worker) settle() bool {
	changed := false
	for len(w.runs) > 0 && w.runs[0].done {
		head := w.runs[0]
		if head.err != nil {
			w.cancel(0, "a run ahead of it failed")
			w.fail(head)
			return true
		}
		landed, err := w.r.land(head)
		if err != nil {
			fmt.Fprintf(w.out, "crate: run %d does not land: %v\n", head.slot, err)
			w.cancel(0, err.Error())
			return true
		}
		w.runs = w.runs[1:]
		fmt.Fprintf(w.out, "crate: main is %s: %s\n", short(head.tip), strings.Join(shorts(head.shas()), " "))
		// What follows the move (the copy, the release, postsubmit) is told to the changes' pushers before their verdict.
		logs, closeLogs := w.r.entryLogs(head.shas())
		w.r.after(head.tip, io.MultiWriter(w.out, logs))
		closeLogs()
		for _, e := range landed {
			w.r.verdict(e, true, short(head.tip))
		}
		changed = true
	}
	return changed
}

// land moves main from the run's base to its tip and takes its changes out of the queue, as one step under the
// queue's lock, so a change that left meanwhile never lands. Main is moved by compare-and-swap: a main that is not
// the run's base is not moved.
func (r *Repo) land(rn *run) ([]Entry, error) {
	var gone []Entry
	err := r.changeQueue(func(q []Entry) ([]Entry, error) {
		for _, sha := range rn.shas() {
			if position(q, sha) < 0 {
				return nil, fmt.Errorf("%s left the queue", short(sha))
			}
		}
		if _, err := r.git("update-ref", "refs/heads/main", rn.tip, rn.base); err != nil {
			return nil, fmt.Errorf("main is not %s", short(rn.base))
		}
		return slices.DeleteFunc(q, func(e Entry) bool {
			if slices.Contains(rn.shas(), e.Sha) {
				gone = append(gone, e)
				return true
			}
			return false
		}), nil
	})
	return gone, err
}

// fail decides a failed head: a change checked alone leaves the queue; a batch is split, each change's limit half
// the batch, so its halves are checked apart.
func (w *worker) fail(rn *run) {
	if len(rn.changes) == 1 {
		for _, e := range w.take(rn.shas()) {
			w.r.verdict(e, false, "its check failed (the output above)")
		}
		return
	}
	half := (len(rn.changes) + 1) / 2
	in := rn.shas()
	_ = w.r.changeQueue(func(q []Entry) ([]Entry, error) {
		for i := range q {
			if slices.Contains(in, q[i].Sha) {
				q[i].Limit = half
			}
		}
		return q, nil
	})
	for _, sha := range in {
		appendTo(w.r.entryLog(sha), fmt.Sprintf("crate: its batch of %d failed; checked again in batches of %d", len(in), half))
	}
}

// fill starts runs on free slots while the window has room and changes wait, each on the tip of the run ahead.
func (w *worker) fill(q []Entry) {
	at := 0
	for _, rn := range w.runs {
		at += len(rn.changes)
	}
	waiting := q[min(at, len(q)):]
	base := w.r.main()
	if len(w.runs) > 0 {
		base = w.runs[len(w.runs)-1].tip
	}
	for len(waiting) > 0 && len(w.runs) < w.window {
		slot := slices.Index(w.busy, false)
		if slot < 0 || base == "" {
			return
		}
		n := min(w.r.declarationAt(base).BatchSize(), len(waiting))
		if limit := waiting[0].Limit; limit > 0 {
			n = min(n, limit)
		}
		take := waiting[:n]
		waiting = waiting[n:]
		dir := w.r.state("runs", strconv.Itoa(slot))
		tip, in, out, err := w.r.build(dir, base, take)
		if err != nil {
			// A checkout that git cannot use (a lock a killed run left, a folder gone half) is made again, once.
			fmt.Fprintf(w.out, "crate: run %d's checkout is made again: %v\n", slot, err)
			w.r.dropWorktree(dir)
			tip, in, out, err = w.r.build(dir, base, take)
		}
		if err != nil {
			for _, e := range w.take(shasOf(take)) {
				w.r.verdict(e, false, "crate could not make its tree: "+err.Error())
			}
			continue
		}
		for sha, why := range out {
			for _, e := range w.take([]string{sha}) {
				w.r.verdict(e, false, why)
			}
		}
		if len(in) == 0 {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		rn := &run{slot: slot, changes: in, base: base, tip: tip, cancel: cancel}
		w.busy[slot] = true
		w.runs = append(w.runs, rn)
		base = tip
		go func() {
			err := w.check(ctx, rn)
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			rn.err = err
			w.results <- rn
		}()
	}
}

// finish takes a run's result. A cancelled run only frees its slot. A failed run ends the runs behind it, as their
// trees hold its changes.
func (w *worker) finish(rn *run) {
	w.busy[rn.slot] = false
	i := slices.Index(w.runs, rn)
	if i < 0 {
		return
	}
	rn.done = true
	if rn.err != nil {
		w.cancel(i+1, "a run ahead of it failed")
	}
}

// cancel ends the runs from i on; their changes still in the queue wait again, and their logs say so.
func (w *worker) cancel(i int, why string) {
	if i >= len(w.runs) {
		return
	}
	q, _ := w.r.readQueue()
	for _, rn := range w.runs[i:] {
		if rn.done {
			continue
		}
		rn.cancel()
		for _, sha := range rn.shas() {
			if position(q, sha) >= 0 {
				appendTo(w.r.entryLog(sha), "crate: run "+strconv.Itoa(rn.slot)+" stopped: "+why+"; it is checked again")
			}
		}
	}
	w.runs = w.runs[:i]
}

// take removes the changes from the queue and gives them as they were.
func (w *worker) take(shas []string) []Entry {
	var gone []Entry
	_ = w.r.changeQueue(func(q []Entry) ([]Entry, error) {
		return slices.DeleteFunc(q, func(e Entry) bool {
			if slices.Contains(shas, e.Sha) {
				gone = append(gone, e)
				return true
			}
			return false
		}), nil
	})
	return gone
}

// mark writes into queue.json which run checks each change, and when a run first took it, when that changed.
func (w *worker) mark() {
	slots := map[string]int{}
	var key []string
	for _, rn := range w.runs {
		for _, sha := range rn.shas() {
			slots[sha] = rn.slot
			key = append(key, sha+"@"+strconv.Itoa(rn.slot))
		}
	}
	if k := strings.Join(key, " "); k != w.marked {
		w.marked = k
	} else {
		return
	}
	now := time.Now().UTC()
	_ = w.r.changeQueue(func(q []Entry) ([]Entry, error) {
		for i := range q {
			q[i].Slot = nil
			if slot, ok := slots[q[i].Sha]; ok {
				q[i].Slot = &slot
				if q[i].Started.IsZero() {
					q[i].Started = now
				}
			}
		}
		return q, nil
	})
}

func shasOf(entries []Entry) []string {
	s := make([]string, len(entries))
	for i, e := range entries {
		s[i] = e.Sha
	}
	return s
}

func shorts(shas []string) []string {
	s := make([]string, len(shas))
	for i, sha := range shas {
		s[i] = short(sha)
	}
	return s
}
