package machine_test

// The slot: a claim on the machine that a system holds across the places it makes, freed by Release or by the
// end of its network, with a count per kind.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/machine"
)

func noFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "never")
}

// Two systems, one process each, check = 1: the second Hold returns only after the first releases.
func TestASecondSystemsHoldWaitsForTheFirst(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	first, second := f.system("a"), f.system("b")
	releaseFirst, releaseSecond := noFile(t), noFile(t)
	h1 := f.startHelper("hold-bind", machine.Check, first,
		"HELPER_NETWORK="+first, "HELPER_RELEASE="+releaseFirst)
	f.networks = append(f.networks, first)
	at1 := outTime(t, h1.waitOut(t, 10*time.Second))
	f.oneSlot(machine.Check)

	h2 := f.startHelper("hold", machine.Check, second, "HELPER_RELEASE="+releaseSecond)
	time.Sleep(time.Second)
	if _, err := os.Stat(h2.out); err == nil {
		t.Fatalf("the second Hold: expected it to wait while %q held the only check slot", first)
	}
	writeFile(t, releaseFirst)
	at2 := outTime(t, h2.waitOut(t, 10*time.Second))
	if !at2.After(at1.Add(500 * time.Millisecond)) {
		t.Errorf("the second Hold: expected it to return after the first released, got %v then %v", at1, at2)
	}
	writeFile(t, releaseSecond)
	h2.waitExit(t, 10*time.Second)
	h1.waitExit(t, 10*time.Second)
}

// Before its network is made, a held slot is still taken: it is never free because a network does not exist yet.
func TestASlotIsNotFreeBecauseItsNetworkIsNotMadeYet(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	first, second := f.system("a"), f.system("b")
	releaseFirst, releaseSecond := noFile(t), noFile(t)
	h1 := f.startHelper("hold", machine.Check, first, "HELPER_RELEASE="+releaseFirst)
	out1 := h1.waitOut(t, 10*time.Second)
	at1 := outTime(t, out1)
	f.oneSlot(machine.Check)
	if err := exec.Command("podman", "network", "exists", first).Run(); err == nil {
		t.Fatalf("the first system's network: expected none before a bind, got one")
	}

	h2 := f.startHelper("hold", machine.Check, second, "HELPER_RELEASE="+releaseSecond)
	time.Sleep(time.Second)
	if _, err := os.Stat(h2.out); err == nil {
		t.Fatalf("the second Hold: expected it to wait, since the first slot is taken even with no network yet")
	}
	writeFile(t, releaseFirst)
	at2 := outTime(t, h2.waitOut(t, 10*time.Second))
	if !at2.After(at1) {
		t.Errorf("the second Hold: expected it to return after the first released, got %v then %v", at1, at2)
	}
	writeFile(t, releaseSecond)
	h2.waitExit(t, 10*time.Second)
	h1.waitExit(t, 10*time.Second)
}

// A holder killed before it binds leaves a slot that is free: its process is gone.
func TestACrashBeforeTheNetworkFreesItsSlot(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	first, second := f.system("a"), f.system("b")
	h1 := f.startHelper("hold", machine.Check, first, "HELPER_RELEASE="+noFile(t), "HELPER_TIMEOUT=60s")
	h1.waitOut(t, 10*time.Second)
	f.oneSlot(machine.Check)
	if err := h1.cmd.Process.Kill(); err != nil {
		t.Fatalf("killing the holder: %v", err)
	}
	h1.waitExit(t, 10*time.Second)

	release := noFile(t)
	h2 := f.startHelper("hold", machine.Check, second, "HELPER_RELEASE="+release, "CRATE_LIMIT_SCALE=3600")
	at := outTime(t, h2.waitOut(t, 10*time.Second))
	if at.Before(time.Now().Add(-10 * time.Second)) {
		t.Fatalf("the second Hold: expected a fresh hold, got one from %v", at)
	}
	writeFile(t, release)
	h2.waitExit(t, 10*time.Second)
}

