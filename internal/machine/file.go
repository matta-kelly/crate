package machine

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"

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
	CPUs   string
	Memory string
	Count  Count
}

var (
	cpuList = regexp.MustCompile(`^[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*$`)
	memory  = regexp.MustCompile(`^[0-9]+[KMG]$`)
)

// Read reads the machine file at path. A path with no file gives nil and no error. A file that cannot be
// read, parsed or accepted is an error that names the file and the key.
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
	if cfg.CPUs, err = text("cpus", raw.CPUs, cpuList); err != nil {
		return nil, fmt.Errorf("machine file %s: %w", path, err)
	}
	if cfg.Memory, err = text("memory", raw.Memory, memory); err != nil {
		return nil, fmt.Errorf("machine file %s: %w", path, err)
	}
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

func text(key string, v any, form *regexp.Regexp) (string, error) {
	s, ok := v.(string)
	if !ok || !form.MatchString(s) {
		return "", fmt.Errorf("%s = %v: not a form crate reads", key, v)
	}
	return s, nil
}

func number(key string, v any) (int, error) {
	n, ok := v.(int64)
	if !ok || n < 1 {
		return 0, fmt.Errorf("%s = %v: expected a whole number of 1 or more", key, v)
	}
	return int(n), nil
}
