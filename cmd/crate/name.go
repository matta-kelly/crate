package main

import "fmt"

func init() { register("name", nameVerb) }

const nameUsage = "crate [-C <path>] name"

func nameVerb(args []string) error {
	if len(args) > 0 {
		return usagef("%s", nameUsage)
	}
	w, err := openWorkspace()
	if err != nil {
		return err
	}
	fmt.Println(w.name)
	return nil
}
