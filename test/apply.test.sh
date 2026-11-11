#!/usr/bin/env bash
# The tests of crate apply, on real git: a declaration makes each repo's bare repo, with crate's hooks and its copy to
# a stand-in GitHub, and seeds a new repo's main from it. Run again, it changes nothing.
set -uo pipefail
root=$(dirname "$(dirname "$(readlink -f "$0")")")
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
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

for r in a b; do
  git init -q --bare -b main "$tmp/github/$r.git"
  git -C "$tmp/github/$r.git" commit-tree -m one "$(git -C "$tmp/github/$r.git" hash-object -w -t tree /dev/null)" >"$tmp/$r.sha"
  git -C "$tmp/github/$r.git" update-ref refs/heads/main "$(cat "$tmp/$r.sha")"
done
conf="GIT_HOME=$tmp/git
GITHUB_BASE=$tmp/github
REPOS=\"a b\""

out=$("$root/bin/crate" apply - <<<"$conf" 2>&1)
if [ "$(git -C "$tmp/git/a.git" config core.hooksPath)" = "$root/hooks" ] && [ -d "$tmp/git/b.git/crate/work/.git" ]; then
  ok "each repo gets a bare repo with crate's hooks"
else not_ok "each repo gets a bare repo with crate's hooks" "$out"; fi

if [ "$(git -C "$tmp/git/a.git" rev-parse main)" = "$(cat "$tmp/a.sha")" ] && grep -q "a seeded" <<<"$out"; then
  ok "a new repo's main is seeded from GitHub's"
else not_ok "a new repo's main is seeded from GitHub's" "$out"; fi

if [ "$(git -C "$tmp/git/b.git" remote get-url github)" = "$tmp/github/b.git" ]; then ok "its copy to GitHub is set"
else not_ok "its copy to GitHub is set"; fi

out=$("$root/bin/crate" apply - <<<"$conf" 2>&1)
if [ -z "$out" ]; then ok "run again, it says nothing and changes nothing"
else not_ok "run again, it says nothing and changes nothing" "$out"; fi

conf="GIT_HOME=$tmp/git
GITHUB_BASE=$tmp/github
REPOS=\"a missing\""
if out=$("$root/bin/crate" apply - <<<"$conf" 2>&1); then
  not_ok "a repo GitHub cannot give fails the apply, and says why" "$out"
elif grep -q "missing has no main" <<<"$out"; then ok "a repo GitHub cannot give fails the apply, and says why"
else not_ok "a repo GitHub cannot give fails the apply, and says why" "$out"; fi

echo "1..$n"
exit "$failed"
