#!/usr/bin/env bash
# crate's check: shellcheck on every script, then the hook tests. It runs on the host: the tests drive git and the
# engine for real, which a container would have to nest.
set -euo pipefail
cd "$(dirname "$(readlink -f "$0")")/.."
shellcheck=docker.io/koalaman/shellcheck@sha256:61862eba1fcf09a484ebcc6feea46f1782532571a34ed51fedf90dd25f925a8d

echo "== shellcheck"
"${CRATE_ENGINE:-podman}" run --rm -v "$PWD:/src:ro" -w /src "$shellcheck" -x -P SCRIPTDIR/.. \
  bin/crate hooks/pre-receive hooks/post-receive lib/crate.sh scripts/check.sh scripts/release.sh test/hooks.test.sh test/apply.test.sh test/release.test.sh
echo "== tests"
test/hooks.test.sh
test/apply.test.sh
test/release.test.sh
echo "check passed"
