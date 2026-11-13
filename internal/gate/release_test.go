package gate_test

import (
	"testing"

	"github.com/matta-kelly/crate/internal/gate"
)

// The bump is the strongest the commits ask for, from their subjects and bodies.
func TestTheBumpIsTheStrongestTheCommitsAskFor(t *testing.T) {
	rows := []struct {
		name     string
		subjects []string
		bodies   []string
		want     string
	}{
		{"feat", []string{"feat: a"}, nil, "minor"},
		{"a scoped fix", []string{"fix(gate): a"}, nil, "patch"},
		{"perf", []string{"perf: a"}, nil, "patch"},
		{"a breaking type", []string{"refactor(x)!: a"}, nil, "major"},
		{"a breaking footer", []string{"fix: a"}, []string{"why\n\nBREAKING CHANGE: b"}, "major"},
		{"the strongest of several", []string{"fix: a", "feat: b", "docs: c"}, nil, "minor"},
		{"none", []string{"docs: a", "chore: b", "featured: c"}, nil, ""},
	}
	for _, row := range rows {
		if got := gate.Bump(row.subjects, row.bodies); got != row.want {
			t.Errorf("%s: expected %q, got %q", row.name, row.want, got)
		}
	}
}

// Before 1.0.0 each bump moves down one place.
func TestTheNextVersionMovesDownAPlaceBeforeOne(t *testing.T) {
	rows := []struct{ from, bump, want string }{
		{"1.4.2", "patch", "1.4.3"},
		{"1.4.2", "minor", "1.5.0"},
		{"1.4.2", "major", "2.0.0"},
		{"0.3.1", "patch", "0.3.2"},
		{"0.3.1", "minor", "0.3.2"},
		{"0.3.1", "major", "0.4.0"},
	}
	for _, row := range rows {
		if got := gate.NextVersion(row.from, row.bump); got != row.want {
			t.Errorf("%s %s: expected %s, got %s", row.from, row.bump, row.want, got)
		}
	}
}
