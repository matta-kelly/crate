#!/usr/bin/env bash
# The tests of the dev image cache, on real git, the real engine and a real registry (a local one, over plain HTTP): a
# bare repo with crate on it whose check runs in its own dev image, a second standing in for GitHub, and a client that
# lands changes. Each dev image a check makes is pushed by its hash, whether its change lands or not, and each green
# main points :main at its own; this suite reads the registry back as a laptop's pull does.
set -uo pipefail
root=${CRATE_ROOT:-$(dirname "$(dirname "$(readlink -f "$0")")")} # the install to test: the built layout
alpine=docker.io/library/alpine@sha256:0f49f331d9212aff11b932863be4eb01cdf962969aa86dab7d1ee21ac6f5f52b
registry_image=docker.io/library/registry@sha256:46faa9a1ae6813194b53921a370f2f4f8c5e1aae228a89bceafef5847a6a3278
tmp=$(mktemp -d)
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
export XDG_CONFIG_HOME=$tmp/config # the push login is the suite's own
engine=${CRATE_ENGINE:-podman}
registry=crate-cache-test-registry-$$
# shellcheck source=test/lib.sh
. "$(dirname "$(readlink -f "$0")")/lib.sh"
# shellcheck disable=SC2329 # the EXIT trap below runs it
cleanup() {
  "$engine" rm -f -t 0 "$registry" >/dev/null 2>&1
  "$engine" images --filter "reference=$dev" --format '{{.Repository}}:{{.Tag}}' | xargs -r "$engine" image rm >/dev/null 2>&1
  sweep
}
trap cleanup EXIT

"$engine" run -d --name "$registry" -p 127.0.0.1::5000 "$registry_image" >/dev/null
port=$("$engine" port "$registry" 5000/tcp | sed 's/.*://')
reg=localhost:$port
dev=$reg/o/repo-dev
for _ in $(seq 1 50); do curl -fs "http://$reg/v2/" >/dev/null && break; sleep 0.2; done

login() { mkdir -p "$XDG_CONFIG_HOME/crate" && echo '{"auths":{}}' >"$XDG_CONFIG_HOME/crate/push-auth.json"; }
no_login() { rm -f "$XDG_CONFIG_HOME/crate/push-auth.json"; }

# setup: a fresh repo on crate whose check runs in its own dev image, and a client in it. Its first main is seeded, so
# it has no check, and no image.
setup() {
  cd "$tmp" && find "$tmp" -mindepth 1 -maxdepth 1 ! -name config -exec rm -rf {} +
  "$engine" images --filter "reference=$dev" --format '{{.Repository}}:{{.Tag}}' | xargs -r "$engine" image rm >/dev/null 2>&1
  mkdir -p "$tmp/gh"
  git init -q --bare -b main "$tmp/gh/repo.git"
  "$root/bin/crate" init "$tmp/repo.git" "$tmp/gh/repo.git" >/dev/null
  git init -q -b main "$tmp/client"
  cd "$tmp/client" || exit 1
  mkdir -p scripts .crate
  cat >crate.toml <<TOML
crate = "4"
name = "repo"
image = "dev"

[dev]
image = "$dev"
inputs = [".crate/Containerfile"]
TOML
  printf 'FROM %s\nRUN echo first >/version\n' "$alpine" >.crate/Containerfile
  cat >scripts/check.sh <<'SH'
#!/bin/sh
[ ! -e refuse ] || { echo refused; exit 1; }
echo "the image says $(cat /version)"
SH
  chmod +x scripts/check.sh
  git add -A && git commit -qm "chore: start"
  seed
}

