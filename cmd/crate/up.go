package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/matta-kelly/crate/internal/limit"
	"github.com/matta-kelly/crate/internal/record"
	"github.com/matta-kelly/crate/internal/system"
)

func init() { register("up", up) }

const upUsage = "crate [-C <path>] up [--attempt] [-e <NAME>]..."

// up makes the checkout's workspace, or with --attempt an attempt's. Each -e NAME passes the host's NAME to the dev
// server by its name alone, as run -e does, so a secret's value is never in argv or crate.toml. The dev server is
// made again to take it. A NAME the host has not set is refused, named.
func up(args []string) error {
	flags := flag.NewFlagSet("up", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	attempt := flags.Bool("attempt", false, "")
	var pass []string
	flags.Func("e", "", func(name string) error { pass = append(pass, name); return nil })
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		return usagef("%s", upUsage)
	}
	for _, name := range pass {
		if !envName.MatchString(name) {
			return usagef("%s", upUsage)
		}
		if _, ok := os.LookupEnv(name); !ok {
			return fmt.Errorf("up -e %s: %s is not set here, so there is nothing to pass", name, name)
		}
	}
	w, err := openWorkspace()
	if err != nil {
		return err
	}
	kind := system.Work
	if *attempt {
		kind = system.Attempt
	}
	rec := record.Start(record.Person(), os.Stderr, record.Line{Kind: kind, Repo: w.repo, System: w.name, Commit: w.head()})
	err = upRecorded(w.system(kind, os.Stderr, rec, pass))
	if err != nil {
		rec.End(record.Fail, err.Error())
	} else {
		rec.End(record.Pass, "")
	}
	return err
}

func upRecorded(s *system.System) error {
	if err := s.Hold(); err != nil {
		return err
	}
	if err := s.Image(); err != nil {
		_ = s.Remove(nil)
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit.Up())
	defer cancel()
	return s.Up(ctx)
}
