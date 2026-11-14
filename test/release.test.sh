#!/usr/bin/env bash
# The tests of crate's release, on real git and the real engine: a bare repo with crate on it, a second standing in for
# GitHub, and a client that lands commits of each Conventional Commits type and reads the version tags that follow.
set -uo pipefail
root=${CRATE_ROOT:-$(dirname "$(dirname "$(readlink -f "$0")")")}  # the install to test: the built layout
alpine=docker.io/library/alpine@sha256:0f49f331d9212aff11b932863be4eb01cdf962969aa86dab7d1ee21ac6f5f52b
tmp=$(mktemp -d)
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
engine=${CRATE_ENGINE:-podman}
trap sweep EXIT
# shellcheck source=test/lib.sh
. "$(dirname "$(readlink -f "$0")")/lib.sh"

# setup <crate.toml> [<first tag>...]: a fresh repo on crate whose crate.toml is the argument, its first commit landed
# with the tags given, and a client in it.
setup() {
  cd "$tmp" && rm -rf "${tmp:?}"/*
  git init -q --bare -b main "$tmp/github.git"
  "$root/bin/crate" init "$tmp/repo.git" "$tmp/github.git" >/dev/null
  git init -q -b main "$tmp/client"
  cd "$tmp/client" || exit 1
  mkdir scripts it
  printf 'crate = "1"\nimage = "%s"\n%s\n' "$alpine" "$1" >crate.toml
  printf '#!/bin/sh\necho "check passed"\n' >scripts/check.sh
  chmod +x scripts/check.sh
  touch it/a
  git add -A && git commit -qm "chore: start"
  shift
  seed
  # A first version is tagged by hand on the server: crate refuses a version tag pushed by a client.
  for tag in "$@"; do git -C "$tmp/repo.git" tag "$tag" main; done
}

# change <file> <message>: commit a change to the file, and land it.
change() {
  echo "$RANDOM" >>"$1" && git add "$1" && git commit -qm "$2" && land
}

tags() { git -C "$1" tag --sort=v:refname | tr '\n' ' '; }

# case <name> <want tags> <got tags>
check_tags() {
  if [ "$3" = "$2" ]; then ok "$1"; else not_ok "$1" "want: $2
got:  $3"; fi
}

setup 'ship = "npm"' v0.1.0
change a "feat: one" >/dev/null
check_tags "before 1.0.0, feat bumps patch, tagged on main" "v0.1.0 v0.1.1 " "$(tags "$tmp/repo.git")"
check_tags "every version tag is copied to github" "v0.1.0 v0.1.1 " "$(tags "$tmp/github.git")"
if [ "$(git -C "$tmp/repo.git" rev-parse "v0.1.1^{commit}")" = "$(git rev-parse HEAD)" ]; then ok "the tag is on the new main"
else not_ok "the tag is on the new main"; fi
change a "fix(a): two" >/dev/null
check_tags "fix bumps patch" "v0.1.0 v0.1.1 v0.1.2 " "$(tags "$tmp/repo.git")"
change a "perf: three" >/dev/null
check_tags "perf bumps patch" "v0.1.0 v0.1.1 v0.1.2 v0.1.3 " "$(tags "$tmp/repo.git")"
change a "docs: four" >/dev/null
change a "chore: five" >/dev/null
check_tags "docs and chore make no version" "v0.1.0 v0.1.1 v0.1.2 v0.1.3 " "$(tags "$tmp/repo.git")"
change a "feat!: six" >/dev/null
check_tags "before 1.0.0, a breaking change bumps minor" "v0.1.0 v0.1.1 v0.1.2 v0.1.3 v0.2.0 " "$(tags "$tmp/repo.git")"

setup 'ship = "npm"' v1.4.2
change a "fix: one" >/dev/null
change a "feat: two" >/dev/null
check_tags "from 1.0.0, feat bumps minor" "v1.4.2 v1.4.3 v1.5.0 " "$(tags "$tmp/repo.git")"
change a "feat(a)!: three" >/dev/null
check_tags "from 1.0.0, a breaking change bumps major" "v1.4.2 v1.4.3 v1.5.0 v2.0.0 " "$(tags "$tmp/repo.git")"
echo x >>a && git commit -qam "fix: three" && echo x >>a && git commit -qam "feat: four

BREAKING CHANGE: a footer" && land >/dev/null
check_tags "a BREAKING CHANGE footer is breaking, and one land of three commits is one version" \
  "v1.4.2 v1.4.3 v1.5.0 v2.0.0 v3.0.0 " "$(tags "$tmp/repo.git")"

setup 'ship = "npm"
packages = ". it"' v0.5.0 it-v0.2.0
change it/a "feat: in it" >/dev/null
check_tags "a change in a package folder versions that package only" "it-v0.2.0 it-v0.2.1 v0.5.0 " "$(tags "$tmp/repo.git")"
change a "fix: at the root" >/dev/null
check_tags "a change outside the folders versions the root only" "it-v0.2.0 it-v0.2.1 v0.5.0 v0.5.1 " "$(tags "$tmp/repo.git")"
check_tags "both packages' tags are copied to github" "it-v0.2.0 it-v0.2.1 v0.5.0 v0.5.1 " "$(tags "$tmp/github.git")"

setup ''
change a "feat: one" >/dev/null
check_tags "a repo that names no ship has no versions" "" "$(tags "$tmp/repo.git")"

setup 'ship = "npm"'
out=$(change a "feat: one")
if [ -z "$(tags "$tmp/repo.git")" ] && grep -q "no v<version> tag to count from" <<<"$out" &&
  [ "$(git -C "$tmp/repo.git" rev-parse main)" = "$(git rev-parse HEAD)" ]; then
  ok "a package with no tag yet makes none, says so, and main still moves"
else not_ok "a package with no tag yet makes none, says so, and main still moves" "$out"; fi

# the message for a package with no tag gives the command that sets a first version in the bare repo, the same one the
# refusal of a pushed version tag gives
setup 'ship = "npm"'
out=$(change a "feat: one")
git tag v9.9.9
refusal=$(git push "$tmp/repo.git" v9.9.9 2>&1)
rel_cmd=$(grep 'to count from' <<<"$out" | grep -oE 'git( -C [^ ]+)? tag' | head -1)
ref_cmd=$(grep 'crate: ' <<<"$refusal" | grep -oE 'git( -C [^ ]+)? tag' | head -1)
if [ -n "$rel_cmd" ] && [ "$rel_cmd" = "$ref_cmd" ]; then ok "the message for a package with no tag gives the same command as the refusal"
else not_ok "the message for a package with no tag gives the same command as the refusal" "release: $rel_cmd
refusal: $ref_cmd"; fi

echo "1..$n"
exit "$failed"