# image: a commit that changes the dev image's inputs.
image() { printf 'FROM %s\nRUN echo %s >/version\n' "$alpine" "$1" >.crate/Containerfile && git add -A && git commit -qm "fix: image $1"; }
hash_of() { "$root/bin/crate" -C "$tmp/client" dev-image hash --rev "$1"; }
digest_of() { curl -fsI -H 'Accept: application/vnd.oci.image.manifest.v1+json' \
  -H 'Accept: application/vnd.docker.distribution.manifest.v2+json' "http://$reg/v2/o/repo-dev/manifests/$1" |
  tr -d '\r' | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest: //p'; }
cache_log() { tail -5 "$tmp/repo.git/crate/cache/run.log" 2>/dev/null; }

# a check's dev image goes to the registry by its hash, and the green main's is :main
login
setup
image second
out=$(land)
settle
main=$(main_of "$tmp/repo.git")
hash=$(hash_of "$main")
if grep -q "crate: landed" <<<"$out" && [ -n "$(digest_of "$hash")" ]; then ok "the dev image the check made is in the registry by its hash"
else not_ok "the dev image the check made is in the registry by its hash" "$out
$(cache_log)"; fi
if [ -n "$(digest_of main)" ] && [ "$(digest_of main)" = "$(digest_of "$hash")" ]; then ok ":main is the green main's dev image"
else not_ok ":main is the green main's dev image" "main: $(digest_of main) hash: $(digest_of "$hash")
$(cache_log)"; fi
if jq -e 'select(.kind == "cache" and .step == "push" and .result == "pass")' "$tmp/repo.git/crate/log.jsonl" >/dev/null &&
  jq -e 'select(.kind == "cache" and .step == "point" and .result == "pass")' "$tmp/repo.git/crate/log.jsonl" >/dev/null; then
  ok "the record holds the push and the move of :main"
else not_ok "the record holds the push and the move of :main" "$(grep '"cache"' "$tmp/repo.git/crate/log.jsonl")"; fi

# a laptop pulls by the hash, and builds nothing
"$engine" image rm "$dev:$hash" >/dev/null 2>&1
pulled=$(cd "$tmp/client" && "$root/bin/crate" dev-image local 2>&1)
if grep -q "^$dev:$hash$" <<<"$pulled" && ! grep -q "is built" <<<"$pulled"; then ok "a checkout of the green main pulls its dev image by the hash"
else not_ok "a checkout of the green main pulls its dev image by the hash" "$pulled"; fi

# a change the check refuses still pushes the image it made, and :main stays the green main's
before=$(digest_of main)
image third
touch refuse && git add refuse && git commit -qm "fix: refused"
out=$(land)
settle
refused=$(hash_of HEAD)
if grep -q "crate: not landed" <<<"$out" && [ -n "$(digest_of "$refused")" ] && [ "$(digest_of main)" = "$before" ]; then
  ok "a refused change's dev image is pushed by its hash, and :main does not move"
else not_ok "a refused change's dev image is pushed by its hash, and :main does not move" "$out
$(cache_log)"; fi
git reset -q --hard "$main"

# with no push login, main still lands and the failure is recorded; crate apply pushes once the login is back
no_login
image fourth
out=$(land)
settle
main=$(main_of "$tmp/repo.git")
hash=$(hash_of "$main")
if grep -q "crate: landed" <<<"$out" && [ -z "$(digest_of "$hash")" ] &&
  jq -e 'select(.kind == "cache" and .step == "push" and .result == "fail")' "$tmp/repo.git/crate/log.jsonl" >/dev/null; then
  ok "with no push login, main lands, and the failed push is recorded"
else not_ok "with no push login, main lands, and the failed push is recorded" "$out
$(cache_log)"; fi
login
printf 'GIT_HOME=%s\nGITHUB_BASE=%s\nREPOS=repo\n' "$tmp" "$tmp/gh" | "$root/bin/crate" apply - >/dev/null 2>&1
settle
if [ -n "$(digest_of "$hash")" ] && [ "$(digest_of main)" = "$(digest_of "$hash")" ]; then
  ok "crate apply pushes what waits, and moves :main"
else not_ok "crate apply pushes what waits, and moves :main" "$(cache_log)"; fi

exit "$failed"
