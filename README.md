# crate

crate makes a repo's workspace: the running system of one change, from the repo's `crate.toml`. A person, a test, an
agent and the check each ask that system whether the change works. crate also keeps each repo's main on the server,
through a merge queue.

## A workspace

```
<name>-<folder>          the workspace's name: crate.toml's name and the checkout's folder (the name alone
                         when the folder is the name)
├── network              its own (isolate=strict); an attempt's has no route outside
├── services             a pod each, reached by name (postgres)
├── tools                the dev image and the checkout: where commands run
└── app                  the change as it runs, reached as app
    └── routes           <name>-<folder>.localhost:8080, <port name>.<name>-<folder>.localhost:8080
```

`crate [-C <checkout>] <verb>`:

- `up [--attempt]`: make it, or start what stopped. A second `up` changes nothing. An attempt runs in a worktree of its
  own, with no route. Each `up` is a run on the person's [record](#the-record)
- `run [-e NAME]... [--] <argv...>`: run a command in the tools member, in the caller's folder. Its exit is the
  command's. Each `-e NAME` passes this shell's `NAME` by name, so a secret's value is never in argv
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
name = "publicpersona"                 # the repo's name: its workspace's and its dev image's
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
- the tools member reaches the app as `app`, and by `CRATE_APP_URL` (`http://<name>-<folder>-app:<first port>`). A
  browser test opens `CRATE_APP_URL`: Chrome sends the top-level domain `app` to https
