# crate

crate makes a repo's workspace: the running system of one change, from the repo's `crate.toml`. A person, a test, an
agent and the check each ask that system whether the change works. crate also guards each repo's main on the server.

## A workspace

```
<name>-<folder>          the workspace's name: crate.toml's name and the checkout's folder (the name alone
                         when the folder is the name)
├── network              its own (isolate=strict); an attempt's has no route outside
├── services             a pod each, reached by name (postgres)
├── tools                the dev image and the checkout: where commands run
└── app                  the change as it runs, reached as app
    └── routes           <name>-<folder>.localhost, <port name>.<name>-<folder>.localhost
```

`crate [-C <checkout>] <verb>`:

- `up [--attempt]`: make it, or start what stopped. A second `up` changes nothing. An attempt runs in a worktree of its
  own, with no route
- `run [--] <argv...>`: run a command in the tools member, in the caller's folder. Its exit is the command's
- `url`: the workspace's URLs, one per line
- `logs [tools|app|<service>]`: a member's output
- `services`: play the services again
- `down`: remove all of it. A second `down` changes nothing
- `name`: print the workspace's name

The app, by kind:

- a person's workspace and an attempt: `[app] command`, the dev server, in the dev image with the checkout
- a check and postsubmit: `[app] build`, the image that ships, run as production runs it: user 1000, a read-only root,
  every capability dropped, no new privileges
- each waits until `[app] health` passes inside the app (as a quadlet's `HealthCmd`), for at most a minute. An app that
  stops, or is not healthy in time, fails `up` with its last output

## crate.toml

```toml
name = "publicpersona"                 # the workspace's name; the repo's folder when absent
services = ".devcontainer/pod.yaml"    # the services file, or no services

[tools]
setup = "pnpm install --frozen-lockfile"   # run once when the tools member is made
volumes = ["node_modules", ".next"]        # checkout folders kept in volumes; ["node_modules"] when absent
env = { POSTGRES_HOST = "postgres" }

[app]
command = "pnpm dev"                       # the dev server
build = "Dockerfile"                       # the image that ships
health = "wget -qO- http://127.0.0.1:3000/api/health"
env = { NODE_ENV = "production", PORT = "3000", IMMICH_URL = "http://127.0.0.1:9" }   # the built app's env
needs = ["IMMICH_URL"]                     # the binding: env every place that runs the app must give

[[app.ports]]                              # the first is also the workspace's own host
name = "web"
port = 3000

[dev]                                      # the dev image: the paths its hash covers
inputs = [".devcontainer/Containerfile", "pnpm-lock.yaml", "package.json"]
```

- services: Pod documents, each named `WORKSPACE-<service>`. `WORKSPACE` becomes the workspace's name, and each pod
  joins the network as `<service>`. No `resources` limits. A volume is `<name>-<key>`
- the tools member's volumes: `<name>-<folder>`, the folder's leading dots taken away and each other character not
  `a-z` or `0-9` made `-`
- needs: the shape of the app's binding. The image and the declaration are the same everywhere; each place gives
  the needs its own way: a workspace and an attempt by `[tools] env`, a check by `[app] env`, production by its
  own. A place that lacks one fails `up` and names it. A check first runs the built image without them, and passes
  only if it stops non-zero and its output names each one
- a built app needs at least one port. The dev image needs the tool `health` calls (`wget`) when the dev server runs it
- the gate's keys: `crate`, `image`, `timeout`, `ship`, `packages`, `postsubmit`, `postsubmit_timeout` ([The gate](#the-gate))

## The machine

The machine's ceiling is root's, on `user.slice` (dazoo on the server, deeotee on the laptop): a memory cap, a soft line
under it, and systemd-oomd. crate sets no limit. It only orders and counts its own work:

- the order: each kind gets a slice under `CRATE_SLICE` (`crate`) with a `CPUWeight`: a person's work, then a check,
  then postsubmit and attempts
- the count: `/etc/crate/machine.toml` (`CRATE_MACHINE`) `[count]` says how many checks, postsubmits and attempts run
  at once. Past it, one waits its turn, at most an hour. A slot is a file under `$XDG_STATE_HOME/crate/slots/<kind>/`
- no file, or a remote engine (`CONTAINER_HOST`): crate places nothing, and says so once

## The dev image

`crate dev-image <hash|ref|ensure|promote|local> [--name <name>] [--rev <commit>]`: one image per hash of the repo's
`[dev] inputs`, in ghcr as `ghcr.io/matta-kelly/<name>-dev`.

- `hash`: the hash of the inputs at the commit
- `ref`: `<image>@<digest>` of `<image>:<hash>`, else of `<image>:main`. Never builds
- `ensure`: build, smoke and push `<image>:<hash>` when the registry lacks it
- `promote`: point `<image>:main` at `<image>:<hash>`
- `local`: put `<image>:<hash>` in this machine's store: its copy, a pull, or a build with no push

## The gate

A repo's main lives in a bare repo on the server. crate's hooks move it only to a commit whose check passed on the
exact tree main becomes. A land is a plain `git push`.

- `pre-receive`: makes the pushed commit a workspace of kind check (its services, its tools member on the repo's
  `image`, its built app), then runs `scripts/check.sh` in it with `CHECK_BASE` the old main, all within `timeout`
  seconds. Red refuses the push. One check at a time per repo, and the machine's count across repos. The workspace is
  removed after, all but its tools volumes, which stay warm. A push of a version tag is refused
- `post-receive`: copies main to GitHub, runs `postsubmit` if the repo has one, then releases
- postsubmit: the slow sets, in a workspace of kind postsubmit, after main moves, one run at a time. Red reverts main
  to the last green, through the check
- release: each move of main tags a version of each of `packages` whose commits ask for one (Conventional Commits as
  semver; before 1.0.0 each bump moves down a place). The tag is the truth, and `ship` names how it publishes. A
  package's first version is tagged by hand in the bare repo: `git -C <bare> tag v<version> <commit>`
- `crate init <bare> [<github url>]`: put the hooks on a bare repo. `crate apply <file|->`: make a server match its
  declaration (`GIT_HOME`, `GITHUB_BASE`, `REPOS`). crate's own version is its install's

`hooks/` holds two one-line scripts that run `crate hook <name>` from the install beside them. The gate's state lives
beside each bare repo, in `<bare>/crate`: the check's checkout, its lock, its log, and postsubmit's.

## Changing crate

crate stays outside the gate it ships, so a broken hook never blocks its own fix. A change lands on GitHub's main after
`just check` passes: `go vet`, `go test ./...`, shellcheck, then the gate's tests in `test/`, run on real git and
podman against the built install (`just build`, in `dist/crate`).

- `just release <major.minor.patch>`: check, tag, move the major tag (`v2`), and put `crate-linux-amd64.tar.gz`
  (`bin/crate`, `hooks/`, `VERSION`, `COMMIT`) on the release. A machine runs crate at the major tag
- `just rollback <major.minor.patch>`: move the major tag back
- every call has a limit, in `internal/limit`. `CRATE_LIMIT_SCALE` divides them all, never below 1 s, for tests
