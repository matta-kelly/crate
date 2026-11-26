package system

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/matta-kelly/crate/internal/declaration"
	"github.com/matta-kelly/crate/internal/fault"
	"github.com/matta-kelly/crate/internal/member"
	"github.com/matta-kelly/crate/internal/record"
	"go.yaml.in/yaml/v3"
)

// The app's definition: [app] deploy, the folder that holds how the app runs in every place. app.yaml is one Pod,
// the same everywhere; dev.yaml is the values (a ConfigMap) and the secrets (a Secret) of a workspace and a check;
// prod.yaml is production's values, whose secrets the node holds. crate plays app.yaml with dev.yaml, rendered for
// its system (render).
const (
	DefinitionFile = "app.yaml"
	DevValuesFile  = "dev.yaml"
	ProdValuesFile = "prod.yaml"
)

// Dev is the change a person's workspace and an attempt make to every container of the definition, so it runs the
// checkout with reload: the dev image, the checkout at its own path as the working folder, its volumes, the user
// the tools member has, and no security context. The app's container (the first) runs [app] command; an init
// container keeps its own command, which runs from the checkout. Env is added to the app's container, in order.
type Dev struct {
	Image    string
	Checkout string
	Command  string
	Volumes  []DevVolume
	Mounts   []string // host folders at the same path inside, read-only
	Env      []string // KEY=value
}

// DevVolume is a named volume at a path inside each container.
type DevVolume struct {
	Name string
	Path string
}

// Render is what render makes of a definition for one system.
type Render struct {
	Document  string             // the Pod, then the values and secrets, as one file for kube play
	Pod       string             // the Pod's name: <system>-app
	Container string             // the app's container, as podman names it: <pod>-<the first container's name>
	Ports     []declaration.Port // the app's container's ports, by name
	Health    bool               // the app's container has a livenessProbe, which podman runs as its health check
	Secrets   []string           // the podman secrets the play makes, one per Secret: <system>-<its name>
}

// readDefinition reads app.yaml and dev.yaml from the checkout's deploy folder.
func readDefinition(checkout, folder string) (definition, values []byte, err error) {
	dir := filepath.Join(checkout, folder)
	if definition, err = os.ReadFile(filepath.Join(dir, DefinitionFile)); err != nil {
		return nil, nil, fmt.Errorf("[app] deploy: %w", err)
	}
	if values, err = os.ReadFile(filepath.Join(dir, DevValuesFile)); err != nil {
		return nil, nil, fmt.Errorf("[app] deploy: %w", err)
	}
	return definition, values, nil
}

