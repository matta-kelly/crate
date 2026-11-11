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

# toml_value <key>: a value from the crate.toml on stdin, as `key = "value"`. Empty when it is not there.
toml_value() { sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p" | head -1; }

# config_of <key>: a value from crate.toml at the checkout.
config_of() {
  [ -f "$work/crate.toml" ] || return 0
  toml_value "$1" <"$work/crate.toml"
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
  local old=$1 new=$2 bare status now limit base=()
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
  if ! work_git checkout -q -f --detach "$new"; then
    echo "crate: could not check out ${new:0:12}, so its tree is not checked. Push again" >&2
    log "check $(repo_name) ${new:0:12} fail: checkout"
    return 1
  fi
  work_git clean -q -fdx -e node_modules
  limit=$(config_of timeout)
  status=0
  zero "$old" || base=(-e "CHECK_BASE=$old")
  in_image check "${limit:-900}" "${base[@]}" -- || status=$?
  # A refused push takes its objects with it: point the checkout back at a commit that stays.
  if [ "$status" -ne 0 ] && ! zero "$old"; then work_git checkout -q -f --detach "$old"; fi
  log "check $(repo_name) ${new:0:12} $([ "$status" -eq 0 ] && echo pass || echo fail)"
  return "$status"
}

# version_tag <tag>: true when the tag name is a version of a package of main, v<semver> or <folder>-v<semver>, in a repo
# that ships. Only release() makes those, in the bare repo.
version_tag() {
  local toml ship packages pkg prefix
  toml=$(git show refs/heads/main:crate.toml 2>/dev/null) || return 1
  ship=$(toml_value ship <<<"$toml")
  [ -n "$ship" ] || return 1
  packages=$(toml_value packages <<<"$toml")
  for pkg in ${packages:-.}; do
    if [ "$pkg" = . ]; then prefix=v; else prefix="$pkg-v"; fi
    [[ $1 =~ ^${prefix//./\\.}[0-9]+\.[0-9]+\.[0-9]+$ ]] && return 0
  done
  return 1
}

# in_image <kind> <limit> [<engine args>...] -- [<check.sh args>...]: run the repo's scripts/check.sh in its image, at
# the checkout $work, as node, with its own packages volume, stopped past <limit> seconds. <kind> names the container
# and the volume, so the check and postsubmit never share either.
in_image() {
  local kind=$1 limit=$2 bare image name status=0 extra=()
  shift 2
  while [ "$#" -gt 0 ] && [ "$1" != -- ]; do extra+=("$1"); shift; done
  [ "$#" -eq 0 ] || shift
  bare=$(cd "$GIT_DIR" && pwd)
  image=$(image_of) || return 1
  if [ "$kind" = check ]; then echo "crate: checking $(work_git rev-parse --short=12 HEAD) in $image"
  else echo "crate: $kind of $(work_git rev-parse --short=12 HEAD) in $image"; fi
  name="crate-$(repo_name)-$kind"
  "$engine" rm -f "$name" >/dev/null 2>&1 || true
  timeout -s KILL "$limit" "$engine" run --rm --name "$name" --userns=keep-id:uid=1000,gid=1000 --user 1000:1000 -e HOME=/home/node \
    --cpus=3 --cpu-shares=256 \
    -v "$work:$work" -v "$bare:$bare:ro" -v "crate-$(repo_name)-$kind-node-modules:$work/node_modules:U" \
    -e GIT_ALTERNATE_OBJECT_DIRECTORIES="${GIT_QUARANTINE_PATH:-}" \
    "${extra[@]}" \
    -w "$work" "$image" scripts/check.sh "$@" || status=$?
  if [ "$status" -eq 124 ] || [ "$status" -eq 137 ]; then
    "$engine" rm -f "$name" >/dev/null 2>&1 || true
    echo "crate: the $kind ran past its limit of ${limit}s, and was stopped" >&2
  fi
  return "$status"
}

# Release: every move of main that a package's commits ask a bump for is a version of that package, read from the
# commit types (Conventional Commits 1.0) as semver (SemVer 2.0). The tag is the truth, never committed back.
#
# crate.toml's `ship` names how a version ships (npm); a repo that names none has no versions. `packages` names the
# package folders, "." when it names none. The package at "." is tagged v<version>; one in a folder, <folder>-v<version>.
# A package's commits are the ones that touch its folder, and, for ".", touch no other package's folder.

# bump_of <range> <pathspec>...: the bump the commits ask for. The strongest one wins: major for a breaking change (a
# `!` after the type, or a `BREAKING CHANGE:` footer), minor for feat, patch for fix or perf, none for any other type.
bump_of() {
  local range=$1
  shift
  if git log --format=%s "$range" -- "$@" | grep -qE '^[a-z]+(\([^)]*\))?!:' ||
    git log --format=%b "$range" -- "$@" | grep -qE '^BREAKING[ -]CHANGE:'; then
    echo major
  elif git log --format=%s "$range" -- "$@" | grep -qE '^feat(\([^)]*\))?:'; then
    echo minor
  elif git log --format=%s "$range" -- "$@" | grep -qE '^(fix|perf)(\([^)]*\))?:'; then
    echo patch
  fi
}

# next_version <major.minor.patch> <bump>: the version after it. Before 1.0.0, each bump moves down one place, as npm's
# caret reads a 0.x version (^0.3.1 takes 0.3.x, never 0.4.0): a breaking change bumps minor, and feat bumps patch
# (release-please's bump-minor-pre-major and bump-patch-for-minor-pre-major). A package leaves 0.x only by a v1.0.0
# tag made by hand.
next_version() {
  local major minor patch bump=$2
  IFS=. read -r major minor patch <<<"$1"
  if [ "$major" -eq 0 ]; then
    case "$bump" in major) bump="minor" ;; minor) bump="patch" ;; esac
  fi
  case "$bump" in
    major) echo "$((major + 1)).0.0" ;;
    minor) echo "$major.$((minor + 1)).0" ;;
    patch) echo "$major.$minor.$((patch + 1))" ;;
  esac
}

