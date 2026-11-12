package limit_test

// The limits of the machine's own: the short lock of a kind's folder and each systemd call.

import (
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/limit"
)

func TestTheMachineLimitsHaveTheirValues(t *testing.T) {
	t.Setenv("CRATE_LIMIT_SCALE", "")
	rows := []struct {
		name string
		get  func() time.Duration
		want time.Duration
	}{
		{"SlotLock", limit.SlotLock, 10 * time.Second},
		{"Systemd", limit.Systemd, 30 * time.Second},
	}
	for _, row := range rows {
		if got := row.get(); got != row.want {
			t.Errorf("%s: expected %v, got %v", row.name, row.want, got)
		}
	}
}

func TestTheScaleDividesTheMachineLimitsToNoLessThanOneSecond(t *testing.T) {
	rows := []struct {
		name string
		get  func() time.Duration
		want time.Duration
	}{
		{"SlotLock", limit.SlotLock, 10 * time.Second},
		{"Systemd", limit.Systemd, 30 * time.Second},
	}
	for _, scale := range []struct {
		env string
		div time.Duration
	}{{"10", 10}, {"30", 30}, {"3600", 3600}} {
		t.Run(scale.env, func(t *testing.T) {
			t.Setenv("CRATE_LIMIT_SCALE", scale.env)
			for _, row := range rows {
				want := row.want / scale.div
				if want < time.Second {
					want = time.Second
				}
				if got := row.get(); got != want {
					t.Errorf("%s at scale %s: expected %v, got %v", row.name, scale.env, want, got)
				}
			}
		})
	}
}
