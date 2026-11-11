# shellcheck shell=bash
# crate's library, sourced by its hooks and its command. A hook runs in the bare repo, with GIT_DIR set by git.
#
# The state of a bare repo lives beside it, in $GIT_DIR/crate:
#   work/   the check's checkout: a repo of its own that borrows the bare repo's objects (alternates), reused across
#           pushes so its caches stay warm
#   lock    one check at a time per repo: the merge queue
#   log     one line per check and per mirror push
# Env: CRATE_ENGINE (podman).

engine=${CRATE_ENGINE:-podman}

# zero <sha>: true for git's all-zeros id, which stands for "no commit" (a new ref, or a deletion).
zero() { [[ $1 =~ ^0+$ ]]; }

log() { echo "$(date -u +%FT%TZ) $*" >>"$GIT_DIR/crate/log"; }

# The bare repo's name, from its folder: que-pasa.git gives que-pasa.
repo_name() { basename "$(cd "$GIT_DIR" && pwd)" .git; }

# work_git <args>: git in the check's checkout. A hook's GIT_DIR points at the bare repo, so it is taken out. The
# pushed objects sit in git's quarantine until the hook passes; the quarantine is added as an alternate, so the
# checkout can read them.
work_git() {
  env -u GIT_DIR -u GIT_QUARANTINE_PATH -u GIT_OBJECT_DIRECTORY \
    GIT_ALTERNATE_OBJECT_DIRECTORIES="${GIT_QUARANTINE_PATH:-}" git -C "$work" "$@"
}

# config_of <key>: a value from crate.toml at the checkout, as `key = "value"`. Empty when it is not there.
config_of() {
  [ -f "$work/crate.toml" ] || return 0
  sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p" "$work/crate.toml" | head -1
}

# The check's time limit is crate.toml's `timeout`, in seconds, 900 when it names none. A check past it is stopped, the
# push refused, and the lock freed.

# image_of: the image the check runs in. "dev" is the repo's own dev image, put on this machine by its
# scripts/dev-image.sh (its copy when the hash is unchanged, so warm); anything else is an image ref.
image_of() {
  local image
  image=$(config_of image)
  case "$image" in
    "") echo "crate: crate.toml names no image" >&2; return 1 ;;
    dev) (cd "$work" && env -u GIT_DIR -u GIT_QUARANTINE_PATH -u GIT_OBJECT_DIRECTORY \
      GIT_ALTERNATE_OBJECT_DIRECTORIES="${GIT_QUARANTINE_PATH:-}" scripts/dev-image.sh local) | tail -1 ;;
    *) echo "$image" ;;
  esac
}

# check <old> <new>: main may move from <old> to <new> only when the repo's check passes on <new>, which is the tree
# main becomes. Holds the repo's lock for the whole check.
check() {
  local old=$1 new=$2 bare image status now limit name base=()
  bare=$(cd "$GIT_DIR" && pwd)
  work="$bare/crate/work"
  exec 9>"$bare/crate/lock"
  flock 9
  # Another push moved main while this one waited: its check would test a tree main never becomes.
  now=$(git rev-parse --verify -q refs/heads/main || true)
  if { zero "$old" && [ -n "$now" ]; } || { ! zero "$old" && [ "$now" != "$old" ]; }; then
    echo "crate: main moved while this push waited. Pull with rebase, and push again" >&2
    return 1
  fi
  if ! zero "$old" && ! git merge-base --is-ancestor "$old" "$new"; then
    echo "crate: not a fast-forward of main. Pull with rebase, and push again" >&2
    return 1
  fi
  log "check $(repo_name) ${new:0:12} start"
  work_git checkout -q -f --detach "$new"
  work_git clean -q -fdx -e node_modules
  image=$(image_of) || return 1
  echo "crate: checking ${new:0:12} in $image"
  zero "$old" || base=(-e "CHECK_BASE=$old")
  limit=$(config_of timeout)
  limit=${limit:-900}
  name="crate-$(repo_name)-check"
  "$engine" rm -f "$name" >/dev/null 2>&1 || true
  status=0
  timeout -s KILL "$limit" "$engine" run --rm --name "$name" --userns=keep-id:uid=1000,gid=1000 --user 1000:1000 -e HOME=/home/node \
    --cpus=3 --cpu-shares=256 \
    -v "$work:$work" -v "$bare:$bare:ro" -v "crate-$(repo_name)-node-modules:$work/node_modules:U" \
    -e GIT_ALTERNATE_OBJECT_DIRECTORIES="${GIT_QUARANTINE_PATH:-}" \
    "${base[@]}" \
    -w "$work" "$image" scripts/check.sh || status=$?
  if [ "$status" -eq 124 ] || [ "$status" -eq 137 ]; then
    "$engine" rm -f "$name" >/dev/null 2>&1 || true
    echo "crate: the check ran past its limit of ${limit}s, and was stopped" >&2
  fi
  # A refused push takes its objects with it: point the checkout back at a commit that stays.
  if [ "$status" -ne 0 ] && ! zero "$old"; then work_git checkout -q -f --detach "$old"; fi
  log "check $(repo_name) ${new:0:12} $([ "$status" -eq 0 ] && echo pass || echo fail)"
  return "$status"
}
