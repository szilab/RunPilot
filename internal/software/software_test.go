package software

import (
	"path/filepath"
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

func TestEffectiveRootUsesDataDirectoryAndCustomRoot(t *testing.T) {
	data := t.TempDir()
	base := model.SoftwareProviderDefinition{ID: "scoop", Name: "RunPilot Scoop", Type: model.SoftwareProviderScoop, Scoop: &model.ScoopProviderSpec{}}
	got, fallback, err := EffectiveRoot(data, base)
	if err != nil || !fallback || got != filepath.Join(data, "software", "scoop") {
		t.Fatalf("default = %q, %v, %v", got, fallback, err)
	}
	base.Scoop.Root = filepath.Join(data, "other")
	got, fallback, err = EffectiveRoot(data, base)
	if err != nil || fallback || got != base.Scoop.Root {
		t.Fatalf("custom = %q, %v, %v", got, fallback, err)
	}
}
