package machine

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

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

// Line is crate.slice's memory line: a share of the machine's memory, or a size in bytes. Exactly one is set.
type Line struct {
	Percent int    // 1 to 100
	Bytes   uint64 // a size
}

// Of is the line in bytes on a machine with total bytes of memory.
func (l Line) Of(total uint64) uint64 {
	if l.Bytes > 0 {
		return l.Bytes
	}
	return total * uint64(l.Percent) / 100
}

// DefaultLine is crate.slice's memory line when the machine file names none: a quarter of the machine's memory.
var DefaultLine = Line{Percent: 25}

// Config is the machine file: how many of each kind run at once, and crate.slice's memory line.
type Config struct {
	Count Count
	Line  Line
}

// Defaults is the machine of no machine file: one of each counted kind at once, and the default line.
func Defaults() *Config {
	return &Config{Count: Count{Check: 1, Postsubmit: 1, Attempt: 1}, Line: DefaultLine}
}

// Read reads the machine file at path. A path with no file gives nil and no error. A file that cannot be read,
// parsed or accepted is an error that names the file and the key. The file holds crate's keys only: [count], and
// [slice] memory, the line crate draws under its own work.
func Read(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("machine file: %w", err)
	}
	var raw struct {
		Count map[string]any `toml:"count"`
		Slice struct {
			Memory string `toml:"memory"`
		} `toml:"slice"`
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&raw); err != nil {
		var unknown *toml.StrictMissingError
		if errors.As(err, &unknown) {
			keys := make([]string, len(unknown.Errors))
			for i, e := range unknown.Errors {
				keys[i] = strings.Join(e.Key(), ".")
			}
			return nil, fmt.Errorf("machine file %s: crate does not know the key %s; the file holds [count] and [slice] memory", path, strings.Join(keys, ", "))
		}
		return nil, fmt.Errorf("machine file %s: %w", path, err)
	}
	cfg := &Config{Line: DefaultLine}
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
	if raw.Slice.Memory != "" {
		if cfg.Line, err = ParseLine(raw.Slice.Memory); err != nil {
			return nil, fmt.Errorf("machine file %s: [slice] memory: %w", path, err)
		}
	}
	return cfg, nil
}

// ParseLine reads a memory line: a share, "25%", or a size, "8G" (K, M, G or T, each 1024 of the one before).
func ParseLine(text string) (Line, error) {
	if share, ok := strings.CutSuffix(text, "%"); ok {
		n, err := strconv.Atoi(share)
		if err != nil || n < 1 || n > 100 {
			return Line{}, fmt.Errorf("%q: expected a share from 1%% to 100%%", text)
		}
		return Line{Percent: n}, nil
	}
	units := map[byte]uint64{'K': 1 << 10, 'M': 1 << 20, 'G': 1 << 30, 'T': 1 << 40}
	if len(text) > 1 {
		if unit, ok := units[text[len(text)-1]]; ok {
			if n, err := strconv.ParseUint(text[:len(text)-1], 10, 64); err == nil && n > 0 {
				return Line{Bytes: n * unit}, nil
			}
		}
	}
	return Line{}, fmt.Errorf("%q: expected a share (25%%) or a size (8G)", text)
}

func number(key string, v any) (int, error) {
	n, ok := v.(int64)
	if !ok || n < 1 {
		return 0, fmt.Errorf("%s = %v: expected a whole number of 1 or more", key, v)
	}
	return int(n), nil
}

// Window is how many systems of kind the machine lets run at once: its file's [count], the defaults' with no file, and
// 1 when the file cannot be read (Hold then says why). The merge queue checks as many changes side by side.
func Window(kind string) int {
	cfg, err := Read(Path())
	if err != nil {
		return 1
	}
	if cfg == nil {
		cfg = Defaults()
	}
	return map[string]int{Check: cfg.Count.Check, Postsubmit: cfg.Count.Postsubmit, Attempt: cfg.Count.Attempt}[kind]
}
