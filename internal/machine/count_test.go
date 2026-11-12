package machine_test

// The count: a slot is a file per slot under $XDG_STATE_HOME/crate/slots/<kind>/, naming its system and its
// holder. A check, postsubmit or attempt waits past its count; work takes none. A slot is free when it is
// released, when its holder is gone before its network exists, or when its network is gone.

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
)

// A slot is a file of its kind's folder, and it names the system and the process holding it.
func TestASlotIsAFileThatNamesItsSystemAndHolder(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	system := f.system("a")
	release := f.absent()
	h := f.startHelper("hold", machine.Check, system, "HELPER_RELEASE="+release, scaleFast)
	requireHeld(t, h.waitOut(t, 30*time.Second), "the first holder")

	record := f.oneSlot(machine.Check)
	text := readFile(t, record)
	if !strings.Contains(text, system) {
		t.Errorf("the slot at %s: expected it to name the system %q, got %q", record, system, text)
	}
	if !strings.Contains(text, strconv.Itoa(h.cmd.Process.Pid)) {
		t.Errorf("the slot at %s: expected it to name the holder pid %d, got %q", record, h.cmd.Process.Pid, text)
	}
	writeFile(t, release)
	h.waitExit(t, 30*time.Second)
}

// At check = 1, a second system's Hold waits until the first releases its slot.
func TestASecondSystemsHoldWaitsForTheFirstToRelease(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	first, second := f.system("a"), f.system("b")
	releaseFirst, releaseSecond := f.absent(), f.absent()
	h1 := f.startHelper("hold-bind", machine.Check, first,
		"HELPER_NETWORK="+first, "HELPER_RELEASE="+releaseFirst)
	f.networks = append(f.networks, first)
	at1 := outTime(t, h1.waitOut(t, 30*time.Second))
	f.oneSlot(machine.Check)

	h2 := f.startHelper("hold", machine.Check, second, "HELPER_RELEASE="+releaseSecond)
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(h2.out); err == nil {
		t.Fatalf("the second Hold: expected it to wait while %q held the only check slot", first)
	}
	writeFile(t, releaseFirst)
	at2 := outTime(t, h2.waitOut(t, 30*time.Second))
	if !at2.After(at1.Add(500 * time.Millisecond)) {
		t.Errorf("the second Hold: expected it to return after the first released, got %v then %v", at1, at2)
	}
	writeFile(t, releaseSecond)
	h1.waitExit(t, 30*time.Second)
	h2.waitExit(t, 30*time.Second)
}

// A live holder keeps its slot before its network exists: a network that is not made yet never frees one.
func TestALiveHolderKeepsItsSlotBeforeItsNetworkExists(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	first, second := f.system("a"), f.system("b")
	releaseFirst, releaseSecond := f.absent(), f.absent()
	h1 := f.startHelper("hold", machine.Check, first, "HELPER_RELEASE="+releaseFirst)
	at1 := outTime(t, h1.waitOut(t, 30*time.Second))
	f.oneSlot(machine.Check)

	h2 := f.startHelper("hold", machine.Check, second, "HELPER_RELEASE="+releaseSecond)
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(h2.out); err == nil {
		t.Fatalf("the second Hold: expected it to wait while %q held its slot with no network yet", first)
	}
	writeFile(t, releaseFirst)
	at2 := outTime(t, h2.waitOut(t, 30*time.Second))
	if !at2.After(at1.Add(500 * time.Millisecond)) {
		t.Errorf("the second Hold: expected it to return after the first released, got %v then %v", at1, at2)
	}
	writeFile(t, releaseSecond)
	h1.waitExit(t, 30*time.Second)
	h2.waitExit(t, 30*time.Second)
}

