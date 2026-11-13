package main

import (
	"os"

	"github.com/matta-kelly/crate/internal/system"
)

func init() { register("down", downVerb) }

const downUsage = "crate [-C <path>] down"

func downVerb(args []string) error {
	if len(args) > 0 {
		return usagef("%s", downUsage)
	}
	w, err := openWorkspace()
	if err != nil {
		return err
	}
	return w.system(system.Work, os.Stderr, nil).Remove(nil)
}
