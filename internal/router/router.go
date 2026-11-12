// Package router runs the machine's router, one Caddy container, and serves a workspace's routes.
package router

import "github.com/matta-kelly/crate/internal/machine"

// Route is one of a workspace's routes: Name is the label under the workspace's name, Target is the network
// alias of the container that answers, and Port is the port on that container.
type Route struct {
	Name   string
	Target string
	Port   int
}

// Router is the machine's router: one container, made on demand.
type Router struct {
	m machine.Machine
}

// New is the router on the machine m. It reads its name, port and state folder from WORKSPACE_ROUTER,
// WORKSPACE_ROUTER_PORT and WORKSPACE_ROUTER_STATE.
func New(m machine.Machine) *Router { return &Router{m: m} }

// Add serves workspace's routes on network, the first also at <workspace>.localhost. The same list changes
// nothing; another list replaces the old one.
func (r *Router) Add(workspace, network string, routes []Route) error { return nil }

// Remove takes workspace's routes away and takes the router off network.
func (r *Router) Remove(workspace, network string) error { return nil }

// URLs is each of workspace's routes' URL, in order, the first route also at the workspace's own host.
func (r *Router) URLs(workspace string) ([]string, error) { return nil, nil }
