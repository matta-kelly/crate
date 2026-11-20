package fault_test

// Whose fault: only a mark makes a failure the change's, and an exit code is the command's own except podman's 125 to 127.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/matta-kelly/crate/internal/fault"
	"github.com/matta-kelly/crate/internal/process"
)

func exit(code int) error {
	return process.Run(context.Background(), process.Call{Prog: "sh", Args: []string{"-c", fmt.Sprintf("exit %d", code)}})
}

func TestAMarkHoldsThroughAWrapAndKeepsTheText(t *testing.T) {
	err := fmt.Errorf("setup: %w", fault.Change(errors.New("exit 1")))
	if !fault.IsChange(err) {
		t.Errorf("expected a wrapped mark to be the change's")
	}
	if err.Error() != "setup: exit 1" {
		t.Errorf("expected the text unchanged, got %q", err)
	}
	if fault.IsChange(errors.New("podman did not answer")) {
		t.Errorf("expected an unmarked failure not to be the change's")
	}
	if fault.Change(nil) != nil {
		t.Errorf("expected nil to stay nil")
	}
}

func TestOwnTakesTheCommandsCodesAndLeavesPodmans(t *testing.T) {
	for code, want := range map[int]bool{1: true, 3: true, 124: true, 125: false, 126: false, 127: false, 130: true} {
		if got := fault.IsChange(fault.Own(exit(code))); got != want {
			t.Errorf("exit %d: expected the change's to be %v, got %v", code, want, got)
		}
	}
	if fault.IsChange(fault.Own(errors.New("not a program"))) {
		t.Errorf("expected a failure with no exit code to stay crate's")
	}
}