// A holder that exits after the bind keeps its slot until its system's network goes, with no Release called.
func TestAHolderThatExitsAfterBindKeepsTheSlotUntilItsNetworkGoes(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	first, second := f.system("a"), f.system("b")
	h1 := f.startHelper("hold-bind-exit", machine.Attempt, first, "HELPER_NETWORK="+first)
	f.networks = append(f.networks, first)
	h1.waitOut(t, 10*time.Second)
	h1.waitExit(t, 10*time.Second)
	if err := exec.Command("podman", "network", "exists", first).Run(); err != nil {
		t.Fatalf("the first system's network: expected it after a bind, got none")
	}
	f.oneSlot(machine.Attempt)

	release := noFile(t)
	h2 := f.startHelper("hold", machine.Attempt, second, "HELPER_RELEASE="+release, "CRATE_LIMIT_SCALE=3600")
	time.Sleep(time.Second)
	if _, err := os.Stat(h2.out); err == nil {
		t.Fatalf("the second Hold: expected it to wait, since the first system's network still exists")
	}
	f.networks = removeString(f.networks, first)
	f.run("podman", "network", "rm", first)
	at := outTime(t, h2.waitOut(t, 10*time.Second))
	if at.Before(time.Now().Add(-10 * time.Second)) {
		t.Fatalf("the second Hold: expected a fresh hold, got one from %v", at)
	}
	writeFile(t, release)
	h2.waitExit(t, 10*time.Second)
}

func removeString(values []string, value string) []string {
	var kept []string
	for _, v := range values {
		if v != value {
			kept = append(kept, v)
		}
	}
	return kept
}

// One system places what it starts and holds again: all of it starts at once, and it holds one slot.
func TestOneSystemHoldsOneSlotForItsPlacesAndHolds(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	t.Setenv("CRATE_LIMIT_SCALE", "3600")
	system := f.system("a")
	m := machine.Default()
	started := time.Now()
	first, err := m.Hold(machine.Check, system)
	if err != nil {
		t.Fatalf("Hold(%q): %v", system, err)
	}
	for _, kind := range []string{machine.Work, machine.Check, machine.Work} {
		if _, err := m.Place(kind, system); err != nil {
			t.Fatalf("Place(%q): %v", kind, err)
		}
	}
	second, err := m.Hold(machine.Check, system)
	if err != nil {
		t.Fatalf("the second Hold(%q): %v", system, err)
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Errorf("one system's places and holds: expected no wait, took %v", took)
	}
	if files := f.slotFiles(machine.Check); len(files) != 1 {
		t.Errorf("the check slots: expected one for this system, got %v", files)
	}
	first.Release()
	second.Release()
}

// Each kind has its own count: a held postsubmit never makes a check wait, nor the other way.
func TestTheKindsHaveTheirOwnCounts(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	t.Setenv("CRATE_LIMIT_SCALE", "3600")
	m := machine.Default()
	post, err := m.Hold(machine.Postsubmit, f.system("a"))
	if err != nil {
		t.Fatalf("the postsubmit Hold: %v", err)
	}
	check, err := m.Hold(machine.Check, f.system("b"))
	if err != nil {
		t.Fatalf("a check Hold beside a held postsubmit: %v", err)
	}
	attempt, err := m.Hold(machine.Attempt, f.system("c"))
	if err != nil {
		t.Fatalf("an attempt Hold beside the others: %v", err)
	}
	for _, kind := range []string{machine.Postsubmit, machine.Check, machine.Attempt} {
		f.oneSlot(kind)
	}
	post.Release()
	check.Release()
	attempt.Release()

	// and the other way: a held check never makes a postsubmit wait
	check, err = m.Hold(machine.Check, f.system("d"))
	if err != nil {
		t.Fatalf("a check Hold: %v", err)
	}
	post, err = m.Hold(machine.Postsubmit, f.system("e"))
	if err != nil {
		t.Fatalf("a postsubmit Hold beside a held check: %v", err)
	}
	post.Release()
	check.Release()
}