// A holder that dies before its network exists leaves a slot that is free.
func TestAHolderThatDiesBeforeItsNetworkFreesItsSlot(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	first, second := f.system("a"), f.system("b")
	h1 := f.startHelper("hold", machine.Check, first, "HELPER_RELEASE="+f.absent(), "HELPER_TIMEOUT=60s")
	requireHeld(t, h1.waitOut(t, 30*time.Second), "the first holder")
	f.oneSlot(machine.Check)
	if err := h1.cmd.Process.Kill(); err != nil {
		t.Fatalf("killing the holder: %v", err)
	}
	h1.waitExit(t, 30*time.Second)

	release := f.absent()
	h2 := f.startHelper("hold", machine.Check, second, "HELPER_RELEASE="+release, scaleFast)
	out := h2.waitOut(t, 30*time.Second)
	if !strings.HasPrefix(out, "ok ") {
		t.Fatalf("the second Hold after the first holder died before its network: expected it to hold, got %q", out)
	}
	writeFile(t, release)
	h2.waitExit(t, 30*time.Second)
}

// A holder that exits after its bind keeps its slot until its system's network goes.
func TestAHolderThatExitsAfterItsBindKeepsItsSlotUntilItsNetworkGoes(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	first, second, third := f.system("a"), f.system("b"), f.system("c")
	h1 := f.startHelper("hold-bind-exit", machine.Attempt, first, "HELPER_NETWORK="+first)
	f.networks = append(f.networks, first)
	requireHeld(t, h1.waitOut(t, 30*time.Second), "the first holder")
	h1.waitExit(t, 30*time.Second)
	if err := exec.Command("podman", "network", "exists", first).Run(); err != nil {
		t.Fatalf("the first system's network: expected it after a bind, got none")
	}
	f.oneSlot(machine.Attempt)

	release := f.absent()
	h2 := f.startHelper("hold", machine.Attempt, second, "HELPER_RELEASE="+release, scaleFast)
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(h2.out); err == nil {
		t.Fatalf("the second Hold: expected it to wait while the first system's network exists")
	}
	out := h2.waitOut(t, 30*time.Second)
	message, ok := strings.CutPrefix(out, "err ")
	if !ok {
		t.Fatalf("the second Hold: expected it to stop at limit.Hold, got %q", out)
	}
	for _, want := range []string{machine.Attempt, "1", first} {
		if !strings.Contains(message, want) {
			t.Errorf("the wait's error: expected it to name %q, got %q", want, message)
		}
	}
	h2.waitExit(t, 30*time.Second)

	f.run("podman", "network", "rm", first)
	h3 := f.startHelper("hold", machine.Attempt, third, "HELPER_RELEASE="+release, scaleFast)
	out = h3.waitOut(t, 30*time.Second)
	if !strings.HasPrefix(out, "ok ") {
		t.Fatalf("the third Hold after the network went: expected it to hold, got %q", out)
	}
	writeFile(t, release)
	h3.waitExit(t, 30*time.Second)
}

// One system holds one slot whatever it starts: its places and a second Hold all start at once.
func TestOneSystemHoldsOneSlotWhateverItStarts(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	release := f.absent()
	h := f.startHelper("one", machine.Check, f.system("a"), "HELPER_RELEASE="+release, scaleFast)
	if took := outMillis(t, h.waitOut(t, 30*time.Second)); took > 5000 {
		t.Errorf("one system's places and holds: expected no wait, took %d ms", took)
	}
	f.oneSlot(machine.Check)
	writeFile(t, release)
	h.waitExit(t, 30*time.Second)
}

