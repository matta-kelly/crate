#!/usr/bin/env bash
# The tests of crate's hooks, on real git and the real engine: a throwaway bare repo with crate on it, and a tiny repo
# whose check runs in alpine and passes or fails as its file `verdict` says.
set -uo pipefail
repo=$(dirname "$(dirname "$(readlink -f "$0")")")
root=${CRATE_ROOT:-$repo}  # the install to test: the built layout
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

# steps: the steps of the repo's last run of kind, each step:result, from its record.
steps() {
  jq -r -s --arg k "$1" '[.[] | select(.kind == $k)] | (last.run) as $r | map(select(.run == $r) | "\(.step):\(.result)") | join(" ")' \
    "$tmp/repo.git/crate/log.jsonl" 2>/dev/null
}

# a green push moves main, and the first push has no base
setup
out=$(git push -q origin 2>&1; git push "$tmp/repo.git" main 2>&1)
if [ "$(main_of "$tmp/repo.git")" = "$(git rev-parse HEAD)" ] && grep -q "base=none" <<<"$out"; then
  ok "a green push moves main"
else not_ok "a green push moves main" "$out"; fi

# the post-receive copied main to github
if [ "$(main_of "$tmp/github.git")" = "$(git rev-parse HEAD)" ]; then ok "main is copied to github"
else not_ok "main is copied to github"; fi

# the check is a run on the repo's record, each step timed, and its summary is in the push's output
want="lock:pass image:pass hold:pass tools:pass check:pass total:pass"
if [ "$(steps check)" = "$want" ] && [ "$(steps mirror)" = "total:pass" ] &&
  grep -q "crate: lock [0-9]*s, image [0-9]*s, hold .*, check [0-9]*s; total [0-9]*s, pass" <<<"$out" &&
  [ ! -e "$tmp/repo.git/crate/log" ]; then
  ok "the check and the copy are runs on the record, and the push shows the check's summary"
else not_ok "the check and the copy are runs on the record, and the push shows the check's summary" "check: $(steps check)
mirror: $(steps mirror)
$out"; fi

# a red push is refused with its output, and main has not moved
before=$(main_of "$tmp/repo.git")
echo fail >verdict && git commit -qam red
out=$(git push "$tmp/repo.git" main 2>&1)
if [ "$(main_of "$tmp/repo.git")" = "$before" ] && grep -q "check failed at: verdict" <<<"$out" &&
  grep -q "base=$before" <<<"$out"; then
  ok "a red push is refused with its output, checked against the old main"
else not_ok "a red push is refused with its output, checked against the old main" "$out"; fi
if [ "$(steps check)" = "lock:pass image:pass hold:pass tools:pass check:fail total:fail" ]; then
  ok "a red check's record names the step that failed"
else not_ok "a red check's record names the step that failed" "$(steps check)"; fi

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
order=$(jq -r -s '[.[] | select(.kind == "check" and .step == "total")] | .[-2:] | map(.result + "/" + (.note // "" | split(" ")[0:2] | join(" "))) | join(" ")' "$tmp/repo.git/crate/log.jsonl")
if [ $ra -eq 0 ] && [ $rb -ne 0 ] && grep -q "main moved" "$tmp/b.out" && [ "$order" = "pass/ refused/main moved" ]; then
  ok "two pushes at once: one lands, the other waits and is told main moved"
else not_ok "two pushes at once: one lands, the other waits and is told main moved" "a=$ra b=$rb order=$order
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

# image "dev": the check runs in crate's own dev image of the pushed commit, put on this machine by crate dev-image
# local: here its copy, tagged by hand, so the test builds nothing
setup
git push -q "$tmp/repo.git" main 2>/dev/null
printf 'crate = "2"\nname = "repo"\nimage = "dev"\n\n[dev]\nimage = "localhost/crate-test-dev"\ninputs = ["Containerfile"]\ncontainerfile = "Containerfile"\n' >crate.toml
echo "FROM $alpine" >Containerfile
git add -A && git commit -qm dev
dev="localhost/crate-test-dev:$("$root/bin/crate" dev-image hash)"
${CRATE_ENGINE:-podman} tag "$alpine" "$dev"
out=$(git push "$tmp/repo.git" main 2>&1)
${CRATE_ENGINE:-podman} rmi -f "$dev" >/dev/null 2>&1
if [ "$(main_of "$tmp/repo.git")" = "$(git rev-parse HEAD)" ] && grep -q "checking .* in $dev" <<<"$out"; then
  ok "image dev: the check runs in crate's dev image of the pushed commit"
