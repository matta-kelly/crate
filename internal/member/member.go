// Package member makes every member of every system from one spec: the image, the user, the mounts, the
// volumes, the env, the aliases, the network, the placement, the command, and whether the root is read-only
// and the capabilities dropped. Two presets, the only two: Tools and Built.
package member

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/matta-kelly/crate/internal/machine"
)

// Mount is a host folder at the same path inside, read-only or not.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// Volume is a named volume at a path inside.
type Volume struct {
	Name string
	Path string
}

// Spec is one member of one system. A field it does not set is the preset's or the image's.
type Spec struct {
	Name             string
	Image            string
	User             string
	Userns           string
	Command          []string
	Env              []string
	Mounts           []Mount
	Volumes          []Volume
	Aliases          []string
	Network          string
	Placement        machine.Placement
	ReadOnlyRoot     bool
	DropCapabilities []string
	CPUs             string
	Memory           string
}

// ToolsOptions is what a tools member is made from.
type ToolsOptions struct {
	System    string
	Root      string
	Image     string
	Folders   []string
	Env       map[string]string
	Placement machine.Placement
}

// BuiltOptions is what a built app member is made from.
type BuiltOptions struct {
	Name      string
	Image     string
	Network   string
	Aliases   []string
	Command   []string
	Env       []string
	Mounts    []Mount
	Volumes   []Volume
	Placement machine.Placement
}

// defaultFolder is the tools member's volume when [tools] volumes is absent.
const defaultFolder = "node_modules"

// Tools makes the tools member of a system: the dev image, the checkout at its host path, keep-id, HOME,
// the checkout's volumes and its [tools] env, and no limit of its own.
func Tools(o ToolsOptions) Spec {
	folders := o.Folders
	if len(folders) == 0 {
		folders = []string{defaultFolder}
	}
	volumes := make([]Volume, len(folders))
	for i, folder := range folders {
		volumes[i] = Volume{Name: VolumeName(o.System, folder), Path: filepath.Join(o.Root, folder)}
	}
	env := []string{"HOME=/home/node"}
	keys := make([]string, 0, len(o.Env))
	for key := range o.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+o.Env[key])
	}
	return Spec{
		Name:      o.System + "-tools",
		Image:     o.Image,
		User:      "1000:1000",
		Userns:    "keep-id:uid=1000,gid=1000",
		Command:   []string{"sleep", "infinity"},
		Env:       env,
		Mounts:    []Mount{{Source: o.Root, Target: o.Root}},
		Volumes:   volumes,
		Aliases:   []string{o.System + "-tools"},
		Network:   o.System,
		Placement: o.Placement,
	}
}

// Built makes a built app member of a system.
func Built(o BuiltOptions) Spec {
	return Spec{
		Name:      o.Name,
		Image:     o.Image,
		Network:   o.Network,
		Aliases:   o.Aliases,
		Command:   o.Command,
		Env:       o.Env,
		Mounts:    o.Mounts,
		Volumes:   o.Volumes,
		Placement: o.Placement,
	}
}

// VolumeName is <system>-<slug>, the slug the folder with its leading dots taken away and each character
// not a-z or 0-9 made -.
func VolumeName(system, folder string) string {
	folder = strings.TrimLeft(folder, ".")
	var slug strings.Builder
	for _, r := range folder {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			slug.WriteRune(r)
		} else {
			slug.WriteByte('-')
		}
	}
	return system + "-" + slug.String()
}
