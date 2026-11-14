package declaration_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/matta-kelly/crate/internal/declaration"
)

const alpine = "docker.io/library/alpine@sha256:0f49f331d9212aff11b932863be4eb01cdf962969aa86dab7d1ee21ac6f5f52b"

// The flat keys: each gives the string `toml_value` in lib/crate.sh gives today, and an absent key gives empty.
// The rows are the crate.toml files test/*.test.sh write, the README's example, and what people do.
func TestEachFlatKeyGivesItsString(t *testing.T) {
	rows := []struct {
		name string
		toml string
		want declaration.Declaration
	}{
		{"crate and image, as hooks.test.sh writes them",
			"crate = \"1\"\nimage = \"" + alpine + "\"\n",
			declaration.Declaration{Crate: "1", Image: alpine}},
		{"a timeout",
			"crate = \"1\"\nimage = \"" + alpine + "\"\ntimeout = \"3\"\n",
			declaration.Declaration{Crate: "1", Image: alpine, Timeout: "3"}},
		{"ship and packages with two folders",
			"crate = \"1\"\nimage = \"" + alpine + "\"\nship = \"npm\"\npackages = \". it\"\n",
			declaration.Declaration{Crate: "1", Image: alpine, Ship: "npm", Packages: ". it"}},
		{"a postsubmit",
			"crate = \"1\"\nimage = \"" + alpine + "\"\nship = \"npm\"\npostsubmit = \"slow\"\n",
			declaration.Declaration{Crate: "1", Image: alpine, Ship: "npm", Postsubmit: "slow"}},
		{"image dev",
			"crate = \"1\"\nimage = \"dev\"\n",
			declaration.Declaration{Crate: "1", Image: "dev"}},
		{"the README's example, with a comment after each value",
			"crate = \"1\"      # the crate version it runs\n" +
				"image = \"dev\"    # its own dev image\n" +
				"timeout = \"900\"  # seconds a check may run\n" +
				"ship = \"npm\"     # how a version ships\n" +
				"packages = \". it\"  # the package folders\n" +
				"postsubmit = \"e2e\" # the slow sets\n" +
				"postsubmit_timeout = \"3600\"  # seconds postsubmit may run\n",
			declaration.Declaration{Crate: "1", Image: "dev", Timeout: "900", Ship: "npm", Packages: ". it", Postsubmit: "e2e", PostsubmitTimeout: "3600"}},
		{"spaces around the equals sign and before the key",
			"  crate   =   \"1\"\n\tship=\"npm\"\n",
			declaration.Declaration{Crate: "1", Ship: "npm"}},
		{"keys in another order, with blank lines between",
			"\nship = \"npm\"\n\n\ncrate = \"2\"\n",
			declaration.Declaration{Crate: "2", Ship: "npm"}},
		{"a file of no keys", "", declaration.Declaration{}},
		{"a name beside the flat keys",
			"crate = \"1\"\nname = \"que-pasa\"\n",
			declaration.Declaration{Crate: "1", Name: "que-pasa"}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got, err := declaration.Parse([]byte(row.toml))
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			got.Dev = declaration.Dev{}
			if !reflect.DeepEqual(got, row.want) {
				t.Errorf("expected %+v, got %+v", row.want, got)
			}
		})
	}
}

func TestTheDevTableReadsAsTypedValues(t *testing.T) {
	rows := []struct {
		name string
		toml string
		want declaration.Dev
	}{
		{"every key",
			"name = \"que-pasa\"\n\n[dev]\nimage = \"example.test/team/custom-dev\"\ncontainerfile = \"docker/Containerfile\"\n" +
				"inputs = [\".devcontainer/devcontainer.json\", \"docker/Containerfile\", \"package-lock.json\"]\nsmoke = \"ci/smoke.sh\"\n",
			declaration.Dev{Image: "example.test/team/custom-dev", Containerfile: "docker/Containerfile",
				Inputs: []string{".devcontainer/devcontainer.json", "docker/Containerfile", "package-lock.json"}, Smoke: "ci/smoke.sh"}},
		{"inputs of one path, over several lines",
			"[dev]\ninputs = [\n  \"a b/c.txt\",\n]\n",
			declaration.Dev{Inputs: []string{"a b/c.txt"}}},
		{"only a smoke",
			"crate = \"1\"\n\n[dev]\nsmoke = \"scripts/dev-image-smoke.sh\"\n",
			declaration.Dev{Smoke: "scripts/dev-image-smoke.sh"}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got, err := declaration.Parse([]byte(row.toml))
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if !reflect.DeepEqual(got.Dev, row.want) {
				t.Errorf("[dev]: expected %+v, got %+v", row.want, got.Dev)
			}
		})
	}
}

