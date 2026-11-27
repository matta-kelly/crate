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

- `up [--attempt] [-e NAME]...`: make it, or start what stopped. A second `up` changes nothing. An attempt runs in a
  worktree of its own, with no route. Each `-e NAME` passes this shell's `NAME` to the dev server by name, as `run -e`
  does, and counts as given for `[app] needs`; the dev server is made again to take it. Each `up` is a run on the
  person's [record](#the-record)
- `run [-e NAME]... [--] <argv...>`: run a command in the tools member, in the caller's folder. Its exit is the
  command's, its stdin is the caller's (a pipe as well as a terminal), and its stdout and stderr stay apart, so a
  program can drive the tools member. Each `-e NAME` passes this shell's `NAME` by name, so a secret's value is never
  in argv
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
- the definition, `[app] deploy`: the folder of how the app runs in every place, played by `podman kube play` as one
  Pod, `<name>-<folder>-app`, reached as `app` ([The definition](#the-definition))

## crate.toml

```toml
name = "publicpersona"                 # the repo's name: its workspace's and its dev image's
services = ".crate/pod.yaml"    # the services file, or no services

[tools]
setup = "pnpm install --frozen-lockfile"   # run once when the tools member is made; a check's, once per key
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
inputs = [".crate/Containerfile", "pnpm-lock.yaml", "package.json"]
```

- services: Pod documents, each named `WORKSPACE-<service>`. `WORKSPACE` becomes the workspace's name, and each pod
  joins the network as `<service>`. No `resources` limits. A volume is `<name>-<key>`
- the tools member's volumes: `<name>-<folder>`, the folder's leading dots taken away and each other character not
  `a-z` or `0-9` made `-`
- needs: the shape of the app's binding. The image and the declaration are the same everywhere; each place gives
  the needs its own way: a workspace and an attempt by `[tools] env` or `up -e`, a check by `[app] env`, production by its
  own. A place that lacks one fails `up` and names it. A check first runs the built image without them, and passes
  only if it stops non-zero and its output names each one
- the tools member reaches the app as `app`, and by `CRATE_APP_URL` (`http://<name>-<folder>-app:<first port>`). A
  browser test opens `CRATE_APP_URL`: Chrome sends the top-level domain `app` to https
- another workspace's app: a person's workspace with routes has `CRATE_ROUTER` in both members, the router's URL on
  its network (`http://workspace-router`). A request there with the other workspace's host (the first line of its
  `crate url`, with no port) reaches that app. Each workspace's own network keeps the rest apart; a check and an
  attempt have no router
- a built app needs at least one port. The dev image needs the tool `health` calls (`wget`) when the dev server runs it
- the merge queue's keys: `crate`, `image`, `timeout`, `batch`, `ship`, `packages`, `postsubmit`, `postsubmit_timeout`
  ([The merge queue](#the-merge-queue)); and, for `ship = "image"`, `[app] publish`, the repository the image ships to
  (`ghcr.io/<owner>/<name>`, no tag), and `[app] quadlets` or `deploy`, the folder of its run files ([The ship](#the-ship))
- `[[images]]`: the images the repo publishes for a node or another repo to pin ([Declared images](#declared-images))

## The definition

`[app] deploy = "deploy"`: the app as a workspace, a check and production each run it, in Kubernetes YAML, the format
podman plays by hand (`podman kube play`) and under systemd (a `.kube` Quadlet). In place of `[app]` env, health,
ports, needs and quadlets, which it holds.

```
deploy/
├── app.yaml     the app: one Pod, its containers and init containers, their images (<publish>:VERSION), ports,
│                livenessProbe and securityContext, and each value (configMapKeyRef) and secret (secretKeyRef) by name
├── dev.yaml     a workspace's and a check's values, a ConfigMap, and secrets, a Secret, each named as app.yaml names them
└── prod.yaml    production's values, a ConfigMap. Its secrets are the node's own podman secret of the same name
```

- the play: app.yaml with dev.yaml, as one file of crate's alone, on the system's network as `app` and
  `<name>-<folder>-app`. Each name podman keeps is the system's: the Pod `<name>-<folder>-app`, each Secret
  `<name>-<folder>-<its name>`, so two workspaces of one repo never meet
- the binding: each key app.yaml names, a deploy must give. podman refuses a play that lacks one, and names it
- a check and postsubmit: `[app] build`, the image that ships, in place of each image that ends in `:VERSION`, run as
  the definition says production runs it
- a person's workspace and an attempt: every container in the dev image, the checkout its working folder, its volumes
  mounted, as user 1000 (keep-id), with no securityContext. The app's container, the first, runs `[app] command`; an
  init container runs its own command from the checkout, so a relative command (`db/migrate.sh`) runs the change's
  file. The app's container takes `up -e` (its value is written to the play's file, never to argv) and `CRATE_ROUTER`
- the health: the app's container's livenessProbe, which crate runs as in [A workspace](#a-workspace)
- the ports: the app's container's named ports: the routes, `CRATE_APP_URL`, and the one a built app must have
- the ship: app.yaml as `<name>.yaml` and prod.yaml as `<name>.prod.yaml`, each `image:` that ends in `:VERSION`
  stamped, and `<name>.kube`, the Quadlet that plays them (`Yaml=`, `ConfigMap=`), so each app's files sit beside every
  other app's in a node's Quadlet folder ([The ship](#the-ship)). The node's drop-in, `<name>.kube.d/<name>.conf`, adds
  its own lines: its network, its isolation, its published port
- a definition that is not one Pod with a container, or values that are not ConfigMaps and Secrets, fail `up` and
  say why

## The machine

crate bounds its own work on every machine it runs on, with no tuning: one line under all of it, an order between its
kinds, and a count of each. Root's settings (dazoo on the server, deeotee on the laptop) are a backstop for the work
crate does not run.

- the line: crate's own slice, `CRATE_SLICE` (`crate`) under the user's manager, gets a `MemoryHigh` (a quarter of the
  machine's memory, or the machine file's `[slice] memory`), and a `CPUWeight` and an `IOWeight` of 50, below a sibling's
  100. Past the line, the kernel reclaims and slows crate's own work, and kills nothing, so crate's memory and its page
  cache never press on the desktop or a node's other services. The line also holds crate's unwritten data to a share
  of it (about 0.6, measured). The properties are `--runtime`, set again by each process's first placement
- the controllers: the line needs cpu, io and memory delegated to the user's manager. A slice that lacks one fails the
  placement, and the error names root's fix, so crate never runs unbounded without saying so
- the order: each kind gets a slice under crate's own with a `CPUWeight`: a person's work, then a check, then
  postsubmit and attempts
- the scope: every podman call that writes the store, or starts what does (a pull, a build, a container's run or
  create, a kube play), runs as `systemd-run --user --scope` in the system's slice. So podman's own work, a layer's
  unpack and commit, is the slice's, and not the caller's. The containers it starts get the slice as their
  `--cgroup-parent`
- the count: `[count]` says how many checks, postsubmits and attempts run at once. Past it, one waits its turn, at most
  an hour. A slot is a file under `$XDG_STATE_HOME/crate/slots/<kind>/`
- the machine file: `/etc/crate/machine.toml` (`CRATE_MACHINE`) holds crate's keys alone, `[count]`,
  `[slice] memory` (a share, `"25%"`, or a size, `"8G"`) and `[router] address` (the IP the workspace router
  listens on, such as the node's mesh address, so the node's proxy reaches it), and a key crate does not know is an
  error. No file is the defaults: a count of 1 for each kind, a quarter of the memory, and the router on 127.0.0.1
- crate's own lasting processes: a queue worker, a postsubmit, a publish or the dev image cache, detached from the push
  or the unit that starts it, runs as a service of the user's manager in crate's slice, so the end of a oneshot unit (a
  timer's `crate apply`) does not stop it. The start waits until it runs: a manager that does not answer, or a service
  that does not start, fails it, and the error says so
- a remote engine (`CONTAINER_HOST`): its work runs on the engine's machine, so crate places nothing, holds nothing,
  and says so once

## The dev image

`crate dev-image <hash|ref|local> [--rev <commit>]`: one image per hash of the repo's `[dev] inputs`, in ghcr as
`ghcr.io/matta-kelly/<name>-dev` (or `[dev] image`). A repo declares `name` and `[dev] inputs`; crate guesses neither,
and refuses a `crate.toml` that lacks one, in the queue too.

- `hash`: the hash of the inputs at the commit
- `ref`: `<image>@<digest>` of `<image>:<hash>`, else of `<image>:main`. Never builds
- `local`: put `<image>:<hash>` in this machine's store: its copy, a pull, or a build with no push
- the smoke: a build is kept only when `[tools] setup` passes in it with no network, on a copy of the commit's tree, so
  the image holds what the lockfiles name and a workspace's setup downloads nothing. A repo with no setup has no smoke

```
the cache: the registry holds each dev image by the hash of its inputs, so its copy is the same image on every machine
├── the writer: the merge queue on the server, alone
│   ├── a check or postsubmit that makes a dev image starts crate cache <bare>, which pushes it by its hash at once, in
│   │   a process of its own, so an upload never holds a check. An image whose change never lands is pushed too: it
│   │   is addressed by hash, so it misleads no one
│   └── a green main pushes its own image again as <image>:main, the image `ref` falls back to
├── the readers: every other machine. `local` pulls by the hash, and builds only an image no check has made yet: the
│   change that edits an input, in its author's workspace
└── a push that fails (no login, a registry that is down) is recorded, and tried again by the next check, the next
    green main, the worker's next start and crate apply. The writes need podman alone, logged in by the ship's login
```

The cache's state lives in `<bare>/crate/cache`: `pushed` (each hash pushed), `main` (the hash `:main` is), `run.log`.

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
│   │   (runs/<n>) and a system (crate-<repo>-check-<n>) whose tools volumes hold its setup
│   ├── a run checks up to `batch` waiting changes (4 by default), on the tip of the run ahead: so each run checks
│   │   what main will be. A change on that tip moves it there; any other has its commits rebased onto it, its author
│   │   kept and crate the committer. A change that does not apply leaves, with the files it conflicts on
│   ├── the head run passes: main moves to its tip by compare-and-swap, and its changes land. Then postsubmit, when
│   │   the repo has one; once main is green, the copy to GitHub and the release
│   ├── a run fails: the runs behind it start again without it. Its failure counts once every run ahead has passed;
│   │   then a change checked alone leaves, and a batch is checked again in halves until the culprit is alone
│   └── a run errs: crate could not check it, so it decides nothing. Its changes stay at the front, unsplit, and
│       are checked again (see whose fault, below)
└── the verdict: the push waits, printing its change's output until its last line,
    `crate: landed <sha> as <main>` or `crate: not landed: <why>`; `git push -o crate.wait=no` returns once queued.
    A change that lands is kept as refs/landed/<sha>, at the main commit it landed as: the verdict a client reads
```

- the check: `scripts/check.sh` in a workspace of kind check (its services, its tools member on the repo's `image`, its
  built app), with `CHECK_BASE` the run's base, within `timeout` seconds (900 by default)
- a check's setup runs once per key: the image (a dev image's tag is the hash of `[dev] inputs`), `[tools] setup`,
  `volumes` and `env`. The slot's kept volumes are its bases. Setup fills them once, from a read-only checkout, and
  each check mounts them as overlays (podman's `:O`): it reads what setup made, and what it writes is gone with it. So
  every check starts as if setup had just run. A setup that writes outside its volumes (a `prepare` that builds into
  the checkout) cannot fill a base: its key runs setup in each check's tools member, as a workspace does. The same
  for postsubmit
- `crate queue <bare>`: the queue, each change's place and what it is doing. `crate queue <bare> jump <sha>`: the
  change goes first, checked alone, and every run starts again behind it
- a worker that stopped starts again with the next push, the next wait, or `crate apply`
- whose fault: a run ends in pass, fail or error (`internal/fault`). A fail is the change's own: its crate.toml or
  services file, its dev image's build or smoke, its setup, its app's build, needs proof or health, its check.sh's
  exit, its check past `timeout`, a conflict. Anything else is an error: crate's machine failed (podman, a pull, the
  network, a slot, a tree, the cache). Only the change's own failures are marked, so a failure nobody marked is never
  blamed on a change
  - an error rejects nothing and splits nothing: the run's changes stay at the front and are checked again, and each
    change's log says why it waits. A change with 3 errors of its own, on a machine that is not paused, leaves with
    `crate: not landed: error: crate could not check it 3 times (<why>); land it again`
  - the pause: the machine counts errors in a row, for all its repos (`$XDG_STATE_HOME/crate/health.json`). The 3rd
    pauses it: no run starts, the workers stop, `crate land` and `crate queue` say why. A pass sets the count back.
    Each `crate apply` probes a paused machine (`podman info`, `podman images`, a network made and removed); when the
    probe passes, the machine is well again and the workers start
  - postsubmit that errs neither reverts nor goes green: `crate apply` runs it again, up to 3 times
- postsubmit: the slow sets, in a workspace of kind postsubmit, after main moves, one run at a time. Red puts a revert
  to the last green main first in the queue
- green: a main that passed every set it has, the gate's and postsubmit's. A repo with no postsubmit is green as it
  lands. Only a green main is copied to GitHub and released, so GitHub, and whatever builds or deploys from it, never
  sees a main postsubmit failed: a red main stays on the server until its revert lands, and the copy after that carries
  both
- release: each green main tags a version of each of `packages` whose files changed since its last version and whose
  commits ask for one (Conventional Commits as semver; before 1.0.0 each bump moves down a place). The tag is the
  truth, and `ship` names how it publishes: `npm` and `source` from the tag, by the repo's own workflow on GitHub;
  `image` by crate itself, with no version tag ([The ship](#the-ship)). A package's first version is tagged by hand in
  the bare repo: `git -C <bare> tag v<version> <commit>`
- `crate init <bare> [<github url>]`: put the hooks on a bare repo: the install's, by the path crate was run by, so
  through a link to the install (`/usr/local/lib/crate/current`) the hooks follow it to each new version. `crate apply <file|->`: make a server match its
  declaration (`GIT_HOME`, `GITHUB_BASE`, `REPOS`, `INTAKE`). A new repo's main is seeded from GitHub's. crate's own
  version is its install's
- the intake: an update bot (Renovate) proposes each change as a branch on the repo's GitHub copy, and never merges.
  `INTAKE` names those branches as git refspec sources, a `^` one left out: `INTAKE="refs/heads/renovate/*
  ^refs/heads/renovate/major-*"`. Each `crate apply` fetches them into `refs/intake/` and enters each head the queue has
  never held, as a push to `refs/queue/<sha>` would, under the door's rules; the record says `intake`. A head enters
  once (its change's log is kept), so one that landed or was refused is not checked again, and a new push to the
  branch is a new head. A branch left out (a major) waits for a person to land it
- the status: crate's verdict on a change from the intake, as a commit status named `crate` on its head on GitHub,
  which the bot's pull request shows: `pending` as it enters, then `success` (landed), `failure` (refused) or
  `error` (crate could not check it). The token: `$XDG_CONFIG_HOME/crate/github-token` (`~/.config` when unset), a
  fine-grained one with commit statuses write on the repos crate serves. Each post is on the record, kind
  `status`; one that fails (no token, GitHub down) is recorded, and the change is decided all the same

`hooks/` holds two one-line scripts that run `crate hook <name>` from the install beside them. The queue's state lives
beside each bare repo, in `<bare>/crate`: `queue.json`, each change's log in `queue/`, the runs' worktrees, the
worker's log, postsubmit's, the ship's, and the [record](#the-record).

## The ship

`ship = "image"`: crate publishes each green main's app, the image its check built and ran, with its run files beside
it, so a node runs what crate tested. Nothing on GitHub builds it again.

- the keep: a check that passes keeps its app's image by the commit it checked, `localhost/crate-<repo>-ship:<commit>`,
  past its slot's removal, so it is there when main is green, after postsubmit for a repo that has one. A repo that
  does not ship its image keeps nothing
- the publish: `crate ship <bare>`, started by each green main beside the copy to GitHub, in its own process, so an
  upload never holds the queue. A land says it started; its output is in `<bare>/crate/ship/run.log`
  - the build number: the count of main's first-parent commits up to the green main. Main only moves forward, so a
    later main has a larger number, and an update bot (Renovate) orders the tags as versions. The
    commit is the run files' revision
  - the image: pushed as `<publish>:<build>`. No `latest`
  - the run files: each file of `quadlets` at the commit, or `deploy`'s definition as a `.kube` and the two files it
    plays ([The definition](#the-definition)), each `Image=` of a `.container` and each `image:` of a `.yaml` that ends
    in `:VERSION` stamped with `:<build>`. A `VERSION` left after is refused. They are packed as one tar.gz that is the
    same bytes for the same files (sorted names, owner 0, time 0), and pushed with oras-go as an OCI artifact,
    `<publish>-quadlets:<build>`: its layer `quadlets.tar.gz` (`application/vnd.oci.image.layer.v1.tar+gzip`),
    its artifact type `application/vnd.<name>.quadlets.v1`, its source the repo on GitHub, its revision the commit,
    its created time the commit's
  - the pin line, printed and on the record: `<name> <build> <publish>-quadlets sha256:<manifest digest>`. A node's
    releases file takes it (monkeydo fetches the manifest by that digest and saves the layer by its title)
- the login: `$XDG_CONFIG_HOME/crate/push-auth.json` (`~/.config` when unset), podman's `auth.json` format, with a
  token that may write the packages. Only the publish and the dev image cache read it; no check, postsubmit or
  workspace mounts it
- the rule is a node's: the green main is published. A publish that fails (no login, a registry that is down) is
  recorded, and main and the queue go on. It is tried again, with the same kept image, by the next green main, the
  worker's next start and `crate apply`; a newer green main takes the place of one not yet published.
  `<bare>/crate/ship/published` is the last commit published
- the kept images that go: after each publish, every kept image but the green main's while it is not published, and
  those of the mains after it, which wait for their turn. So what was published goes, and what never will be: a main
  a newer one superseded, a run that passed and never landed. A green main with no kept image (one that landed before
  its repo shipped its image) has nothing to publish; the next main that lands has

## Declared images

An image a repo publishes that is not its app: a node's bootc image, or an image a node runs, such as a custom Caddy.

```toml
[[images]]
publish = "ghcr.io/matta-kelly/caddy-cloudflare"   # its repository, no tag
containerfile = "images/caddy-cloudflare/Containerfile"
inputs = ["images/caddy-cloudflare"]               # the paths it is built from: they must hold the containerfile
point = "latest"                                   # a tag that follows its newest build; for a reader with no pin
```

- the build: a rootless `podman build` of the commit's tree as git holds it, never a checkout. Each file has git's
  mode (0644, or 0755 for an executable) whatever the machine's umask, and the build makes no `/etc/hostname` of its
  own (`--no-hostname`), so a `RUN` step that writes one keeps it. The same commit builds the same image anywhere.
  crate runs only `podman build`: no command of the repo's runs on the machine
- the check: a run whose changes touch an image's inputs builds it, after `scripts/check.sh` passes, and keeps it on
  this machine as `localhost/crate-<repo>-image-<name>:<hash of its inputs>`. A build that fails refuses the change
- the tag: the build number of the last main that changed the inputs, the count of main's first-parent commits up to
  it. The same inputs keep the same tag, so a change elsewhere in the repo, a pin bump among them, publishes nothing:
  no loop where a pin's change rebuilds what it pins. A later change to the inputs has a larger number, so an update bot
  orders the tags as versions
- the publish: a green main pushes each image whose `<publish>:<tag>` is not published yet, the kept build, else one
  from that main, then moves `point` to it, from a process of its own (`crate images <bare>`), started by the land, the
  worker's start and `crate apply`. It logs in by the push login ([The ship](#the-ship)). Its state is in
  `<bare>/crate/images`: `published`, each `<publish>:<tag>` pushed, one a line; `run.log`, the runs' output

## The record

crate's record of what it did and how long each part took: one JSON line per step of a run, appended as the step
ends, and a total line when the run ends. A run is one thing crate does: a check, a change's land through the queue,
a postsubmit, an `up`, a copy to GitHub (mirror), a release (a version, or an image's publish), a push of dev images
(cache), a declared image's publish (image) or a commit status (status).

- the files
  - the queue: `<bare>/crate/log.jsonl`, each check, land, postsubmit, mirror, release, cache, intake and status of that repo
  - a person: `$XDG_STATE_HOME/crate/log.jsonl` (`~/.local/state` when unset), each `up` of that user
- the journal: on the server, each line of the queue's record is also one journal entry, `SYSLOG_IDENTIFIER=crate`,
  the JSON line its message, so the node's log shipper carries it. A person's record stays in its file
- the queue line: `crate apply` prints `crate: queue repo=<repo> depth=<changes> oldest=<seconds> worker=<what>` for
  each repo whose queue holds a change, so a queue that stops moving shows as a wait that grows in the server's log.
  `worker` is what apply did for it: `started`, `running` (one runs), `paused` (the machine is paused) or `error` (it
  could not tell, with the error on its own line); never nothing
- the line: `at`, `run` (its id), `kind`, `repo`, `system`, `commit`, `step`, `seconds`, `result` (`pass`, `fail`, or `error` for a run crate could not do),
  `note` (why it failed, or what: the image's `stored`, `pulled` or `built`, a check's changes, a land's main, a
  version, the revert, a publish's pin line)
- the steps, in order; a step that does not run writes no line
  - `wait`: a change's time in the queue before a run took it (a land)
  - `image`: the dev image on this machine
  - `hold`: the wait for a slot on the machine
  - `services`, `setup` before `tools` in a check and postsubmit, its note `kept`, `primed`, `refused` (setup writes
    outside its volumes) or `run` (in the tools member); else `tools`, `setup` (a new tools member only)
  - `build`, `needs`: the built app and the proof it refuses without its needs (a check and postsubmit)
  - `app`: started until healthy, and its routes
  - `check`: `scripts/check.sh`
  - `revert`: postsubmit's revert of a red main
  - `push`, `run files`: a publish's image, and its run files beside it (a release of `ship = "image"`); a dev
    image the cache pushed by its hash
  - `point`: the cache's push of the green main's dev image as `:main`
  - `total`: the run, its result the run's; a land's runs from entering the queue to its verdict
- the summary: one line at the end of a run, in its output (the push, postsubmit's log, `up`'s stderr):
  `crate: hold 0s, image 2s (stored), tools 1s, setup 61s, build 140s, needs 3s, app 5s, check 30s; total 242s, pass`
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
  (`bin/crate`, `hooks/`, `VERSION`, `COMMIT`) on the release. The release reaches each machine:
  - the laptop and GitHub's workflows: at the major tag. chezmoi takes it within a day, or at once with
    `chezmoi apply --refresh-externals`
  - monkeybusiness: at the version and sha256 that dazoo's `nodes/mutable/monkeybusiness/releases` pins. Each release
    lands its line there the same day, and dazoo-sync moves `/usr/local/lib/crate/current` on its next tick
- `just rollback <major.minor.patch>`: move the major tag back
- every call has a limit, in `internal/limit`. `CRATE_LIMIT_SCALE` divides them all, never below 1 s, for tests
- every call runs by one runner, `internal/process`: in a process group of its own, so at its limit, or when its
  caller stops it, the whole group gets TERM, and KILL 10 s later (`limit.Grace`). A stopped call leaves no process
  and never holds its caller
