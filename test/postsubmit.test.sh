#!/usr/bin/env bash
# The tests of crate's postsubmit, on real git and the real engine: a repo whose crate.toml names a slow set, which
# passes or fails as its file `slow` says, landed on a bare repo with crate on it, beside a stand-in for GitHub.
set -uo pipefail
root=$(dirname "$(dirname "$(readlink -f "$0")")")
alpine=docker.io/library/alpine@sha256:0f49f331d9212aff11b932863be4eb01cdf962969aa86dab7d1ee21ac6f5f52b
tmp=$(mktemp -d)
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
trap 'pkill -f "crate postsubmit $tmp" 2>/dev/null; rm -rf "$tmp"; ${CRATE_ENGINE:-podman} volume rm -f crate-repo-check-node-modules crate-repo-postsubmit-node-modules >/dev/null 2>&1' EXIT
failed=0
n=0

ok() {
  n=$((n + 1))
  echo "ok $n - $1"
}
not_ok() {
  n=$((n + 1))
  failed=1
  echo "not ok $n - $1"
  [ -z "${2:-}" ] || while IFS= read -r line; do echo "#   $line"; done <<<"$2"
}

setup() {
  cd "$tmp" && rm -rf "${tmp:?}"/*
  git init -q --bare -b main "$tmp/github.git"
  "$root/bin/crate" init "$tmp/repo.git" "$tmp/github.git" >/dev/null
  git init -q -b main "$tmp/client"
  cd "$tmp/client" || exit 1
  mkdir scripts
  printf 'crate = "1"\nimage = "%s"\nship = "npm"\npostsubmit = "slow"\n' "$alpine" >crate.toml
  cat >scripts/check.sh <<'CHECK'
#!/bin/sh
# the fast check passes; the slow set passes or fails as the file `slow` says
[ "${1:-}" = slow ] || { echo "check passed"; exit 0; }
echo "slow set on $(cat slow)"
sleep 2
[ "$(cat slow)" = pass ] || { echo "slow failed at: slow"; exit 1; }
CHECK
  chmod +x scripts/check.sh
  echo pass >slow
  git add -A && git commit -qm "chore: start"
  git tag v0.1.0
  git push -q "$tmp/repo.git" main --tags 2>/dev/null
  settle
}

# settle: wait until no postsubmit runs for this repo and main has a postsubmit verdict, for at most a minute.
settle() {

  for _ in $(seq 1 120); do
    if ! pgrep -f "crate postsubmit $tmp/repo.git" >/dev/null &&
      [ "$(cat "$tmp/repo.git/crate/postsubmit/green" 2>/dev/null)" = "$(git -C "$tmp/repo.git" rev-parse main)" ] ||
      grep -q "not reverted" "$tmp/repo.git/crate/log" 2>/dev/null && ! pgrep -f "crate postsubmit $tmp/repo.git" >/dev/null; then
      return 0
    fi
    sleep 0.5
  done
}

main_of() { git -C "$1" rev-parse --verify -q refs/heads/main; }

# a green land: postsubmit passes, main stays, and only then is it released
setup
echo a >a && git add a && git commit -qm "feat: a"
out=$(git push "$tmp/repo.git" main 2>&1)
if grep -q "postsubmit runs .* in the background" <<<"$out" && ! git -C "$tmp/repo.git" rev-parse -q --verify v0.1.1 >/dev/null; then
  ok "a land does not wait for postsubmit, and is not released before it passes"
else not_ok "a land does not wait for postsubmit, and is not released before it passes" "$out"; fi
settle
if [ "$(main_of "$tmp/repo.git")" = "$(git rev-parse HEAD)" ] && grep -q "postsubmit repo .* pass$" "$tmp/repo.git/crate/log"; then
  ok "a green postsubmit leaves main as it is"
else not_ok "a green postsubmit leaves main as it is" "$(cat "$tmp/repo.git/crate/log")"; fi
if [ "$(git -C "$tmp/repo.git" rev-parse -q --verify 'v0.1.1^{commit}')" = "$(git rev-parse HEAD)" ] &&
  git -C "$tmp/github.git" rev-parse -q --verify v0.1.1 >/dev/null; then
  ok "a green postsubmit releases the main it tested"
else not_ok "a green postsubmit releases the main it tested" "$(git -C "$tmp/repo.git" tag)"; fi

# a red land: reverted on main, its failure kept, and no version made
good=$(git rev-parse HEAD)
echo fail >slow && git commit -qam "fix: breaks the slow set"
red=$(git rev-parse HEAD)
git push -q "$tmp/repo.git" main 2>/dev/null
settle
now=$(main_of "$tmp/repo.git")
if [ "$now" != "$red" ] && [ "$(git -C "$tmp/repo.git" rev-parse "$now^")" = "$red" ] &&
  [ "$(git -C "$tmp/repo.git" rev-parse "$now^{tree}")" = "$(git rev-parse "$good^{tree}")" ]; then
  ok "a red postsubmit reverts the land on main, as a new commit"
else not_ok "a red postsubmit reverts the land on main, as a new commit" "$(cat "$tmp/repo.git/crate/log")"; fi
msg=$(git -C "$tmp/repo.git" log -1 --format=%B "$now")
if grep -q "^revert: 1 commits, red in postsubmit" <<<"$msg" && grep -q "slow failed at: slow" <<<"$msg" &&
  grep -q "slow failed at: slow" "$tmp/repo.git/crate/postsubmit/${red:0:12}.log"; then
  ok "the failure is kept, in the revert's message and in its log"
else not_ok "the failure is kept, in the revert's message and in its log" "$msg"; fi
if [ "$(git -C "$tmp/repo.git" tag --sort=v:refname | tr '\n' ' ')" = "v0.1.0 v0.1.1 " ]; then
  ok "a red land makes no version"
else not_ok "a red land makes no version" "$(git -C "$tmp/repo.git" tag)"; fi
if [ "$(main_of "$tmp/github.git")" = "$now" ]; then ok "the revert is copied to github"
else not_ok "the revert is copied to github"; fi
if [ "$(cat "$tmp/repo.git/crate/postsubmit/green")" = "$now" ] && grep -q "pass: the tree of green" "$tmp/repo.git/crate/log"; then
  ok "the revert itself passes postsubmit with no run: its tree is green's"
else not_ok "the revert itself passes postsubmit with no run: its tree is green's" "$(cat "$tmp/repo.git/crate/log")"; fi

# the next land after a revert is tested and released as usual
git fetch -q "$tmp/repo.git" main && git reset -q --hard "$now"
echo b >b && git add b && git commit -qm "feat: b"
git push -q "$tmp/repo.git" main 2>/dev/null
settle
if [ "$(main_of "$tmp/repo.git")" = "$(git rev-parse HEAD)" ] && git -C "$tmp/repo.git" rev-parse -q --verify v0.1.2 >/dev/null; then
  ok "a green land after a revert stays and is released"
else not_ok "a green land after a revert stays and is released" "$(cat "$tmp/repo.git/crate/log")"; fi

echo "1..$n"
exit "$failed"
