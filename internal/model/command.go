package model

import (
	"fmt"
	"strings"
)

// ValidateCommand validates the command fields shared by managed processes and
// command jobs. It intentionally does not require a path: callers apply their
// own context-specific required-field validation.
func ValidateCommand(spec CommandSpec) error {
	return ValidateEnvironment(spec.Environment)
}

// ValidateEnvironment ensures configured variables can be represented
// unambiguously on Windows, where environment variable names are
// case-insensitive.
func ValidateEnvironment(environment map[string]string) error {
	seen := make(map[string]string, len(environment))
	for name := range environment {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("environment variable name is required")
		}
		if strings.Contains(name, "=") {
			return fmt.Errorf("environment variable name %q must not contain =", name)
		}
		canonical := strings.ToUpper(name)
		if previous, exists := seen[canonical]; exists {
			return fmt.Errorf("environment variable names %q and %q conflict on case-insensitive platforms", previous, name)
		}
		seen[canonical] = name
	}
	return nil
}
