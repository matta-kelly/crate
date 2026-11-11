set shell := ["bash", "-euo", "pipefail", "-c"]

shellcheck := "docker.io/koalaman/shellcheck@sha256:61862eba1fcf09a484ebcc6feea46f1782532571a34ed51fedf90dd25f925a8d"

# List the commands
default:
    @just --list

# go vet and go test, shellcheck on the shell that is left, then the hook tests on real git and podman
check:
    go vet ./...
    go test ./...
    "${CRATE_ENGINE:-podman}" run --rm -v "$PWD:/src:ro" -w /src {{shellcheck}} -x -P SCRIPTDIR/.. bin/crate hooks/pre-receive hooks/post-receive lib/crate.sh test/hooks.test.sh test/apply.test.sh test/release.test.sh test/postsubmit.test.sh
    test/hooks.test.sh
    test/apply.test.sh
    test/release.test.sh
    test/postsubmit.test.sh

# A new version: check, tag v<version>, move the major tag, put the asset on both releases. --dry-run only builds dist/crate-linux-amd64.tar.gz
release version *flags:
    go build -o dist/release ./cmd/release
    dist/release publish {{version}} {{flags}}

# The major tag and its asset back to v<version>
rollback version:
    go build -o dist/release ./cmd/release
    dist/release rollback {{version}}