- a built app needs at least one port. The dev image needs the tool `health` calls (`wget`) when the dev server runs it
- the merge queue's keys: `crate`, `image`, `timeout`, `batch`, `ship`, `packages`, `postsubmit`, `postsubmit_timeout`
  ([The merge queue](#the-merge-queue))

## The machine

The machine's ceiling is root's, on `user.slice` (dazoo on the server, deeotee on the laptop): a memory cap, a soft line
under it, and systemd-oomd. crate sets no limit. It only orders and counts its own work:

- the order: each kind gets a slice under `CRATE_SLICE` (`crate`) with a `CPUWeight`: a person's work, then a check,
  then postsubmit and attempts
- the count: `/etc/crate/machine.toml` (`CRATE_MACHINE`) `[count]` says how many checks, postsubmits and attempts run
  at once. Past it, one waits its turn, at most an hour. A slot is a file under `$XDG_STATE_HOME/crate/slots/<kind>/`
- no file, or a remote engine (`CONTAINER_HOST`): crate places nothing, and says so once

## The dev image

`crate dev-image <hash|ref|ensure|promote|local> [--rev <commit>]`: one image per hash of the repo's `[dev] inputs`,
in ghcr as `ghcr.io/matta-kelly/<name>-dev` (or `[dev] image`). A repo declares `name` and `[dev] inputs`; crate
guesses neither, and refuses a `crate.toml` that lacks one, in the queue too.

- `hash`: the hash of the inputs at the commit
- `ref`: `<image>@<digest>` of `<image>:<hash>`, else of `<image>:main`. Never builds
- `ensure`: build, smoke and push `<image>:<hash>` when the registry lacks it
- `promote`: point `<image>:main` at `<image>:<hash>`
- `local`: put `<image>:<hash>` in this machine's store: its copy, a pull, or a build with no push

## The merge queue

A repo's main lives in a bare repo on the server, and moves only to a tree that passed its check. Only the queue moves
it: a change enters the queue, the queue checks it on top of the changes ahead of it, and lands it when it passes.

`crate land [--no-wait]`, in a checkout whose remote `server` is the bare repo, is how a change lands: HEAD enters the
queue, and the push prints the check's output and the verdict. Landed, it exits 0, HEAD's branch moves to main, and the
repo's main checkout fast-forwards; not landed, it exits 1. `--no-wait` returns once HEAD is queued. A change already
in the queue is waited for.

```
the merge queue: main moves only to a tree that passed, and only the queue moves it
├── the door (pre-receive): what a push may do
│   ├── refs/queue/<sha>: the change enters the queue; <sha> is its commit, which shares history with main
│   ├── :refs/queue/<sha>: it leaves the queue
│   └── main is never pushed, nor refs/landed; a version tag is refused; other branches are free, and kept
├── the queue: its changes in order, each held by its ref until its verdict
├── the worker: one per repo, while changes wait
│   ├── runs side by side, as many as the machine's [count] check, each in its own slot: a worktree
│   │   (runs/<n>) and a system (crate-<repo>-check-<n>) whose tools volumes stay warm
│   ├── a run checks up to `batch` waiting changes (4 by default), on the tip of the run ahead: so each run checks
│   │   what main will be. A change on that tip moves it there; any other has its commits rebased onto it, its author
│   │   kept and crate the committer. A change that does not apply leaves, with the files it conflicts on
│   ├── the head run passes: main moves to its tip by compare-and-swap, its changes land, then the copy to GitHub,
│   │   and postsubmit or the release
│   └── a run fails: the runs behind it start again without it. Its failure counts once every run ahead has passed;
│       then a change checked alone leaves, and a batch is checked again in halves until the culprit is alone
└── the verdict: the push waits, printing its change's output until its last line,
    `crate: landed <sha> as <main>` or `crate: not landed: <why>`; `git push -o crate.wait=no` returns once queued.
    A change that lands is kept as refs/landed/<sha>, at the main commit it landed as: the verdict a client reads
```

- the check: `scripts/check.sh` in a workspace of kind check (its services, its tools member on the repo's `image`, its
  built app), with `CHECK_BASE` the run's base, within `timeout` seconds (900 by default)
- `crate queue <bare>`: the queue, each change's place and what it is doing. `crate queue <bare> jump <sha>`: the
  change goes first, checked alone, and every run starts again behind it
- a worker that stopped starts again with the next push, the next wait, or `crate apply`
- postsubmit: the slow sets, in a workspace of kind postsubmit, after main moves, one run at a time. Red puts a revert
  to the last green main first in the queue
- release: each move of main tags a version of each of `packages` whose commits ask for one (Conventional Commits as
  semver; before 1.0.0 each bump moves down a place). The tag is the truth, and `ship` names how it publishes. A
  package's first version is tagged by hand in the bare repo: `git -C <bare> tag v<version> <commit>`
- `crate init <bare> [<github url>]`: put the hooks on a bare repo. `crate apply <file|->`: make a server match its
  declaration (`GIT_HOME`, `GITHUB_BASE`, `REPOS`). A new repo's main is seeded from GitHub's. crate's own version is
  its install's

`hooks/` holds two one-line scripts that run `crate hook <name>` from the install beside them. The queue's state lives
beside each bare repo, in `<bare>/crate`: `queue.json`, each change's log in `queue/`, the runs' worktrees, the
worker's log, postsubmit's, and the [record](#the-record).

## The record

crate's record of what it did and how long each part took: one JSON line per step of a run, appended as the step
ends, and a total line when the run ends. A run is one thing crate does: a check, a change's land through the queue,
a postsubmit, an `up`, a copy to GitHub (mirror) or a release.

- the files
  - the queue: `<bare>/crate/log.jsonl`, each check, land, postsubmit, mirror and release of that repo
  - a person: `$XDG_STATE_HOME/crate/log.jsonl` (`~/.local/state` when unset), each `up` of that user
- the line: `at`, `run` (its id), `kind`, `repo`, `system`, `commit`, `step`, `seconds`, `result` (`pass` or `fail`),
  `note` (why it failed, or what: the image's `stored`, `pulled` or `built`, a check's changes, a land's main, a
  version, the revert)
- the steps, in order; a step that does not run writes no line
  - `wait`: a change's time in the queue before a run took it (a land)
  - `image`: the dev image on this machine
  - `hold`: the wait for a slot on the machine
  - `services`, `tools`, `setup` (a new tools member only)
  - `build`, `needs`: the built app and the proof it refuses without its needs (a check and postsubmit)
  - `app`: started until healthy, and its routes
  - `check`: `scripts/check.sh`
  - `revert`: postsubmit's revert of a red main
  - `total`: the run, its result the run's; a land's runs from entering the queue to its verdict
- the summary: one line at the end of a run, in its output (the push, postsubmit's log, `up`'s stderr):
  `crate: image 2s (stored), hold 0s, tools 1s, setup 61s, build 140s, needs 3s, app 5s, check 30s; total 242s, pass`
- a run with no total line was stopped. A line crate cannot write is dropped: the record never fails a run

```sh
# the median of each step of a repo's checks
duckdb -c "select step, median(seconds) from '<bare>/crate/log.jsonl' where kind = 'check' group by step"
# the last ten lands: commit, result, seconds from entering the queue to the verdict
jq -r 'select(.kind == "land" and .step == "total") | [.commit[:12], .result, .seconds] | @tsv' <bare>/crate/log.jsonl | tail
```

## Changing crate

crate stays outside the queue it ships, so a broken hook never blocks its own fix. A change lands on GitHub's main after
`just check` passes: `go vet`, `go test ./...`, shellcheck, then the queue's tests in `test/`, run on real git and
podman against the built install (`just build`, in `dist/crate`).

- `just release <major.minor.patch>`: check, tag, move the major tag (`v3`), and put `crate-linux-amd64.tar.gz`
  (`bin/crate`, `hooks/`, `VERSION`, `COMMIT`) on the release. A machine runs crate at the major tag
- `just rollback <major.minor.patch>`: move the major tag back
- every call has a limit, in `internal/limit`. `CRATE_LIMIT_SCALE` divides them all, never below 1 s, for tests
