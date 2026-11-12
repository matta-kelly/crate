# crate

The shared CI every repo runs. A repo's main lives in a bare repo on a server, and crate's git hooks guard it: main moves
only to a commit whose check passed on the exact tree main becomes. A land is a plain `git push`.

- `hooks/pre-receive`: a push to main runs the repo's `scripts/check.sh` in its image, on the pushed commit, with
  `CHECK_BASE` set to the old main. Red refuses the push and sends the check's output to the pusher. One check at a time
  per repo (the merge queue); a push that waited while main moved is told to pull with rebase and push again. A push
  whose commit the check's checkout cannot check out is refused, as is any push of a version tag (`v<semver>`,
  `<folder>-v<semver>`), in every repo: only crate makes those
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
  makes none until its first is tagged by hand, on the server, in the bare repo:
  `git -C <bare> tag v<version> <commit>`. A push of a version tag is refused, and says the same
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

## Install

crate (the Go binary) is one asset on a public GitHub release: `crate-linux-amd64.tar.gz`, at
`https://github.com/matta-kelly/crate/releases/download/v<version>/crate-linux-amd64.tar.gz`, and the same at the major
tag `v<major>`. It needs no token. Unpack it with no strip: `bin/crate` (a static binary), `VERSION` and `COMMIT` are
at the root. `crate --version` prints both.

- `just release <major.minor.patch>`: from a clean main equal to GitHub's, the check, the tag `v<version>`, the major
  tag moved, then the asset on the release `v<version>` and, replacing it, on the release `v<major>`. Run again after a
  failure, it goes on from the step that failed. `--dry-run` only builds `dist/crate-linux-amd64.tar.gz`
- `just rollback <major.minor.patch>`: the major tag and its asset back to that version. It refuses a version whose asset
  lacks `hooks/` when the major's asset has it

`bin/crate` in the repo stays the shell until the server moves to the binary. The built binary goes to `dist/`.

## crate dev-image

`crate [-C <path>] dev-image <hash|ref|ensure|promote|local> [--name <name>] [--rev <commit>]`: a repo's dev image in
ghcr, one per hash of its inputs. Each verb prints one line on stdout. Everything else goes to stderr, each reason in a
line that starts `crate:`. Exit 2 is a usage error, 1 a failure.

- `hash`: the first 16 hex of the sha256 of `git ls-tree -r <commit> -- <inputs>`
- `ref`: `<image>@<digest>` of `<image>:<hash>`, else of `<image>:main`
- `ensure`: build, smoke and push `<image>:<hash>` only when the registry lacks it, then print its ref
- `promote`: point `<image>:main` at `<image>:<hash>`
- `local`: put `<image>:<hash>` in this machine's store (its copy, else a pull, else a build and a smoke), then print the tag
- the image name, the first that is there: `[dev] image`; `name` in `crate.toml`; `--name`; the repo's folder (for a
  worktree, the folder of the checkout it comes from; for a bare repo, its folder without `.git`). All but the first
  are `ghcr.io/matta-kelly/<name>-dev`
- `[dev]` in `crate.toml`: `inputs` (the paths the hash covers), `containerfile` (the path `podman build -f` reads, one
  of `inputs`), `smoke` (a script run with the image as its one argument), `image`. Defaults: the three inputs of the
  template, `.devcontainer/Containerfile`, `scripts/dev-image-smoke.sh`
- `--rev <commit>`: `hash` and `ref` read the commit's tree, from any git folder that holds it, a bare one too. The other
  three build from a checkout and refuse it
