//go:build ignore

// Compile the System reference backend with TinyGo. TinyGo is intentionally a
// build-time dependency only; RunPilot loads the resulting standard WASM file.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	tinygo := os.Getenv("TINYGO")
	if tinygo == "" {
		tinygo = "tinygo"
	}
	if _, err := exec.LookPath(tinygo); err != nil {
		fmt.Fprintln(os.Stderr, "TinyGo 0.38.0 is required to build first-party plugins; install it or set TINYGO to its executable path")
		os.Exit(1)
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		panic(err)
	}
	cmd := exec.Command(tinygo, "build", "-target=wasm-unknown", "-tags=runpilot_wasm", "-scheduler=none", "-gc=conservative", "-no-debug", "-o", "plugin.wasm", ".")
	cmd.Dir = filepath.Join(root, "plugins", "system", "backend")
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build System plugin:", err)
		os.Exit(1)
	}
	generatedRoot := filepath.Join(root, "internal", "plugins")
	for _, artifact := range []struct{ source, destination string }{
		{"plugin.wasm", filepath.Join(generatedRoot, "system_reference.wasm")},
		{"../plugin.yaml", filepath.Join(generatedRoot, "system_reference", "plugin.yaml")},
		{"../web/plugin.js", filepath.Join(generatedRoot, "system_reference", "web", "plugin.js")},
		{"../web/plugin.css", filepath.Join(generatedRoot, "system_reference", "web", "plugin.css")},
	} {
		if err := copyGenerated(filepath.Join(root, "plugins", "system", "backend", artifact.source), artifact.destination); err != nil {
			fmt.Fprintln(os.Stderr, "update embedded System artifact:", err)
			os.Exit(1)
		}
	}
}

func copyGenerated(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o644)
}
