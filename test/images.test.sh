#!/usr/bin/env bash
# The tests of declared images ([[images]]), on real git, the real engine and a real registry (a local one, over plain
# HTTP): a check whose changes touch an image's inputs builds it, a green main publishes it under the build number of
# the last main that changed its inputs, and a change elsewhere publishes nothing.
set -uo pipefail
umask 0002 # a umask that makes files group-writable: the image must not take it
root=${CRATE_ROOT:-$(dirname "$(dirname "$(readlink -f "$0")")")} # the install to test: the built layout
alpine=docker.io/library/alpine@sha256:0f49f331d9212aff11b932863be4eb01cdf962969aa86dab7d1ee21ac6f5f52b
registry_image=docker.io/library/registry@sha256:46faa9a1ae6813194b53921a370f2f4f8c5e1aae228a89bceafef5847a6a3278
tmp=$(mktemp -d)
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
export XDG_CONFIG_HOME=$tmp/config # the push login is the suite's own
engine=${CRATE_ENGINE:-podman}
registry=crate-images-test-registry-$$
kept=localhost/crate-repo-image-thing
# shellcheck source=test/lib.sh
. "$(dirname "$(readlink -f "$0")")/lib.sh"
# shellcheck disable=SC2329 # the EXIT trap below runs it
cleanup() {
  "$engine" rm -f -t 0 "$registry" >/dev/null 2>&1
  "$engine" images --filter "reference=$kept" --format '{{.Repository}}:{{.Tag}}' | xargs -r "$engine" image rm >/dev/null 2>&1
  sweep
}
trap cleanup EXIT

"$engine" run -d --name "$registry" -p 127.0.0.1::5000 "$registry_image" >/dev/null
port=$("$engine" port "$registry" 5000/tcp | sed 's/.*://')
reg=localhost:$port
for _ in $(seq 1 50); do curl -fs "http://$reg/v2/" >/dev/null && break; sleep 0.2; done
mkdir -p "$XDG_CONFIG_HOME/crate" && echo '{"auths":{}}' >"$XDG_CONFIG_HOME/crate/push-auth.json"

digest() { curl -fsI -H 'Accept: application/vnd.oci.image.manifest.v1+json' \
  -H 'Accept: application/vnd.docker.distribution.manifest.v2+json' "http://$reg/v2/o/thing/manifests/$1" |
  tr -d '\r' | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest: //p'; }
has() { grep -qw "$1" <<<"$(tags)"; }
tags() { curl -fs "http://$reg/v2/o/thing/tags/list" | jq -r '.tags // [] | sort | join(" ")'; }
build_of() { git -C "$tmp/repo.git" rev-list --count --first-parent "$1"; }

# a repo on crate that declares one image, from thing/, and a client in it
mkdir -p "$tmp/gh" && git init -q --bare -b main "$tmp/gh/repo.git"
"$root/bin/crate" init "$tmp/repo.git" "$tmp/gh/repo.git" >/dev/null
git init -q -b main "$tmp/client" && cd "$tmp/client" || exit 1
mkdir -p scripts thing
cat >crate.toml <<TOML
crate = "3"
image = "$alpine"
name = "repo"

[[images]]
publish = "$reg/o/thing"
containerfile = "thing/Containerfile"
inputs = ["thing"]
point = "latest"
TOML
printf 'FROM %s\nCOPY thing/note /note\nRUN echo thing > /etc/hostname\n' "$alpine" >thing/Containerfile
echo one >thing/note
printf '#!/bin/sh\ntrue\n' >scripts/check.sh && chmod +x scripts/check.sh
echo a >other
git add -A && git commit -qm "chore: start" && seed

# a change to the inputs: the check builds the image, and the green main publishes it under its build number
echo two >thing/note && git add -A && git commit -qm "feat: two" && out=$(land)
settle
first=$(build_of "$(main_of "$tmp/repo.git")")
if grep -q "crate: landed" <<<"$out" && grep -q "building $reg/o/thing" <<<"$out" && has "$first" && [ "$(digest latest)" = "$(digest "$first")" ]; then
  ok "a change to an image's inputs builds it in the check, and the green main publishes it as its build number and latest"
else not_ok "a change to an image's inputs builds it in the check, and the green main publishes it as its build number and latest" \
  "tags: $(tags); want $first, and latest at it; $(cat "$tmp/repo.git/crate/images/run.log" 2>&1)"; fi

# the image is the commit's: its files have git's modes, not the umask's, and a RUN step's /etc/hostname is kept
"$engine" pull -q --tls-verify=false "$reg/o/thing:$first" >/dev/null
facts=$("$engine" run --rm --no-hostname "$reg/o/thing:$first" sh -c 'stat -c %a /note; cat /note /etc/hostname')
if [ "$facts" = $'644\ntwo\nthing' ]; then ok "the image is the commit's: modes 0644 under a umask of 0002, and its own /etc/hostname"
else not_ok "the image is the commit's: modes 0644 under a umask of 0002, and its own /etc/hostname" "$facts"; fi
"$engine" image rm -f "$reg/o/thing:$first" >/dev/null 2>&1

# a change elsewhere: no build, and nothing new published
before=$(tags)
echo b >other && git add -A && git commit -qm "fix: other" && out=$(land)
settle
if grep -q "crate: landed" <<<"$out" && ! grep -q "building $reg/o/thing" <<<"$out" && [ "$(tags)" = "$before" ]; then
  ok "a change outside the inputs builds nothing and publishes nothing"
else not_ok "a change outside the inputs builds nothing and publishes nothing" "tags: $(tags); $out"; fi

# a second change to the inputs: a new build number, latest follows it
echo three >thing/note && git add -A && git commit -qm "feat: three" && out=$(land)
settle
second=$(build_of "$(main_of "$tmp/repo.git")")
if [ "$second" -gt "$first" ] && has "$second" && [ "$(digest latest)" = "$(digest "$second")" ]; then
  ok "a later change to the inputs publishes a larger build number, and latest moves to it"
else not_ok "a later change to the inputs publishes a larger build number, and latest moves to it" "tags: $(tags); first $first, second $second"; fi

# a recipe that fails refuses the change, which does not land
printf 'RUN false\n' >>thing/Containerfile && git add -A && git commit -qm "feat: broken" && out=$(land)
if grep -q "crate: not landed" <<<"$out" && grep -q "the image $reg/o/thing did not build" <<<"$out"; then
  ok "an image that does not build refuses the change"
else not_ok "an image that does not build refuses the change" "$out"; fi

echo "1..$n"
exit "$failed"