- the registry: "manifest unknown" and "name unknown" read as a missing tag. ghcr says "denied" for a package that does
  not exist yet, so `ensure` reads it as a missing tag too (a new repo's first image is made), while `ref` and `promote`
  fail on it. Any other answer is a failure for every verb, and nothing is built or pushed on it
- the engine: `podman` and `skopeo` from the PATH, with the registry login they read. `CONTAINER_HOST` may name a remote
  engine. The smoke mounts the checkout, so the checkout's path must be the same on the engine's host
- the machine: `/etc/crate/machine.toml` (`CRATE_MACHINE` names another) gives a ceiling. It is placed in the user's
  systemd under the slice `CRATE_SLICE` names (crate by default): the machine's slice takes `AllowedCPUs`, `MemoryMax`
  and `MemorySwapMax=0`, and each kind's slice takes a `CPUWeight` that puts a person's work first, a check next, and
  postsubmit and attempts last. A `cpus` list outside `/sys/devices/system/cpu/possible` is refused with the file,
  the key and the CPUs the machine has, before systemd is asked. With no file, crate places nothing and a build says
  once on stderr that the machine has no ceiling. With `CONTAINER_HOST`, the engine is remote, so crate places nothing
  and says so once
- the slots: a system holds one slot of a kind for the places it makes. Each slot is a file under
  `$XDG_STATE_HOME/crate/slots/<kind>/` that names the system and the process holding it. A check, postsubmit or
  attempt waits past its count in `[count]`; work has no count. A slot is freed by `Release`, by the holder leaving
  before its network exists, or by the system's podman network going after the bind. A wait stops at `limit.Hold`
- limits: every call has one, in `internal/limit`. `CRATE_LIMIT_SCALE` (a whole number of 1 or more) divides them all,
  but a limit so divided is never less than 1 s, so a test that shortens a long limit does not make a short one too
  short to meet on a busy machine; with no factor, a limit is its value. Tests use it to make each limit pass in seconds

## A workspace

`crate [-C <path>] up [--attempt]`, `run [--] <argv...>`, `services`, `logs [<member>]`, `down`, `name`: a
checkout's running copy of its repo. Each gives the same from another folder with `-C <checkout>` as from inside it.

- the name: `<name>-<folder>`, from `crate.toml`'s `name` and the checkout's folder; with no `name` declared, the
  repo's folder is the name. The name must be a DNS label (`a-z`, `0-9`, `-`, 63 or fewer). It is the network, the
  pods, the containers and the volumes' prefix
- `up`: the network (with `--opt isolate=strict`), each service as a pod, the tools member, then `[tools] setup`. A
  second `up` changes nothing, and a stopped member starts again. An attempt (`--attempt`) has an internal network
  with no route outside, runs in a worktree of its own, and is refused in a checkout whose workspace is up
- `run`: every word after `run` (after an optional `--`) is the command's argv, its flags among them. It runs in the
  tools member as the checkout's user, in the caller's folder when the caller is inside the checkout, else at its
  root. With no workspace made, `run` makes it first. Its exit is the command's
- `services`: plays the services again, with the tools member left running. `logs <member>` reads a member's output
  (`tools`, or a service). `down`: removes the containers, pods, volumes and network of the workspace, and a second
  `down` changes nothing

`crate.toml`'s tables:

```toml
services = ".devcontainer/pod.yaml"  # the services file, or no services

[tools]
setup = "npm ci"  # run once when the tools member is made, as the checkout's user, at its root
volumes = ["node_modules"]  # the checkout's folders kept in volumes; ["node_modules"] when absent

[tools.env]
POSTGRES_HOST = "postgres"  # an env value of the tools member
```

The services file holds Pod documents only. Each is a Pod named `WORKSPACE-<service>`. `WORKSPACE` is replaced by
the workspace's name everywhere in the document, and each pod is played alone with `--network <name>:alias=<service>`,
so the tools member reaches its own `postgres` by name. A container may set no `resources.limits` or
`resources.requests`. A volume in a service document is `<name>-<key>`. The tools member's volumes are
`<name>-<slug>`, the folder with its leading dots taken away and each character not `a-z` or `0-9` made `-`.

## Check

`just check`: `go vet`, `go test ./...`, shellcheck on the shell that is left, then `test/hooks.test.sh`, `test/apply.test.sh`, `test/release.test.sh` and
`test/postsubmit.test.sh` on real git and podman.
