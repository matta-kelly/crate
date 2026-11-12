// Package declaration reads a repo's crate.toml.
package declaration

import (
	"bytes"
	"errors"
	"fmt"
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
}

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
	return d, nil
}