func TestAKeyOfTheDevTableIsNotAFlatKey(t *testing.T) {
	got, err := declaration.Parse([]byte("crate = \"1\"\n\n[dev]\nimage = \"example.test/team/custom-dev\"\n"))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got.Image != "" || got.Dev.Image != "example.test/team/custom-dev" {
		t.Errorf("expected the flat image empty and [dev] image set, got flat %q and [dev] %q", got.Image, got.Dev.Image)
	}
}

func TestAKeyItDoesNotKnowIsAnErrorThatNamesIt(t *testing.T) {
	rows := []struct{ name, toml, key string }{
		{"a flat key", "crate = \"1\"\nbogus_key = \"x\"\n", "bogus_key"},
		{"a misspelt known key", "crate = \"1\"\nshipp = \"npm\"\n", "shipp"},
		{"a key of [dev]", "[dev]\nsmoke = \"s\"\nbogus_dev = \"x\"\n", "bogus_dev"},
		{"a table", "crate = \"1\"\n\n[other]\nx = \"y\"\n", "other"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			_, err := declaration.Parse([]byte(row.toml))
			if err == nil || !strings.Contains(err.Error(), row.key) {
				t.Errorf("expected an error that names %q, got %v", row.key, err)
			}
		})
	}
}

func TestATextThatIsNotTomlIsAnError(t *testing.T) {
	if _, err := declaration.Parse([]byte("crate = \"1\nthis is not toml")); err == nil {
		t.Error("expected an error for text that is not TOML, got none")
	}
}

// [app]: how the repo's app runs. A built app must name the port it serves, and each port has a name and a number.
func TestTheAppTableReadsAndABuiltAppMustNameItsPort(t *testing.T) {
	good := "[app]\nbuild = \"Dockerfile\"\ncommand = \"pnpm dev\"\nhealth = \"wget -qO- http://127.0.0.1:3000/\"\n" +
		"env = { PORT = \"3000\" }\nneeds = [\"IMMICH_URL\", \"_KEY_2\"]\n[[app.ports]]\nname = \"web\"\nport = 3000\n"
	d, err := declaration.Parse([]byte(good))
	want := declaration.App{Build: "Dockerfile", Command: "pnpm dev", Health: "wget -qO- http://127.0.0.1:3000/",
		Env: map[string]string{"PORT": "3000"}, Ports: []declaration.Port{{Name: "web", Port: 3000}},
		Needs: []string{"IMMICH_URL", "_KEY_2"}}
	if err != nil || !reflect.DeepEqual(d.App, want) {
		t.Errorf("a whole [app]: expected %+v, got %+v, %v", want, d.App, err)
	}
	refused := []struct{ name, toml, says string }{
		{"a build with no port", "[app]\nbuild = \"Dockerfile\"\n", "[[app.ports]]"},
		{"a port with no name", "[app]\ncommand = \"x\"\n[[app.ports]]\nport = 3000\n", "[[app.ports]]"},
		{"a port out of range", "[app]\ncommand = \"x\"\n[[app.ports]]\nname = \"web\"\nport = 70000\n", "70000"},
		{"a key [app] does not know", "[app]\nrefuses = []\n", "app.refuses"},
		{"a need that is not an env name", "[app]\nneeds = [\"IMMICH-URL\"]\n", "IMMICH-URL"},
		{"a need that starts with a digit", "[app]\nneeds = [\"1KEY\"]\n", "1KEY"},
		{"an empty need", "[app]\nneeds = [\"\"]\n", "env name"},
	}
	for _, row := range refused {
		t.Run(row.name, func(t *testing.T) {
			if _, err := declaration.Parse([]byte(row.toml)); err == nil || !strings.Contains(err.Error(), row.says) {
				t.Errorf("expected an error that says %q, got %v", row.says, err)
			}
		})
	}
}

// batch is the most changes the merge queue checks in one run: its value, DefaultBatch when absent, and a count
// below 1 refused.
func TestBatchIsACountOfChanges(t *testing.T) {
	d, err := declaration.Parse([]byte("batch = 2\n"))
	if err != nil || d.BatchSize() != 2 {
		t.Errorf("batch = 2: expected 2, got %d, %v", d.BatchSize(), err)
	}
	d, err = declaration.Parse([]byte("crate = \"1\"\n"))
	if err != nil || d.BatchSize() != declaration.DefaultBatch {
		t.Errorf("no batch: expected %d, got %d, %v", declaration.DefaultBatch, d.BatchSize(), err)
	}
	if _, err := declaration.Parse([]byte("batch = -1\n")); err == nil || !strings.Contains(err.Error(), "batch") {
		t.Errorf("batch = -1: expected an error that names batch, got %v", err)
	}
}