// render makes a definition and its values one system's: the Pod named <system>-app, each Secret named
// <system>-<its name> and each reference to it with it, so two systems of one repo never share a name podman keeps;
// each image that ends in :VERSION (the one the repo ships) made image; and, with dev, the workspace's change (Dev).
// A definition that is not one Pod with a container, and values that are not ConfigMaps and Secrets, are refused.
func render(definition, values []byte, system, image string, dev *Dev) (Render, error) {
	var pod yaml.Node
	if err := yaml.Unmarshal(definition, &pod); err != nil {
		return Render{}, fmt.Errorf("[app] deploy: %s: %w", DefinitionFile, err)
	}
	root := documentRoot(&pod)
	if root == nil || scalar(root, "kind") != "Pod" {
		return Render{}, fmt.Errorf("[app] deploy: %s: expected one Pod", DefinitionFile)
	}
	spec := field(root, "spec")
	containers := field(spec, "containers")
	if containers == nil || containers.Kind != yaml.SequenceNode || len(containers.Content) == 0 {
		return Render{}, fmt.Errorf("[app] deploy: %s: the Pod has no containers", DefinitionFile)
	}

	out := Render{Pod: system + "-app"}
	setScalar(ensureMap(root, "metadata"), "name", out.Pod)

	var docs []*yaml.Node
	renamed := map[string]string{}
	for _, doc := range splitDocuments(string(values)) {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(doc), &n); err != nil {
			return Render{}, fmt.Errorf("[app] deploy: %s: %w", DevValuesFile, err)
		}
		r := documentRoot(&n)
		if r == nil {
			continue
		}
		switch scalar(r, "kind") {
		case "ConfigMap":
		case "Secret":
			meta := ensureMap(r, "metadata")
			name := scalar(meta, "name")
			renamed[name] = system + "-" + name
			setScalar(meta, "name", renamed[name])
			out.Secrets = append(out.Secrets, renamed[name])
		default:
			return Render{}, fmt.Errorf("[app] deploy: %s: a document that is not a ConfigMap or a Secret: %s", DevValuesFile, scalar(r, "kind"))
		}
		docs = append(docs, &n)
	}

	inits := field(spec, "initContainers")
	var all []*yaml.Node
	if inits != nil {
		all = append(all, inits.Content...)
	}
	all = append(all, containers.Content...)
	app := containers.Content[0]
	for _, c := range all {
		if strings.HasSuffix(scalar(c, "image"), ":VERSION") {
			setScalar(c, "image", image)
			// the image is the system's own, made here: never pulled
			setScalar(c, "imagePullPolicy", "IfNotPresent")
		}
		renameSecretRefs(c, renamed)
	}
	if dev != nil {
		devVolumes(spec, dev)
		for _, c := range all {
			devContainer(c, dev)
		}
		setSequence(app, "command", []string{"sh", "-c", dev.Command})
		for _, kv := range dev.Env {
			name, value, _ := strings.Cut(kv, "=")
			setEnv(app, name, value)
		}
	}

	out.Container = out.Pod + "-" + scalar(app, "name")
	out.Health = field(app, "livenessProbe") != nil
	if ports := field(app, "ports"); ports != nil {
		for _, p := range ports.Content {
			var port struct {
				Name          string `yaml:"name"`
				ContainerPort int    `yaml:"containerPort"`
			}
			if err := p.Decode(&port); err != nil {
				return Render{}, fmt.Errorf("[app] deploy: %s: a port: %w", DefinitionFile, err)
			}
			if port.Name == "" || port.ContainerPort < 1 || port.ContainerPort > 65535 {
				return Render{}, fmt.Errorf("[app] deploy: %s: a port %q %d: expected a name and a containerPort from 1 to 65535", DefinitionFile, port.Name, port.ContainerPort)
			}
			out.Ports = append(out.Ports, declaration.Port{Name: port.Name, Port: port.ContainerPort})
		}
	}

	parts := []*yaml.Node{&pod}
	parts = append(parts, docs...)
	var text []string
	for _, n := range parts {
		b, err := yaml.Marshal(n)
		if err != nil {
			return Render{}, err
		}
		text = append(text, string(b))
	}
	out.Document = strings.Join(text, "---\n")
	return out, nil
}

// devVolumes adds the workspace's volumes to the Pod: the checkout, each caller's mount, and each named volume.
func devVolumes(spec *yaml.Node, dev *Dev) {
	volumes := ensureSeq(spec, "volumes")
	add := func(name string, source map[string]any) {
		var n yaml.Node
		_ = n.Encode(map[string]any{"name": name})
		for key, value := range source {
			var v yaml.Node
			_ = v.Encode(value)
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, &v)
		}
		volumes.Content = append(volumes.Content, &n)
	}
	add("crate-checkout", map[string]any{"hostPath": map[string]string{"path": dev.Checkout, "type": "Directory"}})
	for i, mount := range dev.Mounts {
		add(fmt.Sprintf("crate-mount-%d", i), map[string]any{"hostPath": map[string]string{"path": mount, "type": "Directory"}})
	}
	for _, v := range dev.Volumes {
		add(v.Name, map[string]any{"persistentVolumeClaim": map[string]string{"claimName": v.Name}})
	}
}

