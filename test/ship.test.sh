#!/usr/bin/env bash
# The tests of an image's ship, on real git, the real engine and a real registry (a local one, over plain HTTP): a
# bare repo with crate on it whose crate.toml says ship = "image", a second standing in for GitHub, and a client that
# lands changes. Each green main publishes the image its check ran, and its run files beside it; this suite reads them
# back from the registry as a node does.
set -uo pipefail
root=${CRATE_ROOT:-$(dirname "$(dirname "$(readlink -f "$0")")")} # the install to test: the built layout
alpine=docker.io/library/alpine@sha256:0f49f331d9212aff11b932863be4eb01cdf962969aa86dab7d1ee21ac6f5f52b
busybox=docker.io/library/busybox:latest
registry_image=docker.io/library/registry@sha256:46faa9a1ae6813194b53921a370f2f4f8c5e1aae228a89bceafef5847a6a3278
tmp=$(mktemp -d)
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
export XDG_CONFIG_HOME=$tmp/config # the push login is the suite's own
engine=${CRATE_ENGINE:-podman}
registry=crate-ship-test-registry-$$
kept=localhost/crate-repo-ship
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

login() { mkdir -p "$XDG_CONFIG_HOME/crate" && echo '{"auths":{}}' >"$XDG_CONFIG_HOME/crate/push-auth.json"; }
no_login() { rm -f "$XDG_CONFIG_HOME/crate/push-auth.json"; }

# setup [<more crate.toml>]: a fresh repo on crate whose app ships, and a client in it. Its first main is seeded, so it
# has no check, and no image.
setup() {
  cd "$tmp" && find "$tmp" -mindepth 1 -maxdepth 1 ! -name config -exec rm -rf {} +
  "$engine" images --filter "reference=$kept" --format '{{.Repository}}:{{.Tag}}' | xargs -r "$engine" image rm >/dev/null 2>&1
  mkdir -p "$tmp/gh"
  git init -q --bare -b main "$tmp/gh/repo.git"
  "$root/bin/crate" init "$tmp/repo.git" "$tmp/gh/repo.git" >/dev/null
  git init -q -b main "$tmp/client"
  cd "$tmp/client" || exit 1
  mkdir -p scripts deploy/quadlets
  cat >crate.toml <<TOML
crate = "3"
image = "$alpine"
name = "shipped"
ship = "image"
${1:-}

[app]
build = "app.Containerfile"
health = "wget -qO- http://127.0.0.1:8080/"
publish = "$reg/o/app"
quadlets = "deploy/quadlets"

[[app.ports]]
name = "web"
port = 8080
TOML
  cat >app.Containerfile <<DOCKER
FROM $busybox
COPY note /note
CMD ["sh", "-c", "mkdir -p /tmp/www && cp /note /tmp/www/index.html && exec httpd -f -p 8080 -h /tmp/www"]
DOCKER
  printf '[Container]\nImage=%s/o/app:VERSION\nExec=serve\n' "$reg" >deploy/quadlets/app.container
  printf '[Container]\nImage=%s/o/app:VERSION\nExec=migrate\n' "$reg" >deploy/quadlets/app-migrate.container
  cat >scripts/check.sh <<'SH'
#!/bin/sh
echo "app says $(wget -qO- http://app:8080/)"
echo "login seen: $(find / -name push-auth.json 2>/dev/null | head -1)"
SH
  chmod +x scripts/check.sh
  echo first >note
  git add -A && git commit -qm "chore: start"
  seed
}

# change: commit a change to the app, and land it.
change() { echo "$RANDOM" >>note && git add note && git commit -qm "fix: $RANDOM" && land; }

tag_of() { echo "sha-${1:0:7}"; }
published() { cat "$tmp/repo.git/crate/ship/published" 2>/dev/null; }
kept_tags() { "$engine" images --filter "reference=$kept" --format '{{.Tag}}' | sort | tr '\n' ' '; }
in_registry() { curl -fs -o /dev/null -H 'Accept: application/vnd.oci.image.manifest.v1+json' \
  -H 'Accept: application/vnd.docker.distribution.manifest.v2+json' "http://$reg/v2/$1/manifests/$2"; }

