//go:build ignore

// Compile the Tasks backend with TinyGo. TinyGo is a build-time dependency
// only; RunPilot loads the resulting standard WASM file.
package main

import (
	"fmt"
	"os"
	"os/exec"
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
	// -gc=conservative is required: TinyGo's default wasm-unknown collector
	// never frees memory. -stack-size leaves headroom for encoding/json.
	cmd := exec.Command(tinygo, "build", "-target=wasm-unknown", "-tags=runpilot_wasm", "-scheduler=none", "-gc=conservative", "-stack-size=64kb", "-no-debug", "-o", "plugin.wasm", ".")
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build Tasks plugin:", err)
		os.Exit(1)
	}
}
