#!/usr/bin/env bash
# release <version>: a new version of crate, outside the gate it ships, so a broken hook never blocks its own fix.
# From a clean main equal to GitHub's: run crate's check, tag v<version>, move the major tag (v1) to it, and push both.
# A server runs crate at the major tag, and takes the new version when it applies. A bad release is undone by moving
# the major tag back to the last good version: `git tag -f v1 v<good> && git push -f origin v1`.
set -euo pipefail
cd "$(dirname "$(readlink -f "$0")")/.."
version=${1:?usage: scripts/release.sh <major.minor.patch>}
[[ $version =~ ^([0-9]+)\.[0-9]+\.[0-9]+$ ]] || { echo "release: $version is not major.minor.patch" >&2; exit 2; }
major="v${BASH_REMATCH[1]}"
[ "$(git symbolic-ref --short HEAD)" = main ] || { echo "release: not on main" >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "release: the checkout has uncommitted changes" >&2; exit 1; }
git fetch -q origin main --tags
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || { echo "release: main is not GitHub's main" >&2; exit 1; }
! git rev-parse -q --verify "refs/tags/v$version" >/dev/null || { echo "release: v$version exists" >&2; exit 1; }
scripts/check.sh
git tag "v$version"
git tag -f "$major" "v$version" >/dev/null
git push -q origin "v$version"
git push -q -f origin "$major"
echo "release: v$version, and $major points at it"
