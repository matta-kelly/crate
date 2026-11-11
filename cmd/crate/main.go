// Command crate: the entry point. It reads -C once, then runs the verb the arguments name.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/matta-kelly/crate/internal/limit"
)

// version and commit are set by the release's -ldflags.
var (
	version = "dev"
	commit  = "unknown"
)

// verb runs with the arguments after its name. A usageError is the caller's mistake (exit 2), any other
// error a failure (exit 1).
type verb func(args []string) error

// verbs is the table every cut adds its verbs to: each verb's file registers it.
var verbs = map[string]verb{}

func register(name string, v verb) { verbs[name] = v }

type usageError string

func (e usageError) Error() string { return "usage: " + string(e) }

func usagef(format string, a ...any) error { return usageError(fmt.Sprintf(format, a...)) }

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	err := dispatch(args)
	if err == nil {
		return 0
	}
	fmt.Fprintf(os.Stderr, "crate: %v\n", err)
	var usage usageError
	if errors.As(err, &usage) {
		return 2
	}
	return 1
}

func dispatch(args []string) error {
	flags := flag.NewFlagSet("crate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("C", "", "")
	wantVersion := flags.Bool("version", false, "")
	if err := flags.Parse(args); err != nil {
		return usagef("crate [-C <path>] <%s> ...: %v", verbNames(), err)
	}
	if *wantVersion {
		fmt.Printf("crate %s %s\n", version, commit)
		return nil
	}
	if flags.NArg() == 0 {
		return usagef("crate [-C <path>] <%s> ...", verbNames())
	}
	run, ok := verbs[flags.Arg(0)]
	if !ok {
		return usagef("crate [-C <path>] <%s> ...: no verb %q", verbNames(), flags.Arg(0))
	}
	if *path != "" {
		if err := os.Chdir(*path); err != nil {
			return err
		}
	}
	if err := limit.CheckScale(); err != nil {
		return err
	}
	return run(flags.Args()[1:])
}

func verbNames() string {
	names := make([]string, 0, len(verbs))
	for name := range verbs {
		names = append(names, name)
	}
	slices.Sort(names)
	return strings.Join(names, "|")
}
