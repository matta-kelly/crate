package main

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/matta-kelly/crate/internal/machine"
	"github.com/matta-kelly/crate/internal/router"
)

func init() { register("url", urlVerb) }

const urlUsage = "crate [-C <path>] url"

// urlVerb prints the workspace's URLs, one per line: its own host, then one per port. A workspace with no
// routes prints nothing.
func urlVerb(args []string) error {
	if len(args) > 0 {
		return usagef("%s", urlUsage)
	}
	w, err := openWorkspace()
	if err != nil {
		return err
	}
	urls, err := router.New(machine.Default()).URLs(w.name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, u := range urls {
		fmt.Println(u)
	}
	return nil
}
