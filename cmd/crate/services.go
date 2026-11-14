package main

import (
	"os"

	"github.com/matta-kelly/crate/internal/system"
)

func init() { register("services", servicesVerb) }

const servicesUsage = "crate [-C <path>] services"

func servicesVerb(args []string) error {
	if len(args) > 0 {
		return usagef("%s", servicesUsage)
	}
	w, err := openWorkspace()
	if err != nil {
		return err
	}
	return w.system(system.Work, os.Stderr, nil, nil).Services()
}
