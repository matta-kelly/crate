#!/usr/bin/env bash
# The tests of the intake, on real git: crate apply fetches the branches INTAKE names from a stand-in GitHub and enters
# each new head in the queue, once; a branch left out, or a head on main already, does not enter. A head that enters
# gets crate's commit status; with no token, the record says it was not posted, and the head enters all the same.
set -uo pipefail
root=${CRATE_ROOT:-$(dirname "$(dirname "$(readlink -f "$0")")")}  # the install to test: the built layout
tmp=$(mktemp -d)
export XDG_STATE_HOME="$tmp/.state" # crate's machine state is the suite's own
export XDG_CONFIG_HOME="$tmp/.config" # and its config: no GitHub token, so no status is posted
unset CRATE_GITHUB_API
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
trap 'pkill -f "crate (queue|postsubmit|ship) $tmp" 2>/dev/null; rm -rf "$tmp"' EXIT
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
  echo "#   ${2:-}"
}

# GitHub's copy: main, a bot's branch on it, a major the intake leaves out, and a branch whose head is main's.
git init -q -b main "$tmp/client"
git -C "$tmp/client" commit -q --allow-empty -m one
git init -q --bare -b main "$tmp/github/a.git"
git -C "$tmp/client" push -q "$tmp/github/a.git" main
git -C "$tmp/client" checkout -q -b bot
git -C "$tmp/client" commit -q --allow-empty -m "chore(deps): a bump"
git -C "$tmp/client" push -q "$tmp/github/a.git" bot:refs/heads/renovate/x
bump=$(git -C "$tmp/client" rev-parse HEAD)
git -C "$tmp/client" commit -q --allow-empty -m "chore(deps): a major"
git -C "$tmp/client" push -q "$tmp/github/a.git" bot:refs/heads/renovate/major-y
major=$(git -C "$tmp/client" rev-parse HEAD)
git -C "$tmp/client" push -q "$tmp/github/a.git" main:refs/heads/renovate/z
conf="GIT_HOME=$tmp/git
GITHUB_BASE=$tmp/github
REPOS=a
INTAKE=\"refs/heads/renovate/* ^refs/heads/renovate/major-*\""
bare=$tmp/git/a.git
intakes() { grep -c '"kind":"intake"' "$bare/crate/log.jsonl" 2>/dev/null || echo 0; }

"$root/bin/crate" apply - <<<"$conf" >/dev/null 2>&1  # seeds main
out=$("$root/bin/crate" apply - <<<"$conf" 2>&1)
if [ -e "$bare/crate/queue/$bump.log" ] && grep -q "from GitHub's renovate/x entered the queue" <<<"$out" &&
  jq -e --arg c "$bump" 'select(.kind == "intake" and .commit == $c and .result == "pass" and .note == "renovate/x")' \
    "$bare/crate/log.jsonl" >/dev/null; then
  ok "a head under INTAKE enters the queue, and the record says intake"
else not_ok "a head under INTAKE enters the queue, and the record says intake" "$out"; fi

if jq -e --arg c "$bump" 'select(.kind == "status" and .commit == $c and .result == "error" and (.note | contains("no GitHub token")))' \
  "$bare/crate/log.jsonl" >/dev/null; then
  ok "with no GitHub token, the head enters, and the record says its status was not posted"
else not_ok "with no GitHub token, the head enters, and the record says its status was not posted" "$(grep status "$bare/crate/log.jsonl")"; fi

if [ ! -e "$bare/crate/queue/$major.log" ] && ! git -C "$bare" rev-parse -q --verify refs/intake/renovate/major-y >/dev/null; then
  ok "a branch left out with ^ is not fetched, and does not enter"
else not_ok "a branch left out with ^ is not fetched, and does not enter"; fi

if [ "$(intakes)" = 1 ]; then ok "a head on main already does not enter"
else not_ok "a head on main already does not enter" "$(grep intake "$bare/crate/log.jsonl")"; fi

out=$("$root/bin/crate" apply - <<<"$conf" 2>&1)
if [ "$(intakes)" = 1 ] && ! grep -q "entered the queue" <<<"$out"; then ok "run again, a head that entered once does not enter again"
else not_ok "run again, a head that entered once does not enter again" "$out"; fi

git -C "$tmp/github/a.git" update-ref -d refs/heads/renovate/x
"$root/bin/crate" apply - <<<"$conf" >/dev/null 2>&1
if ! git -C "$bare" rev-parse -q --verify refs/intake/renovate/x >/dev/null; then ok "a branch gone from GitHub leaves refs/intake"
else not_ok "a branch gone from GitHub leaves refs/intake"; fi

# A worker that a timer's crate apply starts outlives the oneshot unit it ran in: apply runs in a transient oneshot
# service, as crate-apply.service does, and the head it enters still gets its verdict after the unit has ended.
verdict() { # the last line of a change's log once it is a verdict, waiting for it up to 60 s

  for _ in $(seq 120); do
    if tail -n1 "$bare/crate/queue/$1.log" 2>/dev/null | grep -q "^crate: \(landed\|not landed\)"; then
      tail -n1 "$bare/crate/queue/$1.log"
      return 0
    fi
    sleep 0.5
  done
  return 1
}
verdict "$bump" >/dev/null # the worker before it has ended, so this apply starts its own
git -C "$tmp/client" commit -q --allow-empty -m "chore(deps): a bump from a timer"
git -C "$tmp/client" push -q "$tmp/github/a.git" bot:refs/heads/renovate/w
timer=$(git -C "$tmp/client" rev-parse HEAD)
printf '%s\n' "$conf" >"$tmp/crate.env"
env_args=()
for name in PATH HOME XDG_STATE_HOME XDG_CONFIG_HOME CRATE_SLICE GIT_AUTHOR_NAME GIT_AUTHOR_EMAIL GIT_COMMITTER_NAME GIT_COMMITTER_EMAIL; do
  [ -n "${!name:-}" ] && env_args+=("--setenv=$name=${!name}")
done
out=$(systemd-run --user --wait --quiet --collect -p Type=oneshot "${env_args[@]}" "$root/bin/crate" apply "$tmp/crate.env" 2>&1)
if verdict "$timer" >/dev/null; then ok "a worker that apply starts in a oneshot unit outlives the unit: the head gets its verdict"
else not_ok "a worker that apply starts in a oneshot unit outlives the unit: the head gets its verdict" \
  "$out; log: $(cat "$bare/crate/queue/$timer.log" 2>&1); worker: $(tail -5 "$bare/crate/worker.log" 2>&1)"; fi

if out=$("$root/bin/crate" apply - <<<"GIT_HOME=$tmp/git
GITHUB_BASE=$tmp/github
REPOS=a
INTAKE=renovate/*" 2>&1); then not_ok "an INTAKE that is not refs/heads/ is refused" "$out"
elif grep -q "a pattern is refs/heads/<name>" <<<"$out"; then ok "an INTAKE that is not refs/heads/ is refused"
else not_ok "an INTAKE that is not refs/heads/ is refused" "$out"; fi

echo "1..$n"
exit "$failed"
