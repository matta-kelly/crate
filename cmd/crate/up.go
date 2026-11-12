package main

import (
	"context"
	"flag"
	"io"
	"os"

	"github.com/matta-kelly/crate/internal/limit"
	"github.com/matta-kelly/crate/internal/system"
)

func init() { register("up", up) }

const upUsage = "crate [-C <path>] up [--attempt]"

func up(args []string) error {
	flags := flag.NewFlagSet("up", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	attempt := flags.Bool("attempt", false, "")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		return usagef("%s", upUsage)
	}
	w, err := openWorkspace()
	if err != nil {
		return err
	}
	kind := system.Work
	if *attempt {
		kind = system.Attempt
	}
	s := w.system(kind, os.Stderr)
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
