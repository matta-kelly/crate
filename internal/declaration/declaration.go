// Package declaration reads a repo's crate.toml.
package declaration

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Dev is the [dev] table. A key it does not hold is empty.
type Dev struct {
	Image         string   `toml:"image"`
	Containerfile string   `toml:"containerfile"`
	Inputs        []string `toml:"inputs"`
	Smoke         string   `toml:"smoke"`
}

// Tools is the [tools] table: the tools member's env, the checkout's volumes, and the setup that runs in it.
type Tools struct {
	Env     map[string]string `toml:"env"`
	Volumes any               `toml:"volumes"`
	Setup   string            `toml:"setup"`
}

// Folders is [tools] volumes as the checkout's folders. An absent key is no folders, and the caller's
// default; a key that is not a list of strings is an error that names it.
func (t Tools) Folders() ([]string, error) {
	switch v := t.Volumes.(type) {
	case nil:
		return nil, nil
	case []any:
		folders := make([]string, len(v))
		for i, e := range v {
			folder, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("crate.toml: [tools] volumes: %v is not a folder", e)
			}
			folders[i] = folder
		}
		return folders, nil
	default:
		return nil, fmt.Errorf("crate.toml: [tools] volumes is %v: expected a list of the checkout's folders", v)
	}
}

// Port is one of the app's ports: its name, the label of its route, and its number inside the app.
type Port struct {
	Name string `toml:"name"`
	Port int    `toml:"port"`
}

// App is the [app] table: how the repo's app runs. build is the image that ships, which a check and postsubmit
// run as production does; command is the dev server, which a person's workspace and an attempt run in the dev
// image; health is the command run inside the app that says it is ready, as a quadlet's HealthCmd; env is the
// built app's env in crate's runs; ports are its ports, the first the workspace's own host. needs is the shape of
// the app's binding: the env each place that runs it must give (the workspace by [tools] env, a check by env,
// production by its own), and without which the built image refuses to start.
type App struct {
	Build   string            `toml:"build"`
	Command string            `toml:"command"`
	Health  string            `toml:"health"`
	Env     map[string]string `toml:"env"`
	Ports   []Port            `toml:"ports"`
	Needs   []string          `toml:"needs"`
}

// Declaration is a crate.toml. A flat key it does not hold is empty.
type Declaration struct {
	Crate             string `toml:"crate"`
	Image             string `toml:"image"`
	Timeout           string `toml:"timeout"`
	Ship              string `toml:"ship"`
	Packages          string `toml:"packages"`
	Postsubmit        string `toml:"postsubmit"`
	PostsubmitTimeout string `toml:"postsubmit_timeout"`
	Name              string `toml:"name"`
	Services          string `toml:"services"`
	Dev               Dev    `toml:"dev"`
	Tools             Tools  `toml:"tools"`
	App               App    `toml:"app"`
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Parse reads the text of a crate.toml. A key it does not know is an error that names it.
func Parse(data []byte) (Declaration, error) {
	var d Declaration
	dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		var unknown *toml.StrictMissingError
		if errors.As(err, &unknown) {
			keys := make([]string, len(unknown.Errors))
			for i, e := range unknown.Errors {
				keys[i] = strings.Join(e.Key(), ".")
			}
			return Declaration{}, fmt.Errorf("crate.toml: unknown key %s", strings.Join(keys, ", "))
		}
		return Declaration{}, fmt.Errorf("crate.toml: %w", err)
	}
	if d.App.Build != "" && len(d.App.Ports) == 0 {
		return Declaration{}, fmt.Errorf("crate.toml: [app] build has no [[app.ports]]: a built app must say the port it serves")
	}
	for _, need := range d.App.Needs {
		if !envName.MatchString(need) {
			return Declaration{}, fmt.Errorf("crate.toml: [app] needs %q: expected an env name (A-Z, a-z, 0-9 and _, not first a digit)", need)
		}
	}
	for _, p := range d.App.Ports {
		if p.Name == "" || p.Port < 1 || p.Port > 65535 {
			return Declaration{}, fmt.Errorf("crate.toml: [[app.ports]] %q %d: expected a name and a port from 1 to 65535", p.Name, p.Port)
		}
	}
	return d, nil
}
