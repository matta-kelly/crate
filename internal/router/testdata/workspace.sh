#!/usr/bin/env bash
# Copied from kinetic templates/repo/scripts/workspace.sh at kinetic d33a005, so this test reads no other repo.
# A workspace's services and its route, on the host. A workspace is one checkout's running copy of the repo's
# environment ([[workspace]]): its own podman network, the pod its repo's .devcontainer/pod.yaml defines, and a
# route on the router to the app port its devcontainer.json forwards. The dev container joins the network itself
# (runArgs), and `just down` removes it before `down` runs here.
#
#   workspace.sh up [--attempt] <root>   make the network, the pod and the route, each only if it is absent
#   workspace.sh down <root>             remove the route, the pod and the network
#   workspace.sh name <root>             print the workspace's name: <repo>-<folder>
#   workspace.sh url <root>              print its URL on the router
#
# The name is the repo (devcontainer.json's "name") and the root's folder, and must be a DNS label: it is a
# network, a pod, a volume and a host name. An attempt's network is --internal: its services are reachable, the
# outside is not, and it gets no route, so it cannot reach another workspace through the router. Every network is
# --opt isolate=strict, since podman 5.4 isolates none by default. The pod's database is reached by the alias
# `postgres`, the same in every workspace. In pod.yaml, WORKSPACE stands for the name.
#
# The router: one Caddy container for the machine, started on demand and never removed here. It joins each
# workspace's network and proxies <name>.localhost to <name>-app, the dev container's alias, since a container
# cannot reach a port published on the host's loopback. Its state is a Caddyfile and one route file per workspace.
# It listens on 127.0.0.1:80, which a rootless podman can bind once net.ipv4.ip_unprivileged_port_start is 80.
set -euo pipefail

router=${WORKSPACE_ROUTER:-workspace-router}
router_port=${WORKSPACE_ROUTER_PORT:-80}
router_state=${WORKSPACE_ROUTER_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/workspace-router}
router_image=docker.io/library/caddy:2.10

die() { echo "workspace: $*" >&2; exit 1; }

# the repo's name from devcontainer.json's "name", and the folder's.
name_of() {
  local root=$1 repo
  repo=$(sed -n 's/^[[:space:]]*"name":[[:space:]]*"\([^"]*\)".*/\1/p' "$root/.devcontainer/devcontainer.json" | head -1)
  [ -n "$repo" ] || die "no \"name\" in $root/.devcontainer/devcontainer.json"
  echo "$repo-$(basename "$root")"
}

# the first port devcontainer.json forwards, or nothing.
app_port() {
  sed -n 's/.*"forwardPorts":[[:space:]]*\[[[:space:]]*\([0-9][0-9]*\).*/\1/p' "$1/.devcontainer/devcontainer.json" | head -1
}

# pod.yaml with WORKSPACE replaced by the name, written where kube play reads it.
rendered_pod() {
  local root=$1 ws=$2 out
  out=$(mktemp "${TMPDIR:-/tmp}/workspace-$ws.XXXXXX.yaml")
  sed "s/WORKSPACE/$ws/g" "$root/.devcontainer/pod.yaml" >"$out"
  echo "$out"
}

router_up() {
  mkdir -p "$router_state/routes" "$router_state/data" "$router_state/config"
  [ -f "$router_state/Caddyfile" ] || cat >"$router_state/Caddyfile" <<'CADDY'
{
	admin localhost:2019
	auto_https off
}
:80 {
	import /etc/caddy/routes/*.caddy
	handle {
		respond "no such workspace" 404
	}
}
CADDY
  podman network exists "$router" || podman network create "$router" >/dev/null
  if ! podman container exists "$router"; then
    podman run -d --name "$router" --network "$router" --cpus=1 --cpu-shares=256 \
      -p "127.0.0.1:$router_port:80" \
      -v "$router_state/Caddyfile:/etc/caddy/Caddyfile:Z" \
      -v "$router_state/routes:/etc/caddy/routes:Z" \
      -v "$router_state/data:/data:Z" -v "$router_state/config:/config:Z" \
      "$router_image" >/dev/null
  else
    podman start "$router" >/dev/null
  fi
}

router_reload() {
  for _ in $(seq 20); do
    podman exec "$router" caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null 2>&1 && return 0
    sleep 0.5
  done
  die "the router did not take its routes"
}

connected() { podman inspect "$router" --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' | grep -qw "$1"; }

up() {
  local attempt=0 root ws port pod
  [ "$1" = --attempt ] && { attempt=1; shift; }
  root=$(cd "$1" && pwd)
  ws=$(name_of "$root")
  [[ $ws =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]] && [ ${#ws} -le 63 ] ||
    die "$ws is not a DNS label: name the folder with lowercase letters, digits and dashes"
  if ! podman network exists "$ws"; then
    if [ $attempt = 1 ]; then
      podman network create --opt isolate=strict --internal "$ws" >/dev/null
    else
      podman network create --opt isolate=strict "$ws" >/dev/null
    fi
  fi
  if [ -f "$root/.devcontainer/pod.yaml" ] && ! podman pod exists "$ws"; then
    pod=$(rendered_pod "$root" "$ws")
    podman kube play --network "$ws:alias=postgres" "$pod" >/dev/null
    rm -f "$pod"
  fi
  port=$(app_port "$root")
  if [ $attempt = 0 ] && [ -n "$port" ]; then
    router_up
    connected "$ws" || podman network connect "$ws" "$router"
    printf '@%s host %s.localhost\nhandle @%s {\n\treverse_proxy %s-app:%s\n}\n' "$ws" "$ws" "$ws" "$ws" "$port" \
      >"$router_state/routes/$ws.caddy"
    router_reload
  fi
}

down() {
  local root ws pod
  root=$(cd "$1" && pwd)
  ws=$(name_of "$root")
  if [ -e "$router_state/routes/$ws.caddy" ]; then
    rm -f "$router_state/routes/$ws.caddy"
    podman container exists "$router" && router_reload
  fi
  if podman container exists "$router" && connected "$ws"; then
    podman network disconnect "$ws" "$router"
  fi
  if podman pod exists "$ws"; then
    pod=$(rendered_pod "$root" "$ws")
    podman kube play --down "$pod" >/dev/null
    rm -f "$pod"
  fi
  if podman network exists "$ws"; then
    podman network rm "$ws" >/dev/null || die "the network $ws is still in use: run just down first"
  fi
}

case ${1:-} in
up) shift; up "$@" ;;
down) shift; down "$@" ;;
name) name_of "$(cd "$2" && pwd)" ;;
url)
  url=http://$(name_of "$(cd "$2" && pwd)").localhost
  [ "$router_port" = 80 ] && echo "$url" || echo "$url:$router_port"
  ;;
*) die "usage: workspace.sh up [--attempt] <root> | down <root> | name <root> | url <root>" ;;
esac
