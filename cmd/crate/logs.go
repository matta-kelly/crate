package main

import (
	"os"

	"github.com/matta-kelly/crate/internal/system"
)

func init() { register("logs", logsVerb) }

const logsUsage = "crate [-C <path>] logs [<member>]"

func logsVerb(args []string) error {
	if len(args) > 1 {
		return usagef("%s", logsUsage)
	}
	member := "tools"
	if len(args) == 1 {
		member = args[0]
	}
	w, err := openWorkspace()
	if err != nil {
		return err
	}
	return w.system(system.Work, os.Stdout).Logs(member, os.Stdout)
}