// devContainer makes one container run the checkout: the dev image, the checkout as its working folder, its mounts,
// the tools member's user and HOME, and no security context, which the built image's run alone holds.
func devContainer(c *yaml.Node, dev *Dev) {
	setScalar(c, "image", dev.Image)
	setScalar(c, "imagePullPolicy", "IfNotPresent")
	setScalar(c, "workingDir", dev.Checkout)
	deleteField(c, "securityContext")
	var sc yaml.Node
	_ = sc.Encode(map[string]int{"runAsUser": 1000, "runAsGroup": 1000})
	setField(c, "securityContext", &sc)
	mounts := ensureSeq(c, "volumeMounts")
	add := func(name, path string, readOnly bool) {
		var n yaml.Node
		m := map[string]any{"name": name, "mountPath": path}
		if readOnly {
			m["readOnly"] = true
		}
		_ = n.Encode(m)
		mounts.Content = append(mounts.Content, &n)
	}
	add("crate-checkout", dev.Checkout, false)
	for i, mount := range dev.Mounts {
		add(fmt.Sprintf("crate-mount-%d", i), mount, true)
	}
	for _, v := range dev.Volumes {
		add(v.Name, v.Path, false)
	}
	setEnv(c, "HOME", "/home/node")
}

// renameSecretRefs points each secretKeyRef and secretRef of a container at its Secret's new name.
func renameSecretRefs(n *yaml.Node, renamed map[string]string) {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i].Value, n.Content[i+1]
			if (key == "secretKeyRef" || key == "secretRef") && value.Kind == yaml.MappingNode {
				if to, ok := renamed[scalar(value, "name")]; ok {
					setScalar(value, "name", to)
				}
			}
		}
	}
	for _, child := range n.Content {
		renameSecretRefs(child, renamed)
	}
}

// setEnv sets one env entry of a container by name, in place when it is there, else at the end.
func setEnv(c *yaml.Node, name, value string) {
	env := ensureSeq(c, "env")
	var entry yaml.Node
	_ = entry.Encode(map[string]string{"name": name, "value": value})
	for i, e := range env.Content {
		if scalar(e, "name") == name {
			env.Content[i] = &entry
			return
		}
	}
	env.Content = append(env.Content, &entry)
}

func documentRoot(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 && n.Content[0].Kind == yaml.MappingNode {
		return n.Content[0]
	}
	return nil
}

func field(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func scalar(n *yaml.Node, key string) string {
	if v := field(n, key); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

func setField(n *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = value
			return
		}
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
}

func deleteField(n *yaml.Node, key string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return
		}
	}
}

func setScalar(n *yaml.Node, key, value string) {
	setField(n, key, &yaml.Node{Kind: yaml.ScalarNode, Value: value})
}

func setSequence(n *yaml.Node, key string, values []string) {
	var v yaml.Node
	_ = v.Encode(values)
	setField(n, key, &v)
}

// ensureMap and ensureSeq are a field of n of that kind, made when absent. Each yaml.Node a caller passes in is a
// mapping, so the field can be added.
func ensureMap(n *yaml.Node, key string) *yaml.Node {
	if v := field(n, key); v != nil && v.Kind == yaml.MappingNode {
		return v
	}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setField(n, key, v)
	return v
}

func ensureSeq(n *yaml.Node, key string) *yaml.Node {
	if v := field(n, key); v != nil && v.Kind == yaml.SequenceNode {
		v.Style = 0
		return v
	}
	v := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	setField(n, key, v)
	return v
}