else not_ok "image dev: the check runs in crate's dev image of the pushed commit" "$out"; fi

# image "dev" in a repo that does not declare itself: no name, or no [dev] inputs. crate guesses neither, so the push
# is refused with the key it lacks, and main has not moved
for row in 'no name|crate = "2"\nimage = "dev"\n\n[dev]\ninputs = ["Containerfile"]\ncontainerfile = "Containerfile"\n|needs name' \
  'no [dev] inputs|crate = "2"\nname = "repo"\nimage = "dev"\n|needs [dev] inputs'; do
  IFS='|' read -r what toml says <<<"$row"
  setup
  git push -q "$tmp/repo.git" main 2>/dev/null
  before=$(main_of "$tmp/repo.git")
  printf '%b' "$toml" >crate.toml
  echo "FROM $alpine" >Containerfile
  git add -A && git commit -qm "dev, $what"
  out=$(git push "$tmp/repo.git" main 2>&1)
  if [ "$(main_of "$tmp/repo.git")" = "$before" ] && grep -qF "$says" <<<"$out"; then
    ok "image dev with $what: the push is refused, and says it $says"
  else not_ok "image dev with $what: the push is refused, and says it $says" "$out"; fi
done

# a failed checkout: the check's checkout cannot take the pushed commit (a stale index.lock makes the real
# `git checkout` fail), though the tree it still holds would pass. The push is refused with crate's reason, the check
# does not run on that stale tree, and main has not moved
setup
git push -q "$tmp/repo.git" main 2>/dev/null
before=$(main_of "$tmp/repo.git")
echo more >extra && git add extra && git commit -qm two
touch "$tmp/repo.git/crate/work/.git/index.lock"
out=$(git push "$tmp/repo.git" main 2>&1)
rc=$?
reason=$(grep 'crate: ' <<<"$out" | grep -v 'crate: checking')
if [ "$rc" -ne 0 ] && [ "$(main_of "$tmp/repo.git")" = "$before" ] && ! grep -q "check passed" <<<"$out" && [ -n "$reason" ]; then
  ok "a failed checkout: the push is refused with crate's reason, the check does not run, main has not moved"
else not_ok "a failed checkout: the push is refused with crate's reason, the check does not run, main has not moved" "rc=$rc
$out"; fi

tags_of() { git -C "$1" tag | tr '\n' ' '; }

# push_tags <tag>...: tag HEAD with each and push it alone to the server. Prints the tags the server took.
push_tags() {
  local t
  for t in "$@"; do
    git tag "$t"
    git push "$tmp/repo.git" "$t" >/dev/null 2>&1
  done
  tags_of "$tmp/repo.git"
}

# a version tag is refused on every repo, whatever its settings say: one that ships a package folder, one that ships
# nothing, and one with no main yet. Each row is a tag shape release counts from: full, short, prerelease, a folder's.
setup
printf 'crate = "1"\nimage = "%s"\nship = "npm"\npackages = ". it"\n' "$alpine" >crate.toml
mkdir it && touch it/a
git add -A && git commit -qm "chore: ship"
git push -q "$tmp/repo.git" main 2>/dev/null
got=$(push_tags v1.2.3 v2 v2.0.0-rc.1 it-v0.3.0)
if [ -z "$got" ] && [ -z "$(tags_of "$tmp/github.git")" ]; then ok "a repo that ships refuses version tags, and they are on neither server"
else not_ok "a repo that ships refuses version tags, and they are on neither server" "repo: $got"; fi

setup
git push -q "$tmp/repo.git" main 2>/dev/null
got=$(push_tags v1.2.3 v2 v2.0.0-rc.1 it-v0.3.0 my-app-v1.0.0)
if [ -z "$got" ] && [ -z "$(tags_of "$tmp/github.git")" ]; then ok "a repo that ships nothing refuses version tags, and they are on neither server"
else not_ok "a repo that ships nothing refuses version tags, and they are on neither server" "repo: $got"; fi

setup
got=$(push_tags v1.0.0)
if [ -z "$got" ] && [ -z "$(tags_of "$tmp/github.git")" ]; then ok "a repo with no main refuses a version tag"
else not_ok "a repo with no main refuses a version tag" "repo: $got"; fi

# the refusal's words: only crate makes version tags, and the command for a first version, on crate's lines
setup
git push -q "$tmp/repo.git" main 2>/dev/null
git tag v4.0.0
out=$(git push "$tmp/repo.git" v4.0.0 2>&1)
reasons=$(grep 'crate: ' <<<"$out")
if grep -qi 'only crate makes' <<<"$reasons" && grep -Eq 'git( -C [^ ]+)? tag [^ ]+' <<<"$reasons"; then
  ok "the refusal says only crate makes version tags, and gives the command that sets a first version"
