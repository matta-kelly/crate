package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/matta-kelly/crate/internal/devimage"
	"github.com/matta-kelly/crate/internal/machine"
)

func init() { register("dev-image", devImage) }

const devImageUsage = "crate [-C <path>] dev-image <hash|ref|ensure|promote|local> [--name <name>] [--rev <commit>]"

// buildsFromACheckout are the sub-verbs that read the working tree, so a commit alone is not enough for them.
var buildsFromACheckout = []string{"ensure", "promote", "local"}

func devImage(args []string) error {
	if len(args) == 0 {
		return usagef("%s", devImageUsage)
	}
	sub := args[0]
	flags := flag.NewFlagSet("dev-image", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("name", "", "")
	rev := flags.String("rev", "", "")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() > 0 {
		return usagef("%s", devImageUsage)
	}
	if !slices.Contains([]string{"hash", "ref", "ensure", "promote", "local"}, sub) {
		return usagef("%s", devImageUsage)
	}
	if *rev != "" && slices.Contains(buildsFromACheckout, sub) {
		return usagef("dev-image %s builds from a checkout: --rev is for hash and ref", sub)
	}

	root, err := devimage.Toplevel(".")
	if err != nil {
		if *rev == "" {
			return err
		}
		root = "." // a bare repo holds the commit with no checkout
	}
	o := devimage.Options{Root: root, Name: *name, Rev: *rev, Stderr: os.Stderr}

	var result string
	switch sub {
	case "hash":
		result, err = devimage.Hash(o)
	case "ref":
		result, err = devimage.Ref(o)
	case "promote":
		result, err = devimage.Promote(o)
	default: // ensure and local start a container
		var placement machine.Placement
		if placement, err = placeBuild(); err != nil {
			return err
		}
		if sub == "ensure" {
			result, err = devimage.Ensure(o, placement)
		} else {
			result, err = devimage.Local(o, placement)
		}
	}
	if err != nil {
		return err
	}
	fmt.Println(result)
	return nil
}

// placeBuild is where this machine runs a dev image's build. It says once when the machine has no ceiling.
func placeBuild() (machine.Placement, error) {
	cfg, err := machine.Read(machine.Path())
	if err != nil {
		return "", err
	}
	if cfg == nil {
		fmt.Fprintf(os.Stderr, "crate: this machine has no ceiling (no %s): nothing limits a build\n", machine.Path())
	}
	return machine.Default().Place(machine.Work, machine.Build)
}
