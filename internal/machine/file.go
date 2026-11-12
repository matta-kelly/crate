package machine

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// DefaultFile is where a machine's file is, unless CRATE_MACHINE names another (a test does).
const DefaultFile = "/etc/crate/machine.toml"

// Path is the machine file of this process.
func Path() string {
	if p := os.Getenv("CRATE_MACHINE"); p != "" {
		return p
	}
	return DefaultFile
}

// Count is the [count] table of the machine file.
type Count struct {
	Check      int
	Postsubmit int
	Attempt    int
}

// Config is the machine file.
type Config struct {
	Count Count
}

// Read reads the machine file at path. A path with no file gives nil and no error. A file that cannot be
// read, parsed or accepted is an error that names the file and the key. A file that still holds the old
// cpus and memory is read and their values dropped: the machine's ceiling belongs to root.
func Read(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("machine file: %w", err)
	}
	var raw struct {
		CPUs   any            `toml:"cpus"`
		Memory any            `toml:"memory"`
		Count  map[string]any `toml:"count"`
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&raw); err != nil {
		return nil, fmt.Errorf("machine file %s: %w", path, err)
	}
	cfg := &Config{}
	for key, dst := range map[string]*int{"check": &cfg.Count.Check, "postsubmit": &cfg.Count.Postsubmit, "attempt": &cfg.Count.Attempt} {
		if *dst, err = number(key, raw.Count[key]); err != nil {
			return nil, fmt.Errorf("machine file %s: %w", path, err)
		}
	}
	for key := range raw.Count {
		if key != "check" && key != "postsubmit" && key != "attempt" {
			return nil, fmt.Errorf("machine file %s: [count] has the key %s, which crate does not know", path, key)
		}
	}
	return cfg, nil
}

func number(key string, v any) (int, error) {
	n, ok := v.(int64)
	if !ok || n < 1 {
		return 0, fmt.Errorf("%s = %v: expected a whole number of 1 or more", key, v)
	}
	return int(n), nil
}