else not_ok "the refusal says only crate makes version tags, and gives the command that sets a first version" "$out"; fi

# a version tag pushed with main refuses the whole push: main has not moved
before=$(main_of "$tmp/repo.git")
echo more >extra && git add extra && git commit -qm "chore: extra"
git tag v3.0.0
out=$(git push "$tmp/repo.git" main v3.0.0 2>&1)
rc=$?
if [ "$rc" -ne 0 ] && [ "$(main_of "$tmp/repo.git")" = "$before" ] && [ -z "$(tags_of "$tmp/repo.git")" ]; then
  ok "a version tag pushed with main refuses the whole push"
else not_ok "a version tag pushed with main refuses the whole push" "rc=$rc
$out"; fi

# a tag of any other name is not touched
got=$(push_tags nightly v-1 version1 it-vnext rc-2)
if [ "$got" = "it-vnext nightly rc-2 v-1 version1 " ]; then ok "a tag that is no version is kept"
else not_ok "a tag that is no version is kept" "repo: $got"; fi

# crate's own tags, made in the bare repo by release, are not touched: the version follows a land, and goes to github
setup
printf 'crate = "1"\nimage = "%s"\nship = "npm"\n' "$alpine" >crate.toml
git add -A && git commit -qm "chore: ship"
git push -q "$tmp/repo.git" main 2>/dev/null
git -C "$tmp/repo.git" tag v0.1.0 main
echo x >>verdict.note && git add verdict.note && git commit -qm "feat: one"
out=$(git push "$tmp/repo.git" main 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && [ "$(tags_of "$tmp/repo.git")" = "v0.1.0 v0.1.1 " ] && [ "$(tags_of "$tmp/github.git")" = "v0.1.0 v0.1.1 " ]; then
  ok "crate's own version tags are made and copied as before"
else not_ok "crate's own version tags are made and copied as before" "rc=$rc
repo: $(tags_of "$tmp/repo.git") github: $(tags_of "$tmp/github.git")
$out"; fi

# the check is a workspace: it reaches its own service by name and its built app as app, and leaves nothing but its
# kept volume
busybox=docker.io/library/busybox:latest
setup
git push -q "$tmp/repo.git" main 2>/dev/null
cat >>crate.toml <<TOML
services = "pod.yaml"

[app]
build = "app.Containerfile"
health = "wget -qO- http://127.0.0.1:8080/"

[[app.ports]]
name = "web"
port = 8080
TOML
cat >pod.yaml <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: WORKSPACE-postgres
spec:
  containers:
    - name: postgres
      image: $busybox
      command: ["sh", "-c", "echo service-up >/tmp/index.html; exec httpd -f -p 5432 -h /tmp"]
YAML
cat >app.Containerfile <<DOCKER
FROM $busybox
CMD ["sh", "-c", "mkdir -p /tmp/www && echo built-app >/tmp/www/index.html && exec httpd -f -p 8080 -h /tmp/www"]
DOCKER
cat >scripts/check.sh <<'SH'
#!/bin/sh
echo "service says $(wget -qO- http://postgres:5432/)"
echo "app says $(wget -qO- http://app:8080/)"
SH
git add -A && git commit -qm "chore: a service and an app"
out=$(git push "$tmp/repo.git" main 2>&1)
if [ "$(main_of "$tmp/repo.git")" = "$(git rev-parse HEAD)" ] && grep -q "service says service-up" <<<"$out" &&
  grep -q "app says built-app" <<<"$out"; then
  ok "the check reaches its service by name and its built app as app"
else not_ok "the check reaches its service by name and its built app as app" "$out"; fi
left=$( { ${CRATE_ENGINE:-podman} ps -a --format '{{.Names}}'; ${CRATE_ENGINE:-podman} pod ls --format '{{.Name}}'
  ${CRATE_ENGINE:-podman} network ls --format '{{.Name}}'; } | grep '^crate-repo-check' | tr '\n' ' ')
if [ -z "$left" ] && ${CRATE_ENGINE:-podman} volume exists crate-repo-check-node-modules; then
  ok "the check leaves nothing but its kept volume"
else not_ok "the check leaves nothing but its kept volume" "left: $left"; fi

# the README says how a first version is set
if grep -Eq 'git( -C [^ ]+)? tag ' "$repo/README.md"; then ok "the README gives the command that sets a first version"
else not_ok "the README gives the command that sets a first version"; fi

echo "1..$n"
exit "$failed"