// definedApp makes the app member from [app] deploy: its definition played as one Pod with dev.yaml, the built image
// in a check and postsubmit, and the dev server from the checkout (Dev) in a person's workspace and an attempt. It
// waits for the app's livenessProbe, as its health check, then a person's workspace gets its routes. A kind with
// nothing to run makes no app. podman refuses a definition whose deploy lacks a key it names, and names it.
func (s *System) definedApp(ctx context.Context, decl declaration.Declaration, folders []string) error {
	a := decl.App
	definition, values, err := readDefinition(s.spec.Checkout, a.Deploy)
	if err != nil {
		return fault.Change(err)
	}
	var image string
	var dev *Dev
	switch {
	case s.built() && a.Build != "":
		placement, err := s.place()
		if err != nil {
			return err
		}
		if err := s.build(ctx, a.Build, placement); err != nil {
			return err
		}
		image = "localhost/" + s.appName()
	case !s.built() && a.Command != "":
		dev = &Dev{Image: s.image, Checkout: s.spec.Checkout, Command: a.Command, Env: slices.Clone(s.spec.Env)}
		if len(folders) == 0 {
			folders = []string{member.DefaultFolder}
		}
		for _, folder := range folders {
			dev.Volumes = append(dev.Volumes, DevVolume{Name: member.VolumeName(s.spec.Name, folder), Path: filepath.Join(s.spec.Checkout, folder)})
		}
		for _, mount := range s.spec.Mounts {
			dev.Mounts = append(dev.Mounts, mount.Path)
		}
		// a name passed by up -e: its value from crate's env, into the play's file, never into argv
		for _, name := range s.spec.Pass {
			dev.Env = append(dev.Env, name+"="+os.Getenv(name))
		}
		routerEnv, err := s.routerEnv(a)
		if err != nil {
			return err
		}
		if routerEnv != "" {
			dev.Env = append(dev.Env, routerEnv)
		}
	default:
		return nil
	}
	r, err := render(definition, values, s.spec.Name, image, dev)
	if err != nil {
		return fault.Change(err)
	}
	if s.built() && len(r.Ports) == 0 {
		return fault.Change(fmt.Errorf("app: [app] deploy: the app's container in %s has no ports: a built app must say the port it serves", DefinitionFile))
	}

	step := s.spec.Record.Step(record.App)
	err = s.playApp(ctx, r, dev != nil)
	step.End(err, "")
	return err
}

// playApp plays the rendered app unless its container runs already, waits for its health, and adds a person's
// routes. A dev server that takes names (up -e) is played again, as its env is set when it is made.
func (s *System) playApp(ctx context.Context, r Render, dev bool) error {
	running, err := s.containerExists(ctx, r.Container)
	if err != nil {
		return err
	}
	if running {
		if running, err = s.running(ctx, r.Container); err != nil {
			return err
		}
	}
	if !running || len(s.spec.Pass) > 0 {
		if err := s.removeDefined(ctx, r.Pod, r.Secrets); err != nil {
			return err
		}
		placement, err := s.place()
		if err != nil {
			return err
		}
		args := []string{"--network", s.spec.Name + ":alias=app,alias=" + s.appName()}
		if dev {
			args = append(args, "--userns=keep-id:uid=1000,gid=1000")
		}
		if err := s.playDocument(ctx, r.Document, args, placement); err != nil {
			return err
		}
	}
	if r.Health {
		if err := s.ready(ctx, r.Container); err != nil {
			return err
		}
	}
	return s.route(r.Ports)
}

// removeDefined removes the app's Pod and the secrets its play made, if they are there.
func (s *System) removeDefined(ctx context.Context, pod string, secrets []string) error {
	made, err := s.podExists(ctx, pod)
	if err != nil {
		return err
	}
	if made {
		if _, err := s.call(ctx, "pod", "rm", "-f", "-t", "0", pod); err != nil {
			return fmt.Errorf("app: the pod %s is still there: %w", pod, err)
		}
	}
	for _, secret := range secrets {
		made, err := s.exists(ctx, "secret", "exists", secret)
		if err != nil {
			return err
		}
		if made {
			if _, err := s.call(ctx, "secret", "rm", secret); err != nil {
				return fmt.Errorf("app: the secret %s is still there: %w", secret, err)
			}
		}
	}
	return nil
}