# release [<commit>]: tag the commit, main when none is given, with each package's next version, and copy the version tags to github. Holds the repo's release
# lock (not the check's, so a push never waits on another's check), so two moves of main are released in order, from
# main as it stands.
release() {
  local bare main toml ship packages pkg other prefix last bump version paths
  bare=$(cd "$GIT_DIR" && pwd)
  exec 8>"$bare/crate/release.lock"
  flock 8
  main=${1:-$(git rev-parse --verify -q refs/heads/main)} || return 0
  [ -n "$main" ] || return 0
  toml=$(git show "$main:crate.toml" 2>/dev/null) || return 0
  ship=$(toml_value ship <<<"$toml")
  [ -n "$ship" ] || return 0
  packages=$(toml_value packages <<<"$toml")
  packages=${packages:-.}
  for pkg in $packages; do
    if [ "$pkg" = . ]; then prefix=v; else prefix="$pkg-v"; fi
    paths=("$pkg")
    if [ "$pkg" = . ]; then
      for other in $packages; do [ "$other" = . ] || paths+=(":(exclude)$other"); done
    fi
    if ! last=$(git describe --tags --abbrev=0 --match "${prefix}[0-9]*" "$main" 2>/dev/null); then
      echo "crate: $pkg has no ${prefix}<version> tag to count from; tag its first version by hand" >&2
      log "release $(repo_name) $pkg none: no tag to count from"
      continue
    fi
    bump=$(bump_of "$last..$main" "${paths[@]}")
    [ -n "$bump" ] || continue
    version="$prefix$(next_version "${last#"$prefix"}" "$bump")"
    git tag "$version" "$main"
    echo "crate: $pkg is $version, after $last"
    log "release $(repo_name) $version ${main:0:12}"
  done
  # Every version tag, so one a failed copy left behind goes with the next.
  git remote get-url github >/dev/null 2>&1 || return 0
  for pkg in $packages; do
    if [ "$pkg" = . ]; then prefix=v; else prefix="$pkg-v"; fi
    if ! git push -q github "refs/tags/$prefix*:refs/tags/$prefix*" 2>&1; then
      log "release $(repo_name) $prefix copy fail"
      echo "crate: the copy of the version tags to github failed; the next move copies again" >&2
    fi
  done
}

# Postsubmit: the slow sets, too slow to hold a land, run on each new main after it moves. crate.toml's `postsubmit`
# names them, as arguments to scripts/check.sh, and `postsubmit_timeout` their limit (3600 seconds when it names none).
# A repo with postsubmit is released only after it passes, so no version is made from a main it failed.
#
# State, beside the check's, in $GIT_DIR/crate:
#   post/               postsubmit's checkout, kept warm as the check's is
#   postsubmit/green    the last main that passed
#   postsubmit/<sha>.log  each run's output, kept

# postsubmit_of <commit>: the sets the commit's crate.toml names for postsubmit; empty for none.
postsubmit_of() { git show "$1:crate.toml" 2>/dev/null | toml_value postsubmit; }

# postsubmit: the slow sets on main as it stands, one run at a time, so a run that waited tests every land since the
# last. Green: main is the new green, and is released. Red: the commits since the last green are reverted on main.
postsubmit() {
  local bare main green status=0 out limit
  bare=$(cd "$GIT_DIR" && pwd)
  work="$bare/crate/post"
  mkdir -p "$bare/crate/postsubmit"
  if [ ! -d "$work/.git" ]; then
    env -u GIT_DIR git init -q "$work"
    echo "$bare/objects" >"$work/.git/objects/info/alternates"
  fi
  exec 7>"$bare/crate/postsubmit.lock"
  flock 7
  main=$(git rev-parse --verify -q refs/heads/main) || return 0
  green=$(cat "$bare/crate/postsubmit/green" 2>/dev/null || true)
  [ "$main" != "$green" ] || return 0
  # The tree a green main had passes again: a revert back to it, or a change of nothing.
  if [ -n "$green" ] && [ "$(git rev-parse "$main^{tree}")" = "$(git rev-parse "$green^{tree}")" ]; then
    echo "$main" >"$bare/crate/postsubmit/green"
    log "postsubmit $(repo_name) ${main:0:12} pass: the tree of green ${green:0:12}"
    return 0
  fi
  work_git checkout -q -f --detach "$main"
  work_git clean -q -fdx -e node_modules
  limit=$(config_of postsubmit_timeout)
  out="$bare/crate/postsubmit/${main:0:12}.log"
  log "postsubmit $(repo_name) ${main:0:12} start"
  # shellcheck disable=SC2046 # the sets are a list of words
  in_image postsubmit "${limit:-3600}" -- $(config_of postsubmit) >"$out" 2>&1 || status=$?
  if [ "$status" -eq 0 ]; then
    echo "$main" >"$bare/crate/postsubmit/green"
    log "postsubmit $(repo_name) ${main:0:12} pass"
    release "$main"
    return 0
  fi
  log "postsubmit $(repo_name) ${main:0:12} fail"
  if [ -z "$green" ]; then
    log "postsubmit $(repo_name) ${main:0:12} not reverted: no green main to go back to"
    return 1
  fi
  revert_since "$green" "$main" "$out"
}

# revert_since <green> <red> <log>: one commit on main that undoes green..red, landed through the check like any push,
# with the failure's last lines in its message. When main moved meanwhile, it goes again on the new main, three times.
revert_since() {
  local green=$1 red=$2 out=$3 bare tries=0 message author
  bare=$(cd "$GIT_DIR" && pwd)
  author=$(git log -1 --format='%an <%ae>' "$red")
  message="revert: $(git rev-list --count "$green..$red") commits, red in postsubmit

Reverts ${green:0:12}..${red:0:12}, which failed postsubmit:
$(git log --format='- %h %s' "$green..$red")

The failure, its last lines ($out):
$(tail -n 30 "$out")"
  while [ "$tries" -lt 3 ]; do
    tries=$((tries + 1))
    work_git checkout -q -f --detach "$(git rev-parse refs/heads/main)"
    if ! work_git -c user.name=crate -c "user.email=crate@$(hostname)" revert --no-commit "$green..$red" >/dev/null ||
      ! work_git -c user.name=crate -c "user.email=crate@$(hostname)" commit -q --author="$author" -m "$message"; then
      work_git revert --abort >/dev/null 2>&1 || true
      log "postsubmit $(repo_name) ${red:0:12} not reverted: the revert does not apply; it needs a hand"
      return 1
    fi
    if work_git push -q "$bare" HEAD:refs/heads/main >>"$out" 2>&1; then
      log "postsubmit $(repo_name) ${red:0:12} reverted by $(work_git rev-parse --short=12 HEAD)"
      return 0
    fi
  done
  log "postsubmit $(repo_name) ${red:0:12} not reverted: main kept moving, or the revert's check failed"
  return 1
}
