# List the commands
default:
    @just --list

# shellcheck, then the hook tests on real git and podman
check:
    scripts/check.sh

# A new version: check, tag v<version>, move the major tag to it, push both
release version:
    scripts/release.sh {{version}}