# The node's side, as monkeydo's fetchArtifact reads it: the manifest by the pinned digest, proved against it; each
# layer by its digest, saved by its title; a .tar.gz title unpacked.
fetch() { # fetch <repository path> <digest> <into>
  local manifest digest title
  manifest=$(curl -fs -H 'Accept: application/vnd.oci.image.manifest.v1+json' "http://$reg/v2/$1/manifests/$2") || return 1
  [ "sha256:$(printf '%s' "$manifest" | sha256sum | cut -d' ' -f1)" = "$2" ] || { echo "the manifest is not its digest"; return 1; }
  digest=$(jq -r '.layers[0].digest' <<<"$manifest")
  title=$(jq -r '.layers[0].annotations["org.opencontainers.image.title"]' <<<"$manifest")
  [ "$title" = quadlets.tar.gz ] || { echo "the layer's title is $title"; return 1; }
  mkdir -p "$3" && curl -fsL "http://$reg/v2/$1/blobs/$digest" -o "$3/$title" || return 1
  [ "sha256:$(sha256sum "$3/$title" | cut -d' ' -f1)" = "$digest" ] || { echo "the layer is not its digest"; return 1; }
  tar -C "$3" -xzf "$3/$title"
}

# a green main publishes its image and its run files, and the pin line names them
login
setup
out=$(change)
settle
main=$(main_of "$tmp/repo.git")
tag=$(tag_of "$main")
pin=$(grep '^shipped ' "$tmp/repo.git/crate/ship/run.log" | tail -1)
digest=${pin##* }
if grep -q "crate: landed" <<<"$out" && grep -q "publishes in the background" <<<"$out"; then
  ok "the land says its image publishes in the background, and does not wait for it"
else not_ok "the land says its image publishes in the background, and does not wait for it" "$out"; fi
if in_registry o/app "$tag"; then ok "the image is in the registry as sha-<short>"
else not_ok "the image is in the registry as sha-<short>" "$(cat "$tmp/repo.git/crate/ship/run.log")"; fi
if ! in_registry o/app latest; then ok "no latest tag"; else not_ok "no latest tag"; fi
if [ "$pin" = "shipped $tag $reg/o/app-quadlets $digest" ] && [[ "$digest" == sha256:* ]]; then ok "the pin line names the run files by their digest"
else not_ok "the pin line names the run files by their digest" "pin: $pin"; fi
if jq -e --arg pin "$pin" 'select(.kind == "release" and .step == "total" and .result == "pass" and .note == $pin)' \
  "$tmp/repo.git/crate/log.jsonl" >/dev/null; then ok "the record holds the pin line"
else not_ok "the record holds the pin line" "$(grep release "$tmp/repo.git/crate/log.jsonl")"; fi
if got=$(fetch o/app-quadlets "$digest" "$tmp/node" 2>&1) &&
  [ "$(cat "$tmp/node/app.container")" = "$(printf '[Container]\nImage=%s/o/app:%s\nExec=serve' "$reg" "$tag")" ] &&
  grep -q "Image=$reg/o/app:$tag" "$tmp/node/app-migrate.container"; then
  ok "a node fetches the run files by the pin, each Image= stamped with the tag"
else not_ok "a node fetches the run files by the pin, each Image= stamped with the tag" "$got"; fi
"$engine" pull -q --tls-verify=false "$reg/o/app:$tag" >/dev/null 2>&1
note=$("$engine" run --rm "$reg/o/app:$tag" cat /note 2>&1)
if [ "$note" = "$(cat note)" ]; then ok "the published image is the commit's app"
else not_ok "the published image is the commit's app" "$note"; fi
"$engine" image rm "$reg/o/app:$tag" >/dev/null 2>&1
if [ "$(published)" = "$main" ] && [ -z "$(kept_tags)" ]; then ok "the publish is noted, and the kept image is gone"
else not_ok "the publish is noted, and the kept image is gone" "published: $(published) kept: $(kept_tags)"; fi
if grep -q "login seen:" <<<"$out" && ! grep "login seen:" <<<"$out" | grep -q push-auth; then ok "the check sees no push login"
else not_ok "the check sees no push login" "$(grep 'login seen' <<<"$out")"; fi

# a failed publish leaves main green and its image kept; a newer green main takes its place; crate apply tries again
no_login
change >/dev/null
first=$(main_of "$tmp/repo.git")
settle
out=$(change)
settle
second=$(main_of "$tmp/repo.git")
if grep -q "crate: landed" <<<"$out" && [ "$(published)" = "$main" ] &&
  grep -q "no push login at $XDG_CONFIG_HOME/crate/push-auth.json" "$tmp/repo.git/crate/ship/run.log" &&
  jq -e 'select(.kind == "release" and .step == "push" and .result == "fail")' "$tmp/repo.git/crate/log.jsonl" >/dev/null; then
  ok "with no push login, main still lands, and the failed publish is recorded"
else not_ok "with no push login, main still lands, and the failed publish is recorded" "$out
$(tail -5 "$tmp/repo.git/crate/ship/run.log")"; fi
if [ "$(kept_tags)" = "$second " ]; then ok "the newer green main's image is kept, and the one it superseded is gone"
else not_ok "the newer green main's image is kept, and the one it superseded is gone" "kept: $(kept_tags) want: $second"; fi
login
printf 'GIT_HOME=%s\nGITHUB_BASE=%s\nREPOS=repo\n' "$tmp" "$tmp/gh" | "$root/bin/crate" apply - >/dev/null 2>&1
settle
if [ "$(published)" = "$second" ] && in_registry o/app "$(tag_of "$second")" && ! in_registry o/app "$(tag_of "$first")" &&
  [ -z "$(kept_tags)" ]; then
  ok "crate apply publishes the green main, not the one it superseded"
else not_ok "crate apply publishes the green main, not the one it superseded" "published: $(published) kept: $(kept_tags)
$(tail -5 "$tmp/repo.git/crate/ship/run.log")"; fi

# with postsubmit, the checked image waits until main is green, then publishes
setup 'postsubmit = "slow"'
out=$(change)
settle
main=$(main_of "$tmp/repo.git")
if [ "$(cat "$tmp/repo.git/crate/postsubmit/green" 2>/dev/null)" = "$main" ] && [ "$(published)" = "$main" ] &&
  in_registry o/app "$(tag_of "$main")"; then
  ok "with postsubmit, the image publishes once main is green"
else not_ok "with postsubmit, the image publishes once main is green" "main: $main green: $(cat "$tmp/repo.git/crate/postsubmit/green" 2>/dev/null) published: $(published) kept: $(kept_tags)
running: $(pgrep -fa "crate" | grep "$tmp" | head -5)
postsubmit: $(tail -5 "$tmp/repo.git/crate/postsubmit/run.log" 2>/dev/null; tail -5 "$tmp/repo.git/crate/postsubmit/${main:0:12}.log" 2>/dev/null)
ship: $(tail -5 "$tmp/repo.git/crate/ship/run.log" 2>/dev/null)
worker: $(tail -5 "$tmp/repo.git/crate/worker.log" 2>/dev/null)"; fi

# a repo that does not ship its image keeps nothing
setup
sed -i -e '/^ship = /d' -e '/^publish = /d' -e '/^quadlets = /d' crate.toml
git commit -qam "chore: no ship"
change >/dev/null
settle
if [ -z "$(kept_tags)" ] && [ ! -e "$tmp/repo.git/crate/ship/published" ]; then ok "a repo that does not ship its image keeps none"
else not_ok "a repo that does not ship its image keeps none" "kept: $(kept_tags)"; fi

echo "1..$n"
exit "$failed"
