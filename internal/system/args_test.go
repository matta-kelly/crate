package system

// runArgs is the one place a member becomes a podman run. The Check promises the member's spec carries the
// read-only root, the dropped capabilities and its own CPU and memory limit, and that Tools sets none of them.

import (
	"slices"
	"testing"

	"github.com/matta-kelly/crate/internal/member"
)

func TestRunArgsCarryTheMembersLimitsAndReadOnlyRoot(t *testing.T) {
	s := &System{remote: func() bool { return false }}
	full := member.Spec{
		Name:             "stateball-stateball-app",
		Image:            "stateball:1",
		Network:          "stateball-stateball",
		Aliases:          []string{"stateball-stateball-app"},
		User:             "1000:1000",
		Userns:           "keep-id:uid=1000,gid=1000",
		Command:          []string{"serve"},
		Env:              []string{"A=B"},
		Mounts:           []member.Mount{{Source: "/data", Target: "/data", ReadOnly: true}},
		Volumes:          []member.Volume{{Name: "v", Path: "/v"}},
		Placement:        "crate-check-1.slice",
		ReadOnlyRoot:     true,
		DropCapabilities: []string{"ALL", "NET_RAW"},
		CPUs:             "3",
		Memory:           "2G",
	}
	tools := member.Tools(member.ToolsOptions{System: "stateball-stateball", Root: "/r", Image: "dev:1"})

	rows := []struct {
		name string
		spec member.Spec
		want [][]string
		not  []string
	}{
		{
			name: "a built app with its limits, a read-only root and dropped capabilities",
			spec: full,
			want: [][]string{
				{"--read-only"},
				{"--cap-drop", "ALL"},
				{"--cap-drop", "NET_RAW"},
				{"--cpus", "3"},
				{"--memory", "2G"},
				{"--cgroup-parent", "crate-check-1.slice"},
				{"--read-only"},
			},
		},
		{
			name: "the tools member sets no limit and keeps its root writable",
			spec: tools,
			want: [][]string{
				{"--user", "1000:1000"},
				{"--userns=keep-id:uid=1000,gid=1000"},
				{"--network", "stateball-stateball"},
				{"--network-alias", "stateball-stateball-tools"},
			},
			not: []string{"--read-only", "--cap-drop", "--cpus", "--memory"},
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			args := s.runArgs(row.spec)
			for _, want := range row.want {
				if !hasRunArgs(args, want...) {
					t.Errorf("runArgs: expected %v, got %v", want, args)
				}
			}
			for _, not := range row.not {
				if slices.Contains(args, not) || hasPrefix(args, not+"=") {
					t.Errorf("runArgs: expected no %s, got %v", not, args)
				}
			}
		})
	}
}

// A member with no slice runs under cgroupfs on a local engine. podman-remote refuses that flag, so a remote
// engine's run has none.
func TestRunArgsSetTheCgroupManagerOnlyOnALocalEngine(t *testing.T) {
	spec := member.Spec{Name: "a", Image: "i"}
	local := (&System{remote: func() bool { return false }}).runArgs(spec)
	if !slices.Contains(local, "--cgroup-manager=cgroupfs") {
		t.Errorf("runArgs on a local engine: expected --cgroup-manager=cgroupfs, got %v", local)
	}
	remote := (&System{remote: func() bool { return true }}).runArgs(spec)
	if hasPrefix(remote, "--cgroup-manager") {
		t.Errorf("runArgs on a remote engine: expected no --cgroup-manager, got %v", remote)
	}
}

// hasRunArgs says args holds the flag run and its values as consecutive words.
func hasRunArgs(args []string, run ...string) bool {
	if len(run) == 0 {
		return false
	}
	for i := 0; i+len(run) <= len(args); i++ {
		if slices.Equal(args[i:i+len(run)], run) {
			return true
		}
	}
	return false
}

func hasPrefix(args []string, prefix string) bool {
	for _, arg := range args {
		if len(arg) >= len(prefix) && arg[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
