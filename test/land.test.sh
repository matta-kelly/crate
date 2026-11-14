#!/usr/bin/env bash
# The tests of crate land, on real git and the real engine: a bare repo with crate on it as the client's remote
# server, and a tiny repo whose check runs in alpine and passes or fails as its file `verdict` says.
set -uo pipefail
repo=$(dirname "$(dirname "$(readlink -f "$0")")")
root=${CRATE_ROOT:-$repo} # the install to test: the built layout
engine=${CRATE_ENGINE:-podman}
alpine=docker.io/library/alpine@sha256:0f49f331d9212aff11b932863be4eb01cdf962969aa86dab7d1ee21ac6f5f52b
tmp=$(mktemp -d)
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
# shellcheck source=test/lib.sh
. "$repo/test/lib.sh"
trap sweep EXIT

# A fresh bare repo with crate on it, seeded with the client's first commit, and the client, its remote server.
setup() {
  pkill -f "crate (queue|postsubmit) $tmp" 2>/dev/null
  cd "$tmp" && rm -rf "${tmp:?}"/*
  "$root/bin/crate" init "$tmp/repo.git" >/dev/null
  git init -q -b main "$tmp/client"
  cd "$tmp/client" || exit 1
  mkdir scripts
  printf 'crate = "1"\nimage = "%s"\n' "$alpine" >crate.toml
  cat >scripts/check.sh <<'SH'
#!/bin/sh
[ ! -f slow ] || sleep 3
[ "$(cat verdict)" = pass ] || { echo "check failed at: verdict"; exit 1; }
echo "check passed"
SH
  chmod +x scripts/check.sh
  echo pass >verdict
  git add -A && git commit -qm one
  seed
  git remote add server "$tmp/repo.git"
}

# crate_land [args...]: crate land in the client; sets out (its stdout and stderr) and code.
crate_land() {
  out=$("$root/bin/crate" land "$@" 2>&1)
  code=$?
}

# a green change lands: main takes it, the branch is main, and it exits 0
setup
git checkout -q -b work
echo a >a && git add a && git commit -qm a
sha=$(git rev-parse HEAD)
crate_land
if [ "$code" -eq 0 ] && [ "$(main_of "$tmp/repo.git")" = "$sha" ] && [ "$(git rev-parse HEAD)" = "$sha" ] &&
  grep -q "check passed" <<<"$out" && grep -q "crate land: ${sha:0:7} landed" <<<"$out"; then
  ok "a green change lands, with the check's output, and exits 0"
else not_ok "a green change lands, with the check's output, and exits 0" "code=$code
$out"; fi

# a red change is not landed: it exits 1 with crate's verdict, and main has not moved
before=$(main_of "$tmp/repo.git")
echo fail >verdict && git commit -qam red
crate_land
if [ "$code" -eq 1 ] && [ "$(main_of "$tmp/repo.git")" = "$before" ] && grep -q "check failed at: verdict" <<<"$out" &&
  grep -q "crate: not landed" <<<"$out" && grep -q "did not land" <<<"$out"; then
  ok "a red change is not landed, exits 1 with crate's verdict, and main has not moved"
else not_ok "a red change is not landed, exits 1 with crate's verdict, and main has not moved" "code=$code
$out"; fi
git reset -q --hard "$before"

# main moved under the change: the queue lands it on top, and the branch moves to the new main
git clone -q -o server "$tmp/repo.git" "$tmp/other"
(cd "$tmp/other" && echo o >o && git add o && git commit -qm other && "$root/bin/crate" land >/dev/null 2>&1)
moved=$(main_of "$tmp/repo.git")
echo b >b && git add b && git commit -qm b
crate_land
if [ "$code" -eq 0 ] && [ "$moved" != "$before" ] && [ "$(git -C "$tmp/repo.git" rev-parse main^)" = "$moved" ] &&
  [ "$(git rev-parse HEAD)" = "$(main_of "$tmp/repo.git")" ]; then
  ok "a change on a main that moved lands on top, and the branch moves to the new main"
else not_ok "a change on a main that moved lands on top, and the branch moves to the new main" "code=$code
$out"; fi

# a change next to lines main changed lands rebased, as a commit whose patch differs, and crate land still knows it
# landed: the verdict is the server's refs/landed/<sha>, not a comparison of patches
printf '1\n2\n3\n4\n5\n' >near && git add near && git commit -qm near && crate_land
(cd "$tmp/other" && git pull -q --rebase server main && sed -i 's/^2$/two/' near && git commit -qam "two" &&
  "$root/bin/crate" land >/dev/null 2>&1)
sed -i 's/^4$/four/' near && git commit -qam four
sha=$(git rev-parse HEAD)
crate_land
landed=$(git -C "$tmp/repo.git" rev-parse "refs/landed/$sha" 2>/dev/null)
if [ "$code" -eq 0 ] && [ "$landed" = "$(main_of "$tmp/repo.git")" ] && [ "$landed" != "$sha" ] &&
  [ "$(git -C "$tmp/repo.git" show main:near)" = "$(printf '1\ntwo\n3\nfour\n5')" ]; then
  ok "a change next to lines main changed lands rebased, and crate land knows it landed"
else not_ok "a change next to lines main changed lands rebased, and crate land knows it landed" "code=$code
$out"; fi

# --no-wait returns once the change is queued, and it lands after
echo c >c && git add c && git commit -qm c
sha=$(git rev-parse HEAD)
start=$(date +%s)
crate_land --no-wait
took=$(($(date +%s) - start))
settle
if [ "$code" -eq 0 ] && grep -q "is queued" <<<"$out" && [ "$took" -lt 5 ] && [ "$(main_of "$tmp/repo.git")" = "$sha" ]; then
  ok "--no-wait returns once queued, and the change lands after"
else not_ok "--no-wait returns once queued, and the change lands after" "code=$code took=${took}s
$out"; fi

# a change in the queue already: crate land waits for its verdict, and moves the branch when it lands
touch slow && git add slow && git commit -qm slow
sha=$(git rev-parse HEAD)
queue >/dev/null
crate_land
if [ "$code" -eq 0 ] && grep -q "in the queue already" <<<"$out" && grep -q "crate land: ${sha:0:7} landed" <<<"$out" &&
  [ "$(main_of "$tmp/repo.git")" = "$sha" ]; then
  ok "a change in the queue already: crate land waits for its verdict"
else not_ok "a change in the queue already: crate land waits for its verdict" "code=$code
$out"; fi

# the main checkout fast-forwards to the land, when it has main checked out
git checkout -q main
git worktree add -q -b side "$tmp/side" main
(cd "$tmp/side" && echo d >d && git add d && git commit -qm d)
out=$(cd "$tmp/side" && "$root/bin/crate" land 2>&1)
if [ "$(git -C "$tmp/client" rev-parse HEAD)" = "$(main_of "$tmp/repo.git")" ] && grep -q "$tmp/client is at" <<<"$out"; then
  ok "the repo's main checkout fast-forwards to the land"
else not_ok "the repo's main checkout fast-forwards to the land" "$out"; fi

# a checkout with no server remote is refused, and nothing is pushed; so is one with nothing to land
git remote remove server
crate_land
missing=$code
git remote add server "$tmp/repo.git"
crate_land
if [ "$missing" -eq 1 ] && [ "$code" -eq 1 ] && grep -q "nothing that server/main lacks" <<<"$out"; then
  ok "no server remote, or nothing to land, is refused"
else not_ok "no server remote, or nothing to land, is refused" "missing=$missing code=$code
$out"; fi

echo "1..$n"
exit "$failed"
