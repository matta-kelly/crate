// Package declaration reads a repo's crate.toml. A stub: da-hammer builds it.
package declaration

// Dev is the [dev] table. A key it does not hold is empty.
type Dev struct {
	Image         string
	Containerfile string
	Inputs        []string
	Smoke         string
}

// Declaration is a crate.toml. A flat key it does not hold is empty.
type Declaration struct {
	Crate             string
	Image             string
	Timeout           string
	Ship              string
	Packages          string
	Postsubmit        string
	PostsubmitTimeout string
	Name              string
	Dev               Dev
}

// Parse reads the text of a crate.toml. A key it does not know is an error that names it.
func Parse(data []byte) (Declaration, error) { return Declaration{}, nil }
