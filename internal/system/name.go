package system

import "fmt"

// Name is a workspace's name: <declared>-<folder>, and <folder>-<folder> when no name is declared.
func Name(declared, folder string) string {
	if declared == "" {
		declared = folder
	}
	return declared + "-" + folder
}

// CheckName refuses a name that is not a DNS label, and says why.
func CheckName(name string) error {
	if len(name) == 0 || len(name) > 63 {
		return fmt.Errorf("the name %q is not a DNS label: expected 1 to 63 characters", name)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		letter := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if !letter && c != '-' {
			return fmt.Errorf("the name %q is not a DNS label: expected a-z, 0-9 and -", name)
		}
		if c == '-' && (i == 0 || i == len(name)-1) {
			return fmt.Errorf("the name %q is not a DNS label: expected no - at either end", name)
		}
	}
	return nil
}
