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

func TestTheScaleDividesEveryLimit(t *testing.T) {
	for _, scale := range []struct {
		env string
		div time.Duration
	}{{"1", 1}, {"5", 5}, {"30", 30}, {"3600", 3600}} {
		t.Run(scale.env, func(t *testing.T) {
			t.Setenv("CRATE_LIMIT_SCALE", scale.env)
			for _, l := range all() {
				if got, want := l.get(), l.want/scale.div; got != want {
					t.Errorf("%s at scale %s: expected %v, got %v", l.name, scale.env, want, got)
				}
			}
		})
	}
}

func TestImageIsPullPlusBuildPlusSmokeAtAnyScale(t *testing.T) {
	for _, s := range []string{"1", "5", "30"} {
		t.Setenv("CRATE_LIMIT_SCALE", s)
		if got, want := limit.Image(), limit.Pull()+limit.Build()+limit.Smoke(); got != want || got == 0 {
			t.Errorf("scale %s: expected Image %v to be Pull+Build+Smoke, got %v", s, want, got)
		}
	}
}
