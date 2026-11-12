package limit_test

import (
	"testing"
	"time"

	"github.com/matta-kelly/crate/internal/limit"
)

// every limit, and its value with the scale unset
func all() []struct {
	name string
	get  func() time.Duration
	want time.Duration
} {
	return []struct {
		name string
		get  func() time.Duration
		want time.Duration
	}{
		{"Git", limit.Git, 30 * time.Second},
		{"SkopeoInspect", limit.SkopeoInspect, 30 * time.Second},
		{"SkopeoCopy", limit.SkopeoCopy, 10 * time.Minute},
		{"Pull", limit.Pull, 15 * time.Minute},
		{"Push", limit.Push, 15 * time.Minute},
		{"Build", limit.Build, 30 * time.Minute},
		{"Smoke", limit.Smoke, 10 * time.Minute},
		{"Gh", limit.Gh, 5 * time.Minute},
		{"Podman", limit.Podman, 30 * time.Second},
		{"RouterReload", limit.RouterReload, 10 * time.Second},
		{"RouterPodman", limit.RouterPodman, 30 * time.Second},
		{"Check", limit.Check, 30 * time.Minute},
		{"Hold", limit.Hold, time.Hour},
		{"Up", limit.Up, 15 * time.Minute},
		{"Image", limit.Image, 55 * time.Minute},
	}
}

func TestEachLimitHasItsValueWhenTheScaleIsUnset(t *testing.T) {
	t.Setenv("CRATE_LIMIT_SCALE", "")
	for _, l := range all() {
		if got := l.get(); got != l.want {
			t.Errorf("%s: expected %v, got %v", l.name, l.want, got)
		}
	}
}

// floored is a value divided by a factor, never less than 1 s
func floored(d, div time.Duration) time.Duration {
	if d/div < time.Second {
		return time.Second
	}
	return d / div
}

// the value a limit has at a factor: Image is the sum of the three limits it holds, each divided and floored
func wantAt(name string, want, div time.Duration) time.Duration {
	if name == "Image" {
		return floored(15*time.Minute, div) + floored(30*time.Minute, div) + floored(10*time.Minute, div)
	}
	return floored(want, div)
}

func TestTheScaleDividesEveryLimitToNoLessThanOneSecond(t *testing.T) {
	// 900 and 3600 reach the floor for the 30 s limits; 1000000 reaches it for every limit
	for _, scale := range []struct {
		env string
		div time.Duration
	}{{"1", 1}, {"5", 5}, {"30", 30}, {"900", 900}, {"3600", 3600}, {"1000000", 1000000}} {
		t.Run(scale.env, func(t *testing.T) {
			t.Setenv("CRATE_LIMIT_SCALE", scale.env)
			for _, l := range all() {
				got := l.get()
				if want := wantAt(l.name, l.want, scale.div); got != want {
					t.Errorf("%s at scale %s: expected %v, got %v", l.name, scale.env, want, got)
				}
			}
		})
	}
}

func TestImageIsPullPlusBuildPlusSmokeAtAnyScale(t *testing.T) {
	for _, s := range []string{"1", "5", "30", "3600", "1000000"} {
		t.Setenv("CRATE_LIMIT_SCALE", s)
		if got, want := limit.Image(), limit.Pull()+limit.Build()+limit.Smoke(); got != want || got == 0 {
			t.Errorf("scale %s: expected Image %v to be Pull+Build+Smoke, got %v", s, want, got)
		}
	}
}
