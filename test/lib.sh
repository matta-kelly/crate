# shellcheck shell=bash disable=SC2034 # failed is the suite's: it exits with it
# What crate's shell suites share: the TAP lines, a repo's first main, and a land through its merge queue.
# A suite sets tmp (its folder) and engine before it sources this, and exits with $failed.
: "${tmp:?a suite sets tmp}" "${engine:?a suite sets engine}"
export XDG_STATE_HOME="$tmp/.state" # crate's machine state is the suite's own; setup's rm of $tmp/* keeps it

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

main_of() { git -C "$1" rev-parse --verify -q refs/heads/main || echo none; }

# seed: the client's main is the bare repo's first main, as crate apply seeds it from GitHub: no push makes main.
seed() { git -C "$tmp/repo.git" fetch -q "$tmp/client" main:main; }

# land: HEAD into the queue, waiting for its verdict; prints the push's output. A land moves the client to main, as
# git land does, since main may hold the change as new commits.
land() {
  local out
  out=$(git push "$tmp/repo.git" "HEAD:refs/queue/$(git rev-parse HEAD)" 2>&1)
  if grep -q "crate: landed" <<<"$out"; then
    git fetch -q "$tmp/repo.git" main && git reset -q --keep FETCH_HEAD
  fi
  echo "$out"
}

# queue: HEAD into the queue with -o crate.wait=no, so the push returns once it is queued; prints its output.
queue() { git push -o crate.wait=no "$tmp/repo.git" "HEAD:refs/queue/$(git rev-parse HEAD)" 2>&1; }

# settle: wait, a minute at most, until the repo's worker, postsubmit, publish and cache are done: three looks in a row, a
# fifth of a second apart, find none, since one look can miss a process as it hands over to the next. One still
# running after the minute says so, so a case that then fails is read as slow, not wrong.
settle() {
  local quiet=0
  for _ in $(seq 1 300); do
    if pgrep -f "crate (queue|postsubmit|ship|cache) $tmp/repo.git" >/dev/null; then quiet=0; else quiet=$((quiet + 1)); fi
    [ "$quiet" -lt 3 ] || return 0
    sleep 0.2
  done
  echo "# settle: crate's worker, postsubmit, publish or cache still runs after a minute: $(pgrep -fa "crate (queue|postsubmit|ship|cache) $tmp/repo.git" | head -3)"
}

# sweep: what the suite's runs leave on the engine: the workers, and the kept volumes of its systems.
sweep() {
  pkill -f "crate (queue|postsubmit|ship|cache) $tmp" 2>/dev/null
  rm -rf "$tmp"
  "$engine" volume ls -q | grep -E '^crate-repo-(check-[0-9]+|postsubmit)-' | xargs -r "$engine" volume rm -f >/dev/null 2>&1
}
