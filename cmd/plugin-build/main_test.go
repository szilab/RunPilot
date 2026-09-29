package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildPackageIsDeterministicAndValidatesAssets(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "apiVersion: runpilot.plugin/v1\nid: test.builder\nname: Builder\nversion: 1\nrequires:\n  runpilotApi: 1\nfrontend:\n  module: web/plugin.js\n"
	if err := os.WriteFile(filepath.Join(source, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "web", "plugin.js"), []byte("export function activate() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(t.TempDir(), "first.rpplugin"), filepath.Join(t.TempDir(), "second.rpplugin")
	if err := buildPackage(source, first); err != nil {
		t.Fatalf("first package: %v", err)
	}
	if err := buildPackage(source, second); err != nil {
		t.Fatalf("second package: %v", err)
	}
	a, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("package output is not deterministic")
	}
	if err := os.Remove(filepath.Join(source, "web", "plugin.js")); err != nil {
		t.Fatal(err)
	}
	if err := buildPackage(source, filepath.Join(t.TempDir(), "missing.rpplugin")); err == nil {
		t.Fatal("missing declared frontend asset accepted")
	}
}
