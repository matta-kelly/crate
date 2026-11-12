package member_test

import (
	"reflect"
	"testing"

	"github.com/matta-kelly/crate/internal/member"
)

// A folder of the checkout gets <system>-<slug>, dots gone and every other character but a-z and 0-9 made -.
func TestAVolumeIsTheSystemAndTheFoldersSlug(t *testing.T) {
	rows := []struct {
		system, folder, want string
	}{
		{"que-pasa-work", "node_modules", "que-pasa-work-node-modules"},
		{"que-pasa-work", ".venv", "que-pasa-work-venv"},
		{"que-pasa-work", ".next", "que-pasa-work-next"},
		{"que-pasa-work", "node_modules/.cache", "que-pasa-work-node-modules---cache"},
		{"que-pasa-work", "packages/web/node_modules", "que-pasa-work-packages-web-node-modules"},
		{"a", "..env", "a-env"},
	}
	for _, row := range rows {
		if got := member.VolumeName(row.system, row.folder); got != row.want {
			t.Errorf("VolumeName(%q, %q): expected %q, got %q", row.system, row.folder, row.want, got)
		}
	}
}

// The tools member: the dev image, the checkout at its host path, keep-id and its user, HOME, the volumes,
// the alias, the placement, and no limit of its own.
func TestToolsGivesTheToolsMemberItsSpec(t *testing.T) {
	root := "/home/mkultra/lot/daworks/stateball"
	spec := member.Tools(member.ToolsOptions{
		System:    "stateball-stateball",
		Root:      root,
		Image:     "ghcr.io/matta-kelly/stateball-dev:5408977388ce524d",
		Folders:   []string{"node_modules", ".venv"},
		Env:       map[string]string{"POSTGRES_HOST": "postgres"},
		Placement: "crate-check-1.slice",
	})

	rows := []struct {
		name string
		got  any
		want any
	}{
		{"the name", spec.Name, "stateball-stateball-tools"},
		{"the image", spec.Image, "ghcr.io/matta-kelly/stateball-dev:5408977388ce524d"},
		{"the user", spec.User, "1000:1000"},
		{"the user namespace", spec.Userns, "keep-id:uid=1000,gid=1000"},
		{"the network", spec.Network, "stateball-stateball"},
		{"the alias", spec.Aliases, []string{"stateball-stateball-tools"}},
		{"the placement", string(spec.Placement), "crate-check-1.slice"},
		{"the mount", spec.Mounts, []member.Mount{{Source: root, Target: root, ReadOnly: false}}},
		{"the volumes", spec.Volumes, []member.Volume{
			{Name: "stateball-stateball-node-modules", Path: root + "/node_modules"},
			{Name: "stateball-stateball-venv", Path: root + "/.venv"},
		}},
		{"the env", envOf(spec.Env), map[string]string{"HOME": "/home/node", "POSTGRES_HOST": "postgres"}},
		{"the CPU limit", spec.CPUs, ""},
		{"the memory limit", spec.Memory, ""},
	}
	for _, row := range rows {
		if !reflect.DeepEqual(row.got, row.want) {
			t.Errorf("%s: expected %v, got %v", row.name, row.want, row.got)
		}
	}
}

// A repo with no [tools] and a [tools] with no volumes both get node_modules.
func TestToolsVolumesDefaultToNodeModules(t *testing.T) {
	for name, folders := range map[string][]string{"no [tools]": nil, "a [tools] with no volumes": {}} {
		t.Run(name, func(t *testing.T) {
			spec := member.Tools(member.ToolsOptions{System: "s", Root: "/r", Image: "i", Folders: folders})
			want := []member.Volume{{Name: "s-node-modules", Path: "/r/node_modules"}}
			if !reflect.DeepEqual(spec.Volumes, want) {
				t.Errorf("the volumes: expected %v, got %v", want, spec.Volumes)
			}
		})
	}
}

// The env is HOME=/home/node and each [tools] env key.
func TestToolsEnvIsHomeAndTheDeclaredEnv(t *testing.T) {
	spec := member.Tools(member.ToolsOptions{
		System: "s", Root: "/r", Image: "i",
		Env: map[string]string{"ZED": "z", "ALPHA": "a"},
	})
	want := map[string]string{"HOME": "/home/node", "ALPHA": "a", "ZED": "z"}
	if got := envOf(spec.Env); !reflect.DeepEqual(got, want) {
		t.Errorf("the env: expected %v, got %v", want, got)
	}
}

// Built passes through what the app gives it and adds nothing of the tools member.
func TestBuiltGivesTheAppsMemberItsSpec(t *testing.T) {
	spec := member.Built(member.BuiltOptions{
		Name:      "stateball-stateball-app",
		Image:     "stateball:1",
		Network:   "stateball-stateball",
		Aliases:   []string{"stateball-stateball-app"},
		Command:   []string{"serve"},
		Env:       []string{"A=B"},
		Mounts:    []member.Mount{{Source: "/data", Target: "/data", ReadOnly: true}},
		Volumes:   []member.Volume{{Name: "v", Path: "/v"}},
		Placement: "crate-check-1.slice",
	})
	rows := []struct {
		name string
		got  any
		want any
	}{
		{"the name", spec.Name, "stateball-stateball-app"},
		{"the image", spec.Image, "stateball:1"},
		{"the network", spec.Network, "stateball-stateball"},
		{"the alias", spec.Aliases, []string{"stateball-stateball-app"}},
		{"the command", spec.Command, []string{"serve"}},
		{"the env", spec.Env, []string{"A=B"}},
		{"the mounts", spec.Mounts, []member.Mount{{Source: "/data", Target: "/data", ReadOnly: true}}},
		{"the volumes", spec.Volumes, []member.Volume{{Name: "v", Path: "/v"}}},
		{"the placement", string(spec.Placement), "crate-check-1.slice"},
	}
	for _, row := range rows {
		if !reflect.DeepEqual(row.got, row.want) {
			t.Errorf("%s: expected %v, got %v", row.name, row.want, row.got)
		}
	}
}

func envOf(env []string) map[string]string {
	got := map[string]string{}
	for _, e := range env {
		for i := 0; i < len(e); i++ {
			if e[i] == '=' {
				got[e[:i]] = e[i+1:]
				break
			}
		}
	}
	return got
}
