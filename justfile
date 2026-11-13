set shell := ["bash", "-euo", "pipefail", "-c"]

shellcheck := "docker.io/koalaman/shellcheck@sha256:61862eba1fcf09a484ebcc6feea46f1782532571a34ed51fedf90dd25f925a8d"

# List the commands
default:
    @just --list

# An install's layout in dist/crate: bin/crate and hooks/, as the release asset holds them
build:
    rm -rf dist/crate && mkdir -p dist/crate/bin
    CGO_ENABLED=0 go build -o dist/crate/bin/crate ./cmd/crate
    cp -r hooks dist/crate/hooks

# go vet and go test, shellcheck, then the gate's tests on real git and podman, against the built install
check: build
    go vet ./...
    go test ./...
    "${CRATE_ENGINE:-podman}" run --rm -v "$PWD:/src:ro" -w /src {{shellcheck}} hooks/pre-receive hooks/post-receive test/hooks.test.sh test/apply.test.sh test/release.test.sh test/postsubmit.test.sh
    CRATE_ROOT="$PWD/dist/crate" test/hooks.test.sh
    CRATE_ROOT="$PWD/dist/crate" test/apply.test.sh
    CRATE_ROOT="$PWD/dist/crate" test/release.test.sh
    CRATE_ROOT="$PWD/dist/crate" test/postsubmit.test.sh

# A new version: check, tag v<version>, move the major tag, put the asset on both releases. --dry-run only builds dist/crate-linux-amd64.tar.gz
release version *flags:
    go build -o dist/release ./cmd/release
    dist/release publish {{version}} {{flags}}

# The major tag and its asset back to v<version>
rollback version:
    go build -o dist/release ./cmd/release
    dist/release rollback {{version}}
