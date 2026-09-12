package launcher

import (
	"strings"
	"testing"
)

func TestMergeEnvironment(t *testing.T) {
	inherited := []string{"PATH=system", "TEMP=temp", "UNCHANGED=value"}
	merged := mergeEnvironment(inherited, map[string]string{
		"PATH":   "custom",
		"CUSTOM": "",
	}, false)

	for _, want := range []string{"TEMP=temp", "UNCHANGED=value", "PATH=custom", "CUSTOM="} {
		if !containsEnvironment(merged, want) {
			t.Fatalf("merged environment missing %q: %v", want, merged)
		}
	}
	if containsEnvironment(merged, "PATH=system") {
		t.Fatalf("inherited PATH was not overridden: %v", merged)
	}
}

func TestMergeEnvironmentWindowsCaseInsensitiveOverride(t *testing.T) {
	merged := mergeEnvironment(
		[]string{"PATH=system", "Path=duplicate", "TEMP=temp"},
		map[string]string{"Path": "custom"},
		true,
	)

	var pathEntries []string
	for _, entry := range merged {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "path") {
			pathEntries = append(pathEntries, entry)
		}
	}
	if len(pathEntries) != 1 || pathEntries[0] != "Path=custom" {
		t.Fatalf("Windows PATH entries = %v, want [Path=custom]", pathEntries)
	}
	if !containsEnvironment(merged, "TEMP=temp") {
		t.Fatalf("unrelated inherited variable was lost: %v", merged)
	}
}

func containsEnvironment(environment []string, want string) bool {
	for _, entry := range environment {
		if entry == want {
			return true
		}
	}
	return false
}
