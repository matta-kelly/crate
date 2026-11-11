# List the commands
default:
    @just --list

# shellcheck, then the hook tests on real git and podman
check:
    scripts/check.sh