// The record names the system, the holder's pid and start time, and says whether it is bound.
func TestTheSlotRecordNamesItsSystemAndHolder(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	system := f.system("a")
	dir := t.TempDir()
	bindAt, boundAt, release := filepath.Join(dir, "bind"), filepath.Join(dir, "bound"), filepath.Join(dir, "release")
	h := f.startHelper("hold-then-bind", machine.Check, system,
		"HELPER_NETWORK="+system, "HELPER_BIND_AT="+bindAt, "HELPER_BOUND_AT="+boundAt, "HELPER_RELEASE="+release)
	f.networks = append(f.networks, system)
	h.waitOut(t, 10*time.Second)
	record := f.oneSlot(machine.Check)
	before := readFile(t, record)
	pid := h.cmd.Process.Pid
	if !strings.Contains(before, system) {
		t.Errorf("the record: expected it to name the system %q, got %q", system, before)
	}
	if !strings.Contains(before, strconv.Itoa(pid)) {
		t.Errorf("the record: expected it to name the holder's pid %d, got %q", pid, before)
	}
	if start := procStart(t, pid); !strings.Contains(before, start) {
		t.Errorf("the record: expected it to name the holder's start time %s, got %q", start, before)
	}

	writeFile(t, bindAt)
	for i := 0; ; i++ {
		if _, err := os.Stat(boundAt); err == nil {
			break
		}
		if i > 500 {
			t.Fatalf("the holder did not bind; its stderr: %q", h.stderr())
		}
		time.Sleep(10 * time.Millisecond)
	}
	afterFiles := f.slotFiles(machine.Check)
	if len(afterFiles) != 1 {
		t.Fatalf("the record after Bind: expected one slot file, got %v", afterFiles)
	}
	after := readFile(t, afterFiles[0])
	if after == before {
		t.Errorf("the record after Bind: expected it to say the slot is bound, got the same %q", after)
	}
	writeFile(t, release)
	h.waitExit(t, 10*time.Second)
}

// procStart is the start time of a process as /proc/<pid>/stat gives it.
func procStart(t *testing.T, pid int) string {
	t.Helper()
	data := readFile(t, "/proc/"+strconv.Itoa(pid)+"/stat")
	close := strings.LastIndex(data, ")")
	if close < 0 {
		t.Fatalf("the stat of pid %d: %q", pid, data)
	}
	fields := strings.Fields(data[close+1:])
	if len(fields) < 20 {
		t.Fatalf("the stat of pid %d has %d fields after comm", pid, len(fields))
	}
	return fields[19]
}

// A pid that is there with another start time counts as gone.
func TestAHolderPidReusedWithAnotherStartTimeIsGone(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	first, second := f.system("a"), f.system("b")
	h1 := f.startHelper("hold", machine.Check, first, "HELPER_RELEASE="+noFile(t), "HELPER_TIMEOUT=60s")
	h1.waitOut(t, 10*time.Second)
	pid := h1.cmd.Process.Pid
	start := procStart(t, pid)
	record := f.oneSlot(machine.Check)
	text := readFile(t, record)
	if err := h1.cmd.Process.Kill(); err != nil {
		t.Fatalf("killing the holder: %v", err)
	}
	h1.waitExit(t, 10*time.Second)

	sleeper := exec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatalf("the second process: %v", err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill() })
	reused := strings.ReplaceAll(text, strconv.Itoa(pid), strconv.Itoa(sleeper.Process.Pid))
	if reused == text {
		t.Fatalf("the record: expected the holder's pid %d as a number in it, got %q", pid, text)
	}
	if !strings.Contains(text, start) {
		t.Fatalf("the record: expected the old start time %s in it, got %q", start, text)
	}
	if err := os.WriteFile(record, []byte(reused), 0o644); err != nil {
		t.Fatal(err)
	}

	release := noFile(t)
	h2 := f.startHelper("hold", machine.Check, second, "HELPER_RELEASE="+release)
	at := outTime(t, h2.waitOut(t, 10*time.Second))
	if at.Before(time.Now().Add(-10 * time.Second)) {
		t.Fatalf("the second Hold: expected a fresh hold after the pid was reused, got one from %v", at)
	}
	writeFile(t, release)
	h2.waitExit(t, 10*time.Second)
}

