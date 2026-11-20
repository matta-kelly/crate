package machine

// The machine's health: errors in a row pause it at PauseAfter, and a pass sets it back.

import "testing"

func TestErrorsInARowPauseTheMachineAndAPassResumesIt(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for i := 1; i < PauseAfter; i++ {
		h, err := Erred("podman did not answer")
		if err != nil || h.Paused != "" || h.Errors != i {
			t.Fatalf("error %d: expected a count of %d and no pause, got %+v, %v", i, i, h, err)
		}
	}
	h, err := Erred("podman images did not finish within 30s")
	if err != nil || h.Paused != "podman images did not finish within 30s" || h.Since.IsZero() {
		t.Fatalf("expected the %dth error to pause with its text, got %+v, %v", PauseAfter, h, err)
	}
	if h, _ := Erred("a later error"); h.Paused != "podman images did not finish within 30s" {
		t.Errorf("expected the pause to keep the error that made it, got %q", h.Paused)
	}
	if err := Passed(); err != nil {
		t.Fatal(err)
	}
	if h, err := ReadHealth(); err != nil || h.Errors != 0 || h.Paused != "" {
		t.Errorf("expected a pass to set the health back, got %+v, %v", h, err)
	}
}

func TestAPassBetweenErrorsStartsTheCountAgain(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for range PauseAfter - 1 {
		_, _ = Erred("x")
	}
	_ = Passed()
	if h, _ := Erred("x"); h.Errors != 1 || h.Paused != "" {
		t.Errorf("expected a count of 1 after a pass, got %+v", h)
	}
}
