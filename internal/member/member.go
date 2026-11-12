// Package member makes every member of every system from one spec: the image, the user, the mounts, the
// volumes, the env, the aliases, the network, the placement, the command, and whether the root is read-only
// and the capabilities dropped. Two presets, the only two: Tools and Built.
package member

import "github.com/matta-kelly/crate/internal/machine"

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

// Tools makes the tools member of a system: the dev image, the checkout at its host path, keep-id, HOME,
// the checkout's volumes and its [tools] env, and no limit of its own.
func Tools(ToolsOptions) Spec { return Spec{} }

// Built makes a built app member of a system.
func Built(BuiltOptions) Spec { return Spec{} }

// VolumeName is <system>-<slug>, the slug the folder with its leading dots taken away and each character
// not a-z or 0-9 made -.
func VolumeName(system, folder string) string { return "" }
