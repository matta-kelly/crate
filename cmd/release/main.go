// Command release puts a version of crate on GitHub, outside the merge queue it ships, so a broken hook never blocks
// its own fix. `release publish <version>` checks, tags v<version>, moves the major tag, and puts the asset on
// the releases of both. `release rollback <version>` moves the major back to an earlier version.
// It is run by `just release` and `just rollback`.
package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"

	"github.com/matta-kelly/crate/internal/limit"
)

// repo is crate's home on GitHub. It is public, so its asset needs no token.
const repo = "matta-kelly/crate"

type usageError string

func (e usageError) Error() string { return "usage: " + string(e) }

var semver = regexp.MustCompile(`^([0-9]+)\.[0-9]+\.[0-9]+$`)

func main() {
	err := dispatch(os.Args[1:])
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "release: %v\n", err)
	var usage usageError
	if errors.As(err, &usage) {
		os.Exit(2)
	}
	os.Exit(1)
}

func dispatch(args []string) error {
	const usage = "release publish <major.minor.patch> [--dry-run] | release rollback <major.minor.patch>"
	if len(args) < 2 {
		return usageError(usage)
	}
	if err := limit.CheckScale(); err != nil {
		return err
	}
	m := semver.FindStringSubmatch(args[1])
	if m == nil {
		return usageError(args[1] + " is not major.minor.patch")
	}
	v := version{number: args[1], tag: "v" + args[1], major: "v" + m[1]}
	switch {
	case args[0] == "publish" && len(args) == 2:
		return publish(v, false)
	case args[0] == "publish" && len(args) == 3 && args[2] == "--dry-run":
		return publish(v, true)
	case args[0] == "rollback" && len(args) == 2:
		return rollback(v)
	}
	return usageError(usage)
}

// version is the one a release names: 2.0.0, its tag v2.0.0 and its major tag v2.
type version struct{ number, tag, major string }
