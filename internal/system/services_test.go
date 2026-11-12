package system_test

import (
	"strings"
	"testing"

	"github.com/matta-kelly/crate/internal/system"
)

const postgresRedis = `apiVersion: v1
kind: Pod
metadata:
  name: WORKSPACE-postgres
spec:
  containers:
    - name: postgres
      image: docker.io/library/alpine
      command: ["httpd", "-f", "-p", "5432"]
      env:
        - {name: POSTGRES_DB, value: WORKSPACE-db}
      volumeMounts:
        - {name: WORKSPACE-pgdata, mountPath: /var/lib/postgresql/data}
  volumes:
    - name: WORKSPACE-pgdata
      emptyDir: {}
---
apiVersion: v1
kind: Pod
metadata:
  name: WORKSPACE-redis
spec:
  containers:
    - name: redis
      image: docker.io/library/alpine
`

// Each document is a pod named <system>-<service>, its alias is <service>, and WORKSPACE is replaced
// everywhere in it.
func TestServicesArePodsNamedForTheSystem(t *testing.T) {
	services, err := system.ServicesOf([]byte(postgresRedis), "que-pasa-work")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(services) != 2 {
		t.Fatalf("the services: expected 2, got %d: %+v", len(services), services)
	}
	want := []struct{ name, alias string }{
		{"que-pasa-work-postgres", "postgres"},
		{"que-pasa-work-redis", "redis"},
	}
	for i, w := range want {
		got := services[i]
		if got.Name != w.name || got.Alias != w.alias {
			t.Errorf("service %d: expected %q with alias %q, got %q with alias %q", i+1, w.name, w.alias, got.Name, got.Alias)
		}
		if !strings.Contains(got.Document, "name: "+w.name) {
			t.Errorf("service %d's document: expected the pod named %q, got\n%s", i+1, w.name, got.Document)
		}
		if strings.Contains(got.Document, "WORKSPACE") {
			t.Errorf("service %d's document: expected no WORKSPACE left, got\n%s", i+1, got.Document)
		}
	}
	for _, want := range []string{"name: que-pasa-work-pgdata", "value: que-pasa-work-db"} {
		if !strings.Contains(services[0].Document, want) {
			t.Errorf("the postgres document: expected %q, got\n%s", want, services[0].Document)
		}
	}
}

// A services file crate cannot play is refused before anything starts, with the document and the reason.
func TestAServicesFileThatCannotBePlayedIsRefused(t *testing.T) {
	const doc = "apiVersion: v1\n"
	rows := []struct{ name, body, reason string }{
		{"a document that is not a Pod",
			"apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: WORKSPACE-postgres\n", "Pod"},
		{"a Pod named otherwise",
			doc + "kind: Pod\nmetadata:\n  name: db\n", "WORKSPACE-"},
		{"a container with resources.limits",
			doc + "kind: Pod\nmetadata:\n  name: WORKSPACE-postgres\nspec:\n  containers:\n    - name: postgres\n      image: x\n      resources:\n        limits:\n          cpu: \"3\"\n", "limits"},
		{"a container with resources.requests",
			doc + "kind: Pod\nmetadata:\n  name: WORKSPACE-redis\nspec:\n  containers:\n    - name: redis\n      image: x\n      resources:\n        requests:\n          memory: 1G\n", "requests"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			services, err := system.ServicesOf([]byte(row.body), "que-pasa-work")
			if err == nil || len(services) != 0 {
				t.Fatalf("expected no services and an error, got %+v, %v", services, err)
			}
			if !strings.Contains(err.Error(), row.reason) {
				t.Errorf("the error: expected it to give the reason %q, got %v", row.reason, err)
			}
			if !strings.Contains(err.Error(), "apiVersion") {
				t.Errorf("the error: expected it to hold the document, got %v", err)
			}
		})
	}
}
