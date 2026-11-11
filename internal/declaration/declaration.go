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
	Dev               Dev    `toml:"dev"`
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
