#!/usr/bin/env bash
# The tests of crate's hooks, on real git and the real engine: a throwaway bare repo with crate on it, and a tiny repo
# whose check runs in alpine and passes or fails as its file `verdict` says.
set -uo pipefail
root=$(dirname "$(dirname "$(readlink -f "$0")")")
alpine=docker.io/library/alpine@sha256:0f49f331d9212aff11b932863be4eb01cdf962969aa86dab7d1ee21ac6f5f52b
tmp=$(mktemp -d)
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
trap 'rm -rf "$tmp"; ${CRATE_ENGINE:-podman} volume rm -f crate-repo-check-node-modules crate-repo-postsubmit-node-modules >/dev/null 2>&1' EXIT
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

# A fresh bare repo with crate on it, a second bare repo standing in for GitHub, and a client clone.
setup() {
  cd "$tmp" && rm -rf "${tmp:?}"/*
  git init -q --bare -b main "$tmp/github.git"
  "$root/bin/crate" init "$tmp/repo.git" "$tmp/github.git" >/dev/null
  git init -q -b main "$tmp/client"
  cd "$tmp/client" || exit 1
  git config user.email t@t && git config user.name t
  mkdir scripts
  printf 'crate = "1"\nimage = "%s"\n' "$alpine" >crate.toml
  cat >scripts/check.sh <<'SH'
#!/bin/sh
echo "base=${CHECK_BASE:-none}"
[ ! -f slow ] || sleep 3
[ "$(cat verdict)" = pass ] || { echo "check failed at: verdict"; exit 1; }
echo "check passed"
SH
  chmod +x scripts/check.sh
  echo pass >verdict
  git add -A && git commit -qm one
}

main_of() { git -C "$1" rev-parse --verify -q refs/heads/main || echo none; }

# a green push moves main, and the first push has no base
setup
out=$(git push -q origin 2>&1; git push "$tmp/repo.git" main 2>&1)
if [ "$(main_of "$tmp/repo.git")" = "$(git rev-parse HEAD)" ] && grep -q "base=none" <<<"$out"; then
  ok "a green push moves main"
else not_ok "a green push moves main" "$out"; fi

# the post-receive copied main to github
if [ "$(main_of "$tmp/github.git")" = "$(git rev-parse HEAD)" ]; then ok "main is copied to github"
else not_ok "main is copied to github"; fi

# a red push is refused with its output, and main has not moved
before=$(main_of "$tmp/repo.git")
echo fail >verdict && git commit -qam red
out=$(git push "$tmp/repo.git" main 2>&1)
if [ "$(main_of "$tmp/repo.git")" = "$before" ] && grep -q "check failed at: verdict" <<<"$out" &&
  grep -q "base=$before" <<<"$out"; then
  ok "a red push is refused with its output, checked against the old main"
else not_ok "a red push is refused with its output, checked against the old main" "$out"; fi

# a green push after a refused one still checks: the checkout recovered from the lost objects
echo pass >verdict && git commit -qam green
out=$(git push "$tmp/repo.git" main 2>&1)
if [ "$(main_of "$tmp/repo.git")" = "$(git rev-parse HEAD)" ]; then ok "a green push after a refused one moves main"
else not_ok "a green push after a refused one moves main" "$out"; fi

# a stale push is refused, and main has not moved
before=$(main_of "$tmp/repo.git")
git reset -q --hard HEAD~1 && echo pass >other && git add other && git commit -qm stale
out=$(git push "$tmp/repo.git" main 2>&1)
if [ "$(main_of "$tmp/repo.git")" = "$before" ]; then ok "a stale push is refused"
else not_ok "a stale push is refused" "$out"; fi
git reset -q --hard "$before"

# a branch push runs no check
echo fail >verdict && git commit -qam wip
out=$(git push "$tmp/repo.git" HEAD:refs/heads/wip 2>&1)
if git -C "$tmp/repo.git" rev-parse -q --verify refs/heads/wip >/dev/null && ! grep -q "crate: checking" <<<"$out"; then
  ok "a branch push runs no check"
else not_ok "a branch push runs no check" "$out"; fi
git reset -q --hard "$before"

# deleting main is refused
out=$(git push "$tmp/repo.git" :main 2>&1)
if [ "$(main_of "$tmp/repo.git")" = "$before" ]; then ok "deleting main is refused"
else not_ok "deleting main is refused" "$out"; fi

# two pushes at once: one lands, the other is told main moved, and the checks never overlap
git clone -q "$tmp/repo.git" "$tmp/second"
git -C "$tmp/second" config user.email t@t && git -C "$tmp/second" config user.name t
touch slow && git add slow && git commit -qm first
(cd "$tmp/second" && touch slow b && git add slow b && git commit -qm second)
git push "$tmp/repo.git" main >"$tmp/a.out" 2>&1 &
a=$!
sleep 1
git -C "$tmp/second" push "$tmp/repo.git" main >"$tmp/b.out" 2>&1 &
b=$!
wait $a; ra=$?
wait $b; rb=$?
starts=$(grep -c "start" "$tmp/repo.git/crate/log")
order=$(grep ' check ' "$tmp/repo.git/crate/log" | tail -2 | awk '{print $NF}' | tr '\n' ' ')
if [ $ra -eq 0 ] && [ $rb -ne 0 ] && grep -q "main moved" "$tmp/b.out" && [ "$order" = "start pass " ]; then
  ok "two pushes at once: one lands, the other waits and is told main moved"
else not_ok "two pushes at once: one lands, the other waits and is told main moved" "a=$ra b=$rb starts=$starts order=$order
$(cat "$tmp/a.out" "$tmp/b.out")"; fi

# a check past its limit is stopped, the push refused, and the next push is not held by it
setup
git push -q "$tmp/repo.git" main 2>/dev/null
printf 'crate = "1"\nimage = "%s"\ntimeout = "3"\n' "$alpine" >crate.toml
touch slow && sed -i 's/sleep 3/sleep 30/' scripts/check.sh && git add -A && git commit -qm hang
before=$(main_of "$tmp/repo.git")
start=$(date +%s)
out=$(git push "$tmp/repo.git" main 2>&1)
took=$(($(date +%s) - start))
if [ "$(main_of "$tmp/repo.git")" = "$before" ] && grep -q "ran past its limit of 3s" <<<"$out" && [ "$took" -lt 25 ]; then
  ok "a check past its limit is stopped, and the push refused"
else not_ok "a check past its limit is stopped, and the push refused" "took=${took}s
$out"; fi
rm slow && git commit -qam unhang
if git push -q "$tmp/repo.git" main 2>/dev/null; then ok "the next push is not held by the stopped check"
else not_ok "the next push is not held by the stopped check"; fi

# image "dev": the repo's own scripts/dev-image.sh names the image, run on the pushed commit
setup
git push -q "$tmp/repo.git" main 2>/dev/null
cat >scripts/dev-image.sh <<SH
#!/bin/sh
# a stand-in: it reads the pushed commit, as the real one hashes it, then names the image
[ "\$1" = local ] && git ls-tree HEAD crate.toml >/dev/null && echo "built nothing" && echo "$alpine"
SH
chmod +x scripts/dev-image.sh
printf 'crate = "1"\nimage = "dev"\n' >crate.toml
git add -A && git commit -qm dev
out=$(git push "$tmp/repo.git" main 2>&1)
if [ "$(main_of "$tmp/repo.git")" = "$(git rev-parse HEAD)" ] && grep -q "checking .* in $alpine" <<<"$out"; then
  ok "image dev: the check runs in the image the repo's dev-image.sh names"
else not_ok "image dev: the check runs in the image the repo's dev-image.sh names" "$out"; fi

echo "1..$n"
exit "$failed"