// The wait for a slot stops at limit.Hold and names the kind, the count, the limit and the holder.
func TestTheHoldStopsAtItsLimitAndNamesWhatItWaitedFor(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	first, second := f.system("a"), f.system("b")
	h1 := f.startHelper("hold", machine.Check, first, "HELPER_RELEASE="+noFile(t))
	h1.waitOut(t, 10*time.Second)

	started := time.Now()
	h2 := f.startHelper("hold", machine.Check, second,
		"HELPER_RELEASE="+noFile(t), "CRATE_LIMIT_SCALE=3600")
	out := h2.waitOut(t, 15*time.Second)
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
}

// Work takes no count: a person's work has no room to wait for.
func TestWorkTakesNoCount(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	t.Setenv("CRATE_LIMIT_SCALE", "3600")
	m := machine.Default()
	var slots []machine.Slot
	for i := 0; i < 3; i++ {
		s, err := m.Hold(machine.Work, f.system("w"+strconv.Itoa(i)))
		if err != nil {
			t.Fatalf("a player's Hold %d: expected no wait and no error, got %v", i, err)
		}
		slots = append(slots, s)
	}
	if files := f.slotFiles(machine.Work); len(files) != 3 {
		t.Errorf("three people's holds: expected three slot files, got %v", files)
	}
	for _, s := range slots {
		s.Release()
	}
}

// The short lock of a kind's folder: many Holds at once take one slot each, never the same one twice.
func TestManyHoldsAtOnceTakeOneSlotEach(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 6, 1, 1)
	type holder struct {
		system  string
		release string
		h       *helperProc
	}
	var holders []holder
	for i := 0; i < 6; i++ {
		system := f.system("s" + strconv.Itoa(i))
		release := noFile(t)
		holders = append(holders, holder{system: system, release: release, h: f.startHelper("hold", machine.Check, system, "HELPER_RELEASE="+release)})
	}
	for _, h := range holders {
		h.h.waitOut(t, 20*time.Second)
	}
	files := f.slotFiles(machine.Check)
	if len(files) != len(holders) {
		t.Fatalf("six Holds at once: expected six slot files, got %d: %v", len(files), files)
	}
	for _, h := range holders {
		named := 0
		for _, file := range files {
			if strings.Contains(readFile(t, file), h.system) {
				named++
			}
		}
		if named != 1 {
			t.Errorf("the slot records: expected exactly one to name %q, got %d", h.system, named)
		}
		writeFile(t, h.release)
	}
}

// Each systemctl and systemd-run call stops at limit.Systemd and names the call.
func TestASystemdCallStopsAtItsLimit(t *testing.T) {
	f := newFixture(t, "0-1", "512M", 1, 1, 1)
	hang := "#!/bin/sh\nsleep 60\n"
	f.pathWith(map[string]string{"systemctl": hang, "systemd-run": hang})
	t.Setenv("CRATE_LIMIT_SCALE", "30")

	started := time.Now()
	_, err := machine.Default().Place(machine.Work, f.system("a"))
	took := time.Since(started)
	if err == nil {
		t.Fatalf("Place with a manager that never answers: expected an error past limit.Systemd, got none")
	}
	if !strings.Contains(err.Error(), "systemctl") && !strings.Contains(err.Error(), "systemd-run") {
		t.Errorf("the error: expected it to name the systemd call, got %q", err)
	}
	if took < 500*time.Millisecond || took > 10*time.Second {
		t.Errorf("the call past its limit: expected about one second (30 s divided by 30), took %v", took)
	}
}
