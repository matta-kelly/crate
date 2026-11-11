# crate

The shared CI every repo runs. A repo's main lives in a bare repo on a server, and crate's git hooks guard it: main moves
only to a commit whose check passed on the exact tree main becomes. A land is a plain `git push`.

- `hooks/pre-receive`: a push to main runs the repo's `scripts/check.sh` in its image, on the pushed commit, with
  `CHECK_BASE` set to the old main. Red refuses the push and sends the check's output to the pusher. One check at a time
  per repo (the merge queue); a push that waited while main moved is told to pull with rebase and push again
- `hooks/post-receive`: after main moves, push it to the remote `github`, the public copy
- `bin/crate apply <file|->`: make a server match its declaration: crate's version tag (`CRATE_VERSION`), where the bare repos live (`GIT_HOME`), where their copies go (`GITHUB_BASE`), and the repos (`REPOS`). Safe to run again: it moves this checkout to the version, inits each repo, and seeds a new one from GitHub
- `bin/crate init <bare> [<github url>]`: make or take a bare repo, and put the hooks and rules on it (fast-forward only, no deletion). With an ssh GitHub url, it makes the repo's own deploy key on this machine and prints its public half
- `lib/crate.sh`: what the hooks share

## A repo on crate

`crate.toml` at its root:

```toml
crate = "1"      # the crate version it runs
image = "dev"    # its own dev image (scripts/dev-image.sh local), or an image ref
timeout = "900"  # seconds a check may run; past it, the check is stopped and the push refused
```

and `scripts/check.sh`, which reads `CHECK_BASE` to run only the sets a change touched.

The check's checkout and its packages are kept between pushes (`<bare>/crate/work`, and the volume
`crate-<repo>-node-modules`), so a change that leaves the lockfile alone installs nothing.

## Changing crate

crate stays outside the gate it ships, so a broken hook never blocks its own fix. A change lands on GitHub's main after
`just check` passes, and reaches a server only by a release: `just release <major.minor.patch>` checks, tags the version,
and moves the major tag (`v1`) to it. A server runs crate at the major tag. A bad release is undone by moving the major
tag back to the last good version.

## Check

`just check`: shellcheck, then `test/hooks.test.sh` on real git and podman.