// The count is the number of slots a kind has: at check = 2, two systems hold and a third waits.
func TestTheCountIsTheNumberOfSlotsAKindHas(t *testing.T) {
	f := newFixture(t, 2, 1, 1)
	systems := []string{f.system("a"), f.system("b"), f.system("c")}
	var releases []string
	var holders []*helperProc
	for _, system := range systems[:2] {
		release := f.absent()
		releases = append(releases, release)
		holders = append(holders, f.startHelper("hold", machine.Check, system, "HELPER_RELEASE="+release, scaleFast))
	}
	for _, h := range holders {
		requireHeld(t, h.waitOut(t, 30*time.Second), "a check holder")
	}
	if files := f.slotFiles(machine.Check); len(files) != 2 {
		t.Fatalf("two check holds at check = 2: expected two slot files, got %v", files)
	}

	thirdRelease := f.absent()
	h3 := f.startHelper("hold", machine.Check, systems[2], "HELPER_RELEASE="+thirdRelease, scaleFast)
	out := h3.waitOut(t, 30*time.Second)
	message, ok := strings.CutPrefix(out, "err ")
	if !ok {
		t.Fatalf("a third Hold at check = 2: expected it to stop at limit.Hold, got %q", out)
	}
	for _, want := range []string{machine.Check, "2", systems[0], systems[1]} {
		if !strings.Contains(message, want) {
			t.Errorf("the wait's error: expected it to name %q, got %q", want, message)
		}
	}
	h3.waitExit(t, 30*time.Second)
	for i, h := range holders {
		writeFile(t, releases[i])
		h.waitExit(t, 30*time.Second)
	}
}

// Work takes no count: three people's work holds at check = 1 all start at once, one slot each.
func TestWorkTakesNoCount(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	systems := []string{f.system("a"), f.system("b"), f.system("c")}
	release := f.absent()
	h := f.startHelper("work-holds", machine.Work, "unused",
		"HELPER_SYSTEMS="+strings.Join(systems, ","), "HELPER_RELEASE="+release, scaleFast)
	if took := outMillis(t, h.waitOut(t, 30*time.Second)); took > 2000 {
		t.Errorf("three people's work holds: expected no wait, took %d ms", took)
	}
	if files := f.slotFiles(machine.Work); len(files) != 3 {
		t.Errorf("three work holds: expected three slot files, got %v", files)
	}
	writeFile(t, release)
	h.waitExit(t, 30*time.Second)
}

// Each kind has its own count: at 1 of each, a held postsubmit never makes a check or an attempt wait.
func TestTheKindsHaveTheirOwnCounts(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	var releases []string
	var holders []*helperProc
	for i, kind := range []string{machine.Postsubmit, machine.Check, machine.Attempt} {
		release := f.absent()
		releases = append(releases, release)
		holders = append(holders, f.startHelper("hold", kind, f.system(strconv.Itoa(i)), "HELPER_RELEASE="+release, scaleFast))
	}
	for _, h := range holders {
		if out := h.waitOut(t, 30*time.Second); !strings.HasPrefix(out, "ok ") {
			t.Fatalf("a Hold of one kind beside the others: expected it to hold, got %q", out)
		}
	}
	for _, kind := range []string{machine.Postsubmit, machine.Check, machine.Attempt} {
		f.oneSlot(kind)
	}
	for i, h := range holders {
		writeFile(t, releases[i])
		h.waitExit(t, 30*time.Second)
	}
}

// The wait stops at limit.Hold, and its error names the kind, the count and the holders.
func TestTheHoldStopsAtItsLimitAndNamesWhatItWaitedFor(t *testing.T) {
	f := newFixture(t, 1, 1, 1)
	first, second := f.system("a"), f.system("b")
	h1 := f.startHelper("hold", machine.Check, first, "HELPER_RELEASE="+f.absent())
	requireHeld(t, h1.waitOut(t, 30*time.Second), "the first holder")

	started := time.Now()
	h2 := f.startHelper("hold", machine.Check, second, "HELPER_RELEASE="+f.absent(), scaleFast)
	out := h2.waitOut(t, 30*time.Second)
	took := time.Since(started)
	message, ok := strings.CutPrefix(out, "err ")
	if !ok {
		t.Fatalf("the second Hold: expected an error past the limit, got %q", out)
	}
	for _, want := range []string{machine.Check, "1", first, "1s"} {
		if !strings.Contains(message, want) {
			t.Errorf("the wait's error: expected it to name %q, got %q", want, message)
		}
	}
	if took < 500*time.Millisecond || took > 10*time.Second {
		t.Errorf("the wait past its limit: expected about one second (1 h divided by 3600), took %v", took)
	}
	f.oneSlot(machine.Check)
	h2.waitExit(t, 30*time.Second)
}
