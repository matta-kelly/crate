// Package system makes a workspace as a running system from its checkout's declaration: its network, its
// services as a pod each, its tools member, its setup, and its removal.
package system

import (
	"context"
	"errors"
	"io"

	"github.com/matta-kelly/crate/internal/machine"
)

// The four kinds of a system, as the machine names them.
const (
	Work       = machine.Work
	Attempt    = machine.Attempt
	Check      = machine.Check
	Postsubmit = machine.Postsubmit
)

// But is what the system cannot do yet.
var But = errors.New("system: not built")

// Mount is a host folder the caller adds, read-only, at the same path inside.
type Mount struct {
	Path string
}

// Spec is one system. Kind is one of the four; Name is the gate's name or a workspace's; Checkout is the
// folder that holds crate.toml. Keep names the volumes a gate's Up leaves in place when it clears a crash's
// system, and Out is where Up's and Remove's steps and failures go.
type Spec struct {
	Kind     string
	Name     string
	Checkout string
	Mounts   []Mount
	Env      []string
	Keep     []string
	Out      io.Writer
}

// System is a system of one name on one machine.
type System struct {
	m    machine.Machine
	spec Spec
}

// New makes a system on m. It starts nothing.
func New(m machine.Machine, spec Spec) *System { return &System{m: m, spec: spec} }

// Spec is what the system was made from.
func (s *System) Spec() Spec { return s.spec }

// Name is the system's name: its network, its pods, its containers and its volumes start with it.
func (s *System) Name() string { return s.spec.Name }

// Hold waits for the system's slot on its machine.
func (s *System) Hold() error { return But }

// Image puts the system's dev image on the machine.
func (s *System) Image() error { return But }

// Up makes the network, the services, the tools member and the setup, all by ctx.
func (s *System) Up(ctx context.Context) error { return But }

// Run runs argv in the tools member.
func (s *System) Run(ctx context.Context, argv []string, out io.Writer) (int, error) { return 0, But }

// Services makes the services again, with the tools member left running.
func (s *System) Services() error { return But }

// Logs writes a member's output to out.
func (s *System) Logs(member string, out io.Writer) error { return But }

// Remove removes the system, all but the named volumes, and releases its slot.
func (s *System) Remove(keep []string) error { return But }

// Name is a workspace's name: <declared>-<folder>, and <folder>-<folder> when no name is declared.
func Name(declared, folder string) string { return "" }

// CheckName refuses a name that is not a DNS label, and says why.
func CheckName(name string) error { return But }

// Service is one pod of the services file: its rendered name, its alias and its rendered document.
type Service struct {
	Name     string
	Alias    string
	Document string
}

// ServicesOf splits a services file and renders each Pod for a system: WORKSPACE replaced everywhere, the
// Pod named <system>-<service>, its alias <service>. A document that is not a Pod, a Pod named otherwise,
// and a container with resources are refused, with the document and the reason.
func ServicesOf(data []byte, systemName string) ([]Service, error) { return nil, But }
