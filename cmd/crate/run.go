package main

import (
	"context"
	"fmt"
	"os"
	"regexp"

	"github.com/matta-kelly/crate/internal/limit"
	"github.com/matta-kelly/crate/internal/system"
)

func init() { register("run", runVerb) }

const runUsage = "crate [-C <path>] run [-e <NAME>]... [--] <argv...>"

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// runVerb runs argv in the tools member. Each -e NAME passes the host's NAME by its name alone, so a secret's value is
// never in argv, where ps shows it. A NAME the host has not set is refused, named.
func runVerb(args []string) error {
	var env []string
	for len(args) > 0 && args[0] == "-e" {
		if len(args) < 2 || !envName.MatchString(args[1]) {
			return usagef("%s", runUsage)
		}
		if _, ok := os.LookupEnv(args[1]); !ok {
			return fmt.Errorf("run -e %s: %s is not set here, so there is nothing to pass", args[1], args[1])
		}
		env = append(env, args[1])
		args = args[2:]
	}
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
	s := w.system(system.Work, os.Stderr, nil)
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
	code, err := s.Run(context.Background(), args, env, os.Stdout)
	if err != nil {
		return err
	}
	if code != 0 {
		return commandExit(code)
	}
	return nil
}
