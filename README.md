# crate

The shared CI every repo runs. A repo's main lives in a bare repo on a server, and crate's git hooks guard it: main moves
only to a commit whose check passed on the exact tree main becomes. A land is a plain `git push`.

- `hooks/pre-receive`: a push to main runs the repo's `scripts/check.sh` in its image, on the pushed commit, with
  `CHECK_BASE` set to the old main. Red refuses the push and sends the check's output to the pusher. One check at a time
  per repo (the merge queue); a push that waited while main moved is told to pull with rebase and push again
- `hooks/post-receive`: after main moves, push it to the remote `github`, the public copy, then release it
  ([Release](#release)). A repo with postsubmit runs it in the background first, and is released only when it passes
  ([Postsubmit](#postsubmit))
- `bin/crate postsubmit <bare>`: the repo's postsubmit on main as it stands; post-receive starts it
- `bin/crate apply <file|->`: make a server match its declaration: crate's version tag (`CRATE_VERSION`), where the bare repos live (`GIT_HOME`), where their copies go (`GITHUB_BASE`), and the repos (`REPOS`). Safe to run again: it moves this checkout to the version, inits each repo, and seeds a new one from GitHub
- `bin/crate init <bare> [<github url>]`: make or take a bare repo, and put the hooks and rules on it (fast-forward only, no deletion). With an ssh GitHub url, it makes the repo's own deploy key on this machine and prints its public half
- `lib/crate.sh`: what the hooks share

## A repo on crate

`crate.toml` at its root:

```toml
crate = "1"      # the crate version it runs
image = "dev"    # its own dev image (scripts/dev-image.sh local), or an image ref
timeout = "900"  # seconds a check may run; past it, the check is stopped and the push refused
ship = "npm"     # how a version ships; a repo that names none has no versions
packages = ". it"  # the package folders, "." when it names none
postsubmit = "e2e" # the slow sets, run as scripts/check.sh <sets> after each land; none when it names none
postsubmit_timeout = "3600"  # seconds postsubmit may run
```

and `scripts/check.sh`, which reads `CHECK_BASE` to run only the sets a change touched.

The check's checkout and its packages are kept between pushes (`<bare>/crate/work`, and the volume
`crate-<repo>-check-node-modules`), so a change that leaves the lockfile alone installs nothing.

## Release

Every move of main is a version of each package whose commits ask for one, read from the commit types (Conventional
Commits 1.0) as semver (SemVer 2.0). The tag is the truth: package.json's version is never committed back.

- the bump: the strongest commit since the package's last version tag wins
  - a breaking change (`feat!:`, or a `BREAKING CHANGE:` footer): major
  - `feat`: minor
  - `fix`, `perf`: patch
  - any other type: no version
- before 1.0.0: each bump moves down one place, as npm's caret reads a 0.x version: a breaking change bumps minor, and
  `feat` bumps patch (release-please's `bump-minor-pre-major` and `bump-patch-for-minor-pre-major`). A package leaves
  0.x only by a `v1.0.0` tag made by hand
- the tag: `v<version>` for the package at `.`, `<folder>-v<version>` for one in a folder. A package's commits are the
  ones that touch its folder; for `.`, the ones that touch no other package's folder. A package with no version tag yet
  makes none until its first is tagged by hand
- the ship: crate tags main and copies the version tags to GitHub. The repo's own tag-triggered workflow publishes
  (for `npm`, by npm's trusted publishing: no token, with provenance). GitHub's tag ruleset lets the deploy key make
  version tags

## Postsubmit

The slow sets, too slow to hold a land, run on each new main after it moves, in the background, one run at a time. A
run that waited tests main as it stands, so it covers every land since the last.

- green: main is the last green, and is released, so no version is made from a main postsubmit failed
- red: one commit on main undoes every commit since the last green main, landed through the check like any push. The
  failure is kept: its last lines in the revert's message, and all of it in `<bare>/crate/postsubmit/<sha>.log`
- a main whose tree is the last green's (the revert itself) passes with no run
- a revert that does not apply, or no green main to go back to, leaves main as it is, and the log says it needs a hand

## Changing crate

crate stays outside the gate it ships, so a broken hook never blocks its own fix. A change lands on GitHub's main after
`just check` passes, and reaches a server only by a release: `just release <major.minor.patch>` checks, tags the version,
and moves the major tag (`v1`) to it. A server runs crate at the major tag. A bad release is undone by moving the major
tag back to the last good version.

## Check

`just check`: shellcheck, then `test/hooks.test.sh`, `test/apply.test.sh`, `test/release.test.sh` and
`test/postsubmit.test.sh` on real git and podman.
