package devimage

import (
	"crypto/sha256"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/matta-kelly/crate/internal/declaration"
)

const registry = "ghcr.io/matta-kelly/"

var (
	defaultInputs        = []string{".devcontainer/devcontainer.json", ".devcontainer/Containerfile", "package-lock.json"}
	defaultContainerfile = ".devcontainer/Containerfile"
	defaultSmoke         = "scripts/dev-image-smoke.sh"
)

// plan is what a repo's declaration, and the commit it is read at, say about its dev image.
type plan struct {
	root          string
	rev           string
	stderr        io.Writer
	image         string // the name, with no tag
	inputs        []string
	containerfile string
	smoke         string
}

// open reads the declaration at the commit and settles the image name and the [dev] roles.
func open(o Options) (*plan, error) {
	p := &plan{root: o.Root, rev: o.Rev, stderr: o.Stderr}
	if p.rev == "" {
		p.rev = "HEAD"
	}
	if p.stderr == nil {
		p.stderr = io.Discard
	}
	decl, err := p.declaration()
	if err != nil {
		return nil, err
	}
	dev := decl.Dev
	p.inputs = orDefault(dev.Inputs, defaultInputs)
	p.containerfile = orDefault(dev.Containerfile, defaultContainerfile)
	p.smoke = orDefault(dev.Smoke, defaultSmoke)
	if !slices.Contains(p.inputs, p.containerfile) {
		return nil, fmt.Errorf("[dev] inputs lack the Containerfile %s: the hash must cover what the build reads", p.containerfile)
	}
	if p.image, err = p.imageName(decl, o.Name); err != nil {
		return nil, err
	}
	return p, nil
}

func orDefault[T string | []string](v, fallback T) T {
	if len(v) == 0 {
		return fallback
	}
	return v
}

// declaration reads crate.toml from the commit's tree. A commit with none gives the defaults.
func (p *plan) declaration() (declaration.Declaration, error) {
	listed, err := git(p.root, "ls-tree", p.rev, "--", "crate.toml")
	if err != nil {
		return declaration.Declaration{}, err
	}
	if listed == "" {
		return declaration.Declaration{}, nil
	}
	text, err := git(p.root, "show", p.rev+":crate.toml")
	if err != nil {
		return declaration.Declaration{}, err
	}
	return declaration.Parse([]byte(text))
}

// imageName: the first that is there wins: [dev] image, the declared name, --name, the repo's folder.
func (p *plan) imageName(decl declaration.Declaration, flag string) (string, error) {
	if image := decl.Dev.Image; image != "" {
		if strings.Contains(path.Base(image), ":") {
			return "", fmt.Errorf("[dev] image %s: expected a name with no tag", image)
		}
		return image, nil
	}
	name := decl.Name
	if name == "" {
		name = flag
	}
	if name == "" {
		var err error
		if name, err = p.repoName(); err != nil {
			return "", err
		}
	}
	return registry + name + "-dev", nil
}

// repoName is the folder that holds the git common folder, the same for each worktree of a checkout. A bare
// repo is its own folder with .git taken away.
func (p *plan) repoName() (string, error) {
	out, err := git(p.root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common := filepath.Clean(strings.TrimSpace(out))
	if filepath.Base(common) == ".git" {
		return filepath.Base(filepath.Dir(common)), nil
	}
	return strings.TrimSuffix(filepath.Base(common), ".git"), nil
}

// hash is git's tree entries of the inputs, which hold each file's blob hash and path, so one commit gives
// one hash on every machine. `git ls-tree` prints nothing for a missing path, so each is checked first.
func (p *plan) hash() (string, error) {
	for _, in := range p.inputs {
		entry, err := git(p.root, "ls-tree", p.rev, "--", in)
		if err != nil {
			return "", err
		}
		if entry == "" {
			return "", fmt.Errorf("no %s at %s", in, p.rev)
		}
	}
	entries, err := git(p.root, append([]string{"ls-tree", "-r", p.rev, "--"}, p.inputs...)...)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(entries)))[:16], nil
}
