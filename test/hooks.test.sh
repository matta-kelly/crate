#!/usr/bin/env bash
# The tests of crate's merge queue through its hooks, on real git and the real engine: a throwaway bare repo with
# crate on it, a second standing in for GitHub, and a tiny repo whose check runs in alpine and passes or fails as its
# file `verdict` says.
set -uo pipefail
repo=$(dirname "$(dirname "$(readlink -f "$0")")")
root=${CRATE_ROOT:-$repo} # the install to test: the built layout
engine=${CRATE_ENGINE:-podman}
alpine=docker.io/library/alpine@sha256:0f49f331d9212aff11b932863be4eb01cdf962969aa86dab7d1ee21ac6f5f52b
tmp=$(mktemp -d)
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
# shellcheck source=test/lib.sh
. "$repo/test/lib.sh"
trap sweep EXIT

# A fresh bare repo with crate on it, seeded with the client's first commit, and the client.
setup() {
  pkill -f "crate (queue|postsubmit) $tmp" 2>/dev/null
  cd "$tmp" && rm -rf "${tmp:?}"/*
  git init -q --bare -b main "$tmp/github.git"
  "$root/bin/crate" init "$tmp/repo.git" "$tmp/github.git" >/dev/null
  git init -q -b main "$tmp/client"
  cd "$tmp/client" || exit 1
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
  seed
}

# steps <kind>: the steps of the repo's last run of kind on the record, each step:result.
steps() {
  jq -r -s --arg k "$1" '[.[] | select(.kind == $k)] | (last.run) as $r | map(select(.run == $r) | "\(.step):\(.result)") | join(" ")' \
    "$tmp/repo.git/crate/log.jsonl" 2>/dev/null
}

# a green land: main moves to it, its check ran against main as it was, and it is copied to github
setup
base=$(git rev-parse HEAD)
echo a >a && git add a && git commit -qm a
out=$(land)
if [ "$(main_of "$tmp/repo.git")" = "$(git rev-parse HEAD)" ] && grep -q "base=$base" <<<"$out" &&
  grep -q "^remote: crate: landed" <<<"$out" && [ "$(git -C "$tmp/repo.git" rev-parse "refs/landed/$(git rev-parse HEAD)")" = "$(git rev-parse HEAD)" ]; then
  ok "a green land moves main, its verdict comes back to the push, and refs/landed keeps it"
else not_ok "a green land moves main, and its verdict comes back to the push" "$out"; fi
if [ "$(main_of "$tmp/github.git")" = "$(git rev-parse HEAD)" ]; then ok "main is copied to github"
else not_ok "main is copied to github"; fi

# the record: the check is a run with each step timed, the change's land a run with its wait, and the copy a run
if [ "$(steps check)" = "image:pass hold:pass tools:pass check:pass total:pass" ] &&
  [ "$(steps land)" = "wait:pass total:pass" ] && [ "$(steps mirror)" = "total:pass" ] &&
  grep -q "crate: image [0-9]*s, hold .*, check [0-9]*s; total [0-9]*s, pass" <<<"$out"; then
  ok "the check, the land and the copy are runs on the record, and the push shows the check's summary"
else not_ok "the check, the land and the copy are runs on the record, and the push shows the check's summary" "check: $(steps check)
land: $(steps land)
mirror: $(steps mirror)
$out"; fi

# a red land is not landed: its output and why come back, main has not moved, and it leaves the queue
before=$(main_of "$tmp/repo.git")
echo fail >verdict && git commit -qam red
out=$(land)
if [ "$(main_of "$tmp/repo.git")" = "$before" ] && grep -q "check failed at: verdict" <<<"$out" &&
  grep -q "^remote: crate: not landed: " <<<"$out" && [ -z "$(git -C "$tmp/repo.git" for-each-ref refs/queue/)" ]; then
  ok "a red land is not landed, with its output, and main has not moved"
else not_ok "a red land is not landed, with its output, and main has not moved" "$out"; fi
git reset -q --hard "$before"

# a push to main is refused: only the queue moves it
echo b >b && git add b && git commit -qm b
out=$(git push "$tmp/repo.git" HEAD:main 2>&1)
if [ "$(main_of "$tmp/repo.git")" = "$before" ] && grep -q "main moves only through the merge queue" <<<"$out"; then
  ok "a push to main is refused"
else not_ok "a push to main is refused" "$out"; fi

# a queue ref that is not its commit is refused, as is a change main holds already
out=$(git push "$tmp/repo.git" "HEAD:refs/queue/$before" 2>&1)
out2=$(git push "$tmp/repo.git" "$before:refs/queue/$before" 2>&1)
if grep -q "is not the commit it names" <<<"$out" && grep -q "is on main already" <<<"$out2"; then
  ok "a queue ref must be the commit it names, and not one main holds"
else not_ok "a queue ref must be the commit it names, and not one main holds" "$out
$out2"; fi

# only crate writes refs/landed: a pushed one is refused
out=$(git push "$tmp/repo.git" "HEAD:refs/landed/$(git rev-parse HEAD)" 2>&1)
if grep -q "only crate writes refs/landed" <<<"$out" &&
  ! git -C "$tmp/repo.git" rev-parse -q --verify "refs/landed/$(git rev-parse HEAD)" >/dev/null; then
  ok "a pushed refs/landed is refused"
else not_ok "a pushed refs/landed is refused" "$out"; fi

# a branch is free to push, and is not checked; deleting it is refused
out=$(git push "$tmp/repo.git" HEAD:refs/heads/wip 2>&1)
del=$(git push "$tmp/repo.git" :refs/heads/wip 2>&1)
if git -C "$tmp/repo.git" rev-parse -q --verify refs/heads/wip >/dev/null && ! grep -q "crate: run" <<<"$out" &&
  grep -q "is kept" <<<"$del"; then
  ok "a branch push runs no check, and is not deleted"
else not_ok "a branch push runs no check, and is not deleted" "$out
$del"; fi

# two lands at once from two clients on the same main: both land, checked side by side, and neither rebases
setup
git clone -q "$tmp/repo.git" "$tmp/second"
git -C "$tmp/second" config user.email t@t && git -C "$tmp/second" config user.name t
touch slow && git add slow && git commit -qm first
(cd "$tmp/second" && touch slow other && git add slow other && git commit -qm second)
land >"$tmp/a.out" &
a=$!
sleep 1
(cd "$tmp/second" && land) >"$tmp/b.out" &
b=$!
wait $a
wait $b
landed=$(git -C "$tmp/repo.git" log --format=%s main | head -2 | tr '\n' ' ')
overlap=$(jq -r -s '[.[] | select(.kind == "check" and .step == "total")] | .[-2:] |
  if ((.[1].at | fromdateiso8601) - .[1].seconds < (.[0].at | fromdateiso8601)) then "yes" else "no" end' \
  "$tmp/repo.git/crate/log.jsonl")
if [ "$landed" = "second first " ] && grep -q "crate: landed" "$tmp/a.out" && grep -q "crate: landed" "$tmp/b.out" &&
  [ "$overlap" = yes ]; then
  ok "two lands at once both land, checked side by side, with no rebase"
else not_ok "two lands at once both land, checked side by side, with no rebase" "main: $landed overlap: $overlap
$(cat "$tmp/a.out" "$tmp/b.out")"; fi

# -o crate.wait=no: the push returns once the change is in the queue, and the change lands after
setup
echo c >c && git add c && git commit -qm c
sha=$(git rev-parse HEAD)
start=$(date +%s)
out=$(queue)
took=$(($(date +%s) - start))
settle
if grep -q "is in the queue" <<<"$out" && ! grep -q "crate: landed" <<<"$out" && [ "$took" -lt 3 ] &&
  [ "$(main_of "$tmp/repo.git")" = "$sha" ]; then
  ok "crate.wait=no returns once queued, and the change lands after"
else not_ok "crate.wait=no returns once queued, and the change lands after" "took=${took}s main=$(main_of "$tmp/repo.git")
$out"; fi

# a check past its limit is stopped and the change not landed; the next land is not held by it
setup
printf 'crate = "1"\nimage = "%s"\ntimeout = "3"\n' "$alpine" >crate.toml
touch slow && sed -i 's/sleep 3/sleep 30/' scripts/check.sh && git add -A && git commit -qm hang
before=$(main_of "$tmp/repo.git")
start=$(date +%s)
out=$(land)
took=$(($(date +%s) - start))
if [ "$(main_of "$tmp/repo.git")" = "$before" ] && grep -q "ran past its limit of 3s" <<<"$out" &&
  grep -q "crate: not landed" <<<"$out" && [ "$took" -lt 25 ]; then
  ok "a check past its limit is stopped, and the change not landed"
else not_ok "a check past its limit is stopped, and the change not landed" "took=${took}s
$out"; fi
git reset -q --hard "$before"
echo d >d && git add d && git commit -qm d
if grep -q "crate: landed" <<<"$(land)"; then ok "the next land is not held by the stopped check"
else not_ok "the next land is not held by the stopped check"; fi

# image "dev": the check runs in crate's own dev image of the change, put on this machine by crate dev-image local:
# here its copy, tagged by hand, so the test builds nothing
setup
printf 'crate = "2"\nname = "repo"\nimage = "dev"\n\n[dev]\nimage = "localhost/crate-test-dev"\ninputs = ["Containerfile"]\ncontainerfile = "Containerfile"\n' >crate.toml
echo "FROM $alpine" >Containerfile
git add -A && git commit -qm dev
dev="localhost/crate-test-dev:$("$root/bin/crate" dev-image hash)"
"$engine" tag "$alpine" "$dev"
out=$(land)
"$engine" rmi -f "$dev" >/dev/null 2>&1
if [ "$(main_of "$tmp/repo.git")" = "$(git rev-parse HEAD)" ] && grep -q "image [0-9]*s (stored)" <<<"$out"; then
  ok "image dev: the check runs in crate's dev image of the change"
else not_ok "image dev: the check runs in crate's dev image of the change" "$out"; fi

# image "dev" in a repo that does not declare itself: no name, or no [dev] inputs. crate guesses neither, so the
# change is not landed, with the key it lacks
for row in 'no name|crate = "2"\nimage = "dev"\n\n[dev]\ninputs = ["Containerfile"]\ncontainerfile = "Containerfile"\n|needs name' \
  'no [dev] inputs|crate = "2"\nname = "repo"\nimage = "dev"\n|needs [dev] inputs'; do
  IFS='|' read -r what toml says <<<"$row"
  setup
  before=$(main_of "$tmp/repo.git")
  printf '%b' "$toml" >crate.toml
  echo "FROM $alpine" >Containerfile
  git add -A && git commit -qm "dev, $what"
  out=$(land)
  if [ "$(main_of "$tmp/repo.git")" = "$before" ] && grep -qF "$says" <<<"$out" && grep -q "crate: not landed" <<<"$out"; then
    ok "image dev with $what: the change is not landed, and says it $says"
  else not_ok "image dev with $what: the change is not landed, and says it $says" "$out"; fi
done

# a run's checkout that git cannot use (a lock a killed run left) is made again, and the land goes through
setup
echo e >e && git add e && git commit -qm e
land >/dev/null
for wt in "$tmp"/repo.git/worktrees/*; do touch "$wt/index.lock"; done
echo f >f && git add f && git commit -qm f
out=$(land)
if [ "$(main_of "$tmp/repo.git")" = "$(git rev-parse HEAD)" ] && grep -q "crate: landed" <<<"$out"; then
  ok "a broken run checkout is made again, and the land goes through"
else not_ok "a broken run checkout is made again, and the land goes through" "$out
$(cat "$tmp/repo.git/crate/worker.log")"; fi

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
# nothing. Each row is a tag shape release counts from: full, short, prerelease, a folder's.
setup
printf 'crate = "1"\nimage = "%s"\nship = "npm"\npackages = ". it"\n' "$alpine" >crate.toml
mkdir it && touch it/a
git add -A && git commit -qm "chore: ship"
land >/dev/null
got=$(push_tags v1.2.3 v2 v2.0.0-rc.1 it-v0.3.0)
if [ -z "$got" ] && [ -z "$(tags_of "$tmp/github.git")" ]; then ok "a repo that ships refuses version tags, and they are on neither server"
else not_ok "a repo that ships refuses version tags, and they are on neither server" "repo: $got"; fi

setup
got=$(push_tags v1.2.3 v2 v2.0.0-rc.1 it-v0.3.0 my-app-v1.0.0)
if [ -z "$got" ] && [ -z "$(tags_of "$tmp/github.git")" ]; then ok "a repo that ships nothing refuses version tags, and they are on neither server"
else not_ok "a repo that ships nothing refuses version tags, and they are on neither server" "repo: $got"; fi

# the refusal's words: only crate makes version tags, and the command for a first version, on crate's lines
setup
git tag v4.0.0
out=$(git push "$tmp/repo.git" v4.0.0 2>&1)
reasons=$(grep 'crate: ' <<<"$out")
if grep -qi 'only crate makes' <<<"$reasons" && grep -Eq 'git( -C [^ ]+)? tag [^ ]+' <<<"$reasons"; then
  ok "the refusal says only crate makes version tags, and gives the command that sets a first version"
else not_ok "the refusal says only crate makes version tags, and gives the command that sets a first version" "$out"; fi

# a version tag pushed with a change refuses the whole push: nothing enters the queue
echo more >extra && git add extra && git commit -qm "chore: extra"
git tag v3.0.0
out=$(git push "$tmp/repo.git" "HEAD:refs/queue/$(git rev-parse HEAD)" v3.0.0 2>&1)
rc=$?
if [ "$rc" -ne 0 ] && [ -z "$(git -C "$tmp/repo.git" for-each-ref refs/queue/)" ] && [ -z "$(tags_of "$tmp/repo.git")" ]; then
  ok "a version tag pushed with a change refuses the whole push"
else not_ok "a version tag pushed with a change refuses the whole push" "rc=$rc
$out"; fi
git tag -d v3.0.0 v4.0.0 >/dev/null

# a tag of any other name is not touched
got=$(push_tags nightly v-1 version1 it-vnext rc-2)
if [ "$got" = "it-vnext nightly rc-2 v-1 version1 " ]; then ok "a tag that is no version is kept"
else not_ok "a tag that is no version is kept" "repo: $got"; fi

# crate's own tags, made in the bare repo by release, are not touched: the version follows a land, and goes to github
setup
printf 'crate = "1"\nimage = "%s"\nship = "npm"\n' "$alpine" >crate.toml
git add -A && git commit -qm "chore: ship"
land >/dev/null
git -C "$tmp/repo.git" tag v0.1.0 main
echo x >>verdict.note && git add verdict.note && git commit -qm "feat: one"
out=$(land)
if grep -q "crate: landed" <<<"$out" && [ "$(tags_of "$tmp/repo.git")" = "v0.1.0 v0.1.1 " ] &&
  [ "$(tags_of "$tmp/github.git")" = "v0.1.0 v0.1.1 " ]; then
  ok "crate's own version tags are made and copied as before"
else not_ok "crate's own version tags are made and copied as before" "repo: $(tags_of "$tmp/repo.git") github: $(tags_of "$tmp/github.git")
$out"; fi

# the check is a workspace: it reaches its own service by name and its built app as app, and leaves nothing but its
# kept volume
busybox=docker.io/library/busybox:latest
setup
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
out=$(land)
if [ "$(main_of "$tmp/repo.git")" = "$(git rev-parse HEAD)" ] && grep -q "service says service-up" <<<"$out" &&
  grep -q "app says built-app" <<<"$out"; then
  ok "the check reaches its service by name and its built app as app"
else not_ok "the check reaches its service by name and its built app as app" "$out"; fi
left=$( { "$engine" ps -a --format '{{.Names}}'; "$engine" pod ls --format '{{.Name}}'
  "$engine" network ls --format '{{.Name}}'; } | grep '^crate-repo-check' | tr '\n' ' ')
if [ -z "$left" ] && "$engine" volume exists crate-repo-check-0-node-modules; then
  ok "the check leaves nothing but its kept volume"
else not_ok "the check leaves nothing but its kept volume" "left: $left"; fi

# the README says how a first version is set
if grep -Eq 'git( -C [^ ]+)? tag ' "$repo/README.md"; then ok "the README gives the command that sets a first version"
else not_ok "the README gives the command that sets a first version"; fi

echo "1..$n"
exit "$failed"
