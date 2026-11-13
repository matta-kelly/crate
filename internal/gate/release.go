package gate

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Release: every move of main that a package's commits ask a bump for is a version of that package, read from the
// commit types (Conventional Commits 1.0) as semver (SemVer 2.0). The tag is the truth, never committed back.
// crate.toml's ship names how a version ships; a repo that names none has no versions. packages names the package
// folders, "." when it names none. The package at "." is tagged v<version>; one in a folder <folder>-v<version>. A
// package's commits are the ones that touch its folder, and, for ".", touch no other package's folder.

var (
	breaking = regexp.MustCompile(`^[a-z]+(\([^)]*\))?!:`)
	footer   = regexp.MustCompile(`(?m)^BREAKING[ -]CHANGE:`)
	feat     = regexp.MustCompile(`^feat(\([^)]*\))?:`)
	fixPerf  = regexp.MustCompile(`^(fix|perf)(\([^)]*\))?:`)
)

// Bump is the bump the commits ask for, from their subjects and bodies. The strongest wins: major for a breaking
// change (a ! after the type, or a BREAKING CHANGE: footer), minor for feat, patch for fix or perf, "" for none.
func Bump(subjects, bodies []string) string {
	for _, s := range subjects {
		if breaking.MatchString(s) {
			return "major"
		}
	}
	for _, b := range bodies {
		if footer.MatchString(b) {
			return "major"
		}
	}
	for _, s := range subjects {
		if feat.MatchString(s) {
			return "minor"
		}
	}
	for _, s := range subjects {
		if fixPerf.MatchString(s) {
			return "patch"
		}
	}
	return ""
}

// NextVersion is the version after v (major.minor.patch) for the bump. Before 1.0.0, each bump moves down one place,
// as npm's caret reads a 0.x version: a breaking change bumps minor, and feat bumps patch.
func NextVersion(v, bump string) string {
	parts := strings.SplitN(v, ".", 3)
	n := make([]int, 3)
	for i := range parts {
		n[i], _ = strconv.Atoi(parts[i])
	}
	if n[0] == 0 {
		switch bump {
		case "major":
			bump = "minor"
		case "minor":
			bump = "patch"
		}
	}
	switch bump {
	case "major":
		return fmt.Sprintf("%d.0.0", n[0]+1)
	case "minor":
		return fmt.Sprintf("%d.%d.0", n[0], n[1]+1)
	default:
		return fmt.Sprintf("%d.%d.%d", n[0], n[1], n[2]+1)
	}
}

// Release tags commit, main when it is "", with each package's next version, and copies the version tags to
// github. It holds the release lock, not the check's, so a push never waits on another's release.
func (r *Repo) Release(commit string, out io.Writer) {
	unlock, err := r.lock("release.lock")
	if err != nil {
		return
	}
	defer unlock()
	main := commit
	if main == "" {
		main = r.main()
	}
	if main == "" {
		return
	}
	decl := r.declarationAt(main)
	if decl.Ship == "" {
		return
	}
	packages := strings.Fields(decl.Packages)
	if len(packages) == 0 {
		packages = []string{"."}
	}
	for _, pkg := range packages {
		prefix := pkg + "-v"
		paths := []string{pkg}
		if pkg == "." {
			prefix = "v"
			for _, other := range packages {
				if other != "." {
					paths = append(paths, ":(exclude)"+other)
				}
			}
		}
		last, err := r.git("describe", "--tags", "--abbrev=0", "--match", prefix+"[0-9]*", main)
		if err != nil {
			fmt.Fprintf(out, "crate: %s has no %s<version> tag to count from; set its first version on the server: %s\n",
				pkg, prefix, r.FirstVersionCommand(prefix+"<version>"))
			r.log("release %s %s none: no tag to count from", r.Name(), pkg)
			continue
		}
		rng := last + ".." + main
		subjects, _ := r.git(append([]string{"log", "--format=%s", rng, "--"}, paths...)...)
		bodies, _ := r.git(append([]string{"log", "--format=%b%x00", rng, "--"}, paths...)...)
		bump := Bump(lines(subjects), strings.Split(bodies, "\x00"))
		if bump == "" {
			continue
		}
		version := prefix + NextVersion(strings.TrimPrefix(last, prefix), bump)
		if _, err := r.git("tag", version, main); err != nil {
			fmt.Fprintf(out, "crate: %v\n", err)
			continue
		}
		fmt.Fprintf(out, "crate: %s is %s, after %s\n", pkg, version, last)
		r.log("release %s %s %s", r.Name(), version, short(main))
	}
	// Every version tag, so one a failed copy left behind goes with the next.
	if !r.hasGithub() {
		return
	}
	for _, pkg := range packages {
		prefix := pkg + "-v"
		if pkg == "." {
			prefix = "v"
		}
		if _, err := r.git("push", "-q", "github", "refs/tags/"+prefix+"*:refs/tags/"+prefix+"*"); err != nil {
			r.log("release %s %s copy fail", r.Name(), prefix)
			fmt.Fprintln(out, "crate: the copy of the version tags to github failed; the next move copies again")
		}
	}
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
