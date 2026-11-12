package main

import (
	"context"
	"os"

	"github.com/matta-kelly/crate/internal/limit"
	"github.com/matta-kelly/crate/internal/system"
)

func init() { register("run", runVerb) }

const runUsage = "crate [-C <path>] run [--] <argv...>"

func runVerb(args []string) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return usagef("%s", runUsage)
	}
	w, err := openWorkspace()
	if err != nil {
		return err
	}
	s := w.system(system.Work, os.Stderr)
	made, err := s.Made()
	if err != nil {
		return err
	}
	if !made {
		if err := s.Hold(); err != nil {
			return err
		}
		if err := s.Image(); err != nil {
			_ = s.Remove(nil)
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), limit.Up())
		defer cancel()
		if err := s.Up(ctx); err != nil {
			return err
		}
	}
	code, err := s.Run(context.Background(), args, os.Stdout)
	if err != nil {
		return err
	}
	if code != 0 {
		return commandExit(code)
	}
	return nil
}
