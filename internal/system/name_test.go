package system_test

import (
	"strings"
	"testing"

	"github.com/matta-kelly/crate/internal/system"
)

// A workspace is <name>-<folder>; in a folder of its name, or with no name declared, it is the folder alone.
func TestANameIsTheDeclaredNameAndTheFolder(t *testing.T) {
	rows := []struct {
		name, declared, folder, want string
	}{
		{"a declared name", "que-pasa", "work", "que-pasa-work"},
		{"no declared name", "", "que-pasa", "que-pasa"},
		{"a checkout in a folder of its name", "que-pasa", "que-pasa", "que-pasa"},
		{"a name crate keeps for itself", "", "router", "router-router"},
		{"a worktree of a checkout", "que-pasa", "try-1", "que-pasa-try-1"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if got := system.Name(row.declared, row.folder); got != row.want {
				t.Errorf("Name(%q, %q): expected %q, got %q", row.declared, row.folder, row.want, got)
			}
		})
	}
}

// A name that is a DNS label is accepted, and any other is refused with the name and the reason.
func TestANameThatIsNotADNSLabelIsRefused(t *testing.T) {
	for _, name := range []string{"que-pasa-work", "a", "0", "a-b-c", "a" + strings.Repeat("b", 62)} {
		t.Run("accepted "+name, func(t *testing.T) {
			if err := system.CheckName(name); err != nil {
				t.Errorf("CheckName(%q): expected no error, got %v", name, err)
			}
		})
	}
	for _, name := range []string{"", "Que-Pasa", "que_pasa", "que.pasa", "-que", "que-", "qué", "a" + strings.Repeat("b", 63)} {
		t.Run("refused "+name, func(t *testing.T) {
			err := system.CheckName(name)
			if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "DNS label") {
				t.Errorf("CheckName(%q): expected an error that names it and says DNS label, got %v", name, err)
			}
		})
	}
}
