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
		fmt.Fprintln(os.Stderr, "TinyGo is required to build first-party plugins; install TinyGo 0.42.0+ or set TINYGO to its executable path")
		os.Exit(1)
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		panic(err)
	}
	cmd := exec.Command(tinygo, "build", "-target=wasm-unknown", "-tags=runpilot_wasm", "-scheduler=none", "-no-debug", "-o", "plugin.wasm", ".")
	cmd.Dir = filepath.Join(root, "plugins", "system", "backend")
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build System plugin:", err)
		os.Exit(1)
	}
}
