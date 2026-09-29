package plugins

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestReferenceSystemWASMMatchesSourceArtifact(t *testing.T) {
	wasm, err := os.ReadFile(filepath.Join("..", "..", "plugins", "system", "backend", "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wasm, referenceSystemWASM) {
		t.Fatal("embedded System WASM differs from source-derived artifact")
	}
}
