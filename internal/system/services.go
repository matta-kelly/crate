package system

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Service is one pod of the services file: its rendered name, its alias and its rendered document.
type Service struct {
	Name     string
	Alias    string
	Document string
}

// serviceDocument is the part of a services file's document that crate reads.
type serviceDocument struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Containers     []serviceContainer `yaml:"containers"`
		InitContainers []serviceContainer `yaml:"initContainers"`
	} `yaml:"spec"`
}

type serviceContainer struct {
	Resources struct {
		Limits   any `yaml:"limits"`
		Requests any `yaml:"requests"`
	} `yaml:"resources"`
}

// ServicesOf splits a services file and renders each Pod for a system: WORKSPACE replaced everywhere, the
// Pod named <system>-<service>, its alias <service>. A document that is not a Pod, a Pod named otherwise,
// and a container with resources are refused, with the document and the reason.
func ServicesOf(data []byte, systemName string) ([]Service, error) {
	var services []Service
	for _, doc := range splitDocuments(string(data)) {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var parsed serviceDocument
		if err := yaml.Unmarshal([]byte(doc), &parsed); err != nil {
			return nil, fmt.Errorf("services file: %v: %s", err, doc)
		}
		if parsed.Kind != "Pod" {
			return nil, fmt.Errorf("services file: a document that is not a Pod: %s", doc)
		}
		name := parsed.Metadata.Name
		if !strings.HasPrefix(name, "WORKSPACE-") || name == "WORKSPACE-" {
			return nil, fmt.Errorf("services file: the Pod %q: expected its name WORKSPACE-<service>: %s", name, doc)
		}
		for _, resource := range []string{"limits", "requests"} {
			for _, containers := range [][]serviceContainer{parsed.Spec.Containers, parsed.Spec.InitContainers} {
				for _, container := range containers {
					var set bool
					if resource == "limits" {
						set = container.Resources.Limits != nil
					} else {
						set = container.Resources.Requests != nil
					}
					if set {
						return nil, fmt.Errorf("services file: a container sets resources.%s, which is not kept under cgroups = \"disabled\": %s", resource, doc)
					}
				}
			}
		}
		alias := strings.TrimPrefix(name, "WORKSPACE-")
		services = append(services, Service{
			Name:     systemName + "-" + alias,
			Alias:    alias,
			Document: strings.ReplaceAll(doc, "WORKSPACE", systemName),
		})
	}
	return services, nil
}

// volumesOf is the named volumes one rendered Pod document declares.
func volumesOf(document string) []string {
	var pod struct {
		Spec struct {
			Volumes []struct {
				Name string `yaml:"name"`
			} `yaml:"volumes"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(document), &pod); err != nil {
		return nil
	}
	var names []string
	for _, volume := range pod.Spec.Volumes {
		if volume.Name != "" {
			names = append(names, volume.Name)
		}
	}
	return names
}

// splitDocuments is a services file's documents, in order, on the lines that hold only ---.
func splitDocuments(text string) []string {
	var documents []string
	var current []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "---" {
			documents = append(documents, strings.Join(current, "\n"))
			current = nil
			continue
		}
		current = append(current, line)
	}
	return append(documents, strings.Join(current, "\n"))
}
