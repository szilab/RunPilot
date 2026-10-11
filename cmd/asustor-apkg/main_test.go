package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildAPKProducesDeterministicValidatedPackage(t *testing.T) {
	root := filepath.Join("..", "..", "packaging", "asustor")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "runpilot.apk")
	if err := buildAPK(root, binary, "1.2.3-test.1", out); err != nil {
		t.Fatal(err)
	}
	if err := validateAPK(out, "1.2.3-test.1"); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildAPK(root, binary, "1.2.3-test.1", out); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("APKG output is not reproducible")
	}
}

func TestValidateAPKRejectsInvalidArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.apk")
	if err := os.WriteFile(path, []byte("not an apk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateAPK(path, "1.2.3"); err == nil {
		t.Fatal("validateAPK accepted an invalid archive")
	}
}

func TestPackageVersionAcceptsASUSTORRevisionAndLegacyDevelopmentVersions(t *testing.T) {
	for _, version := range []string{
		"1.0.5.r121",
		"1.0.4-dev.60ece7b.5af087f.1",
		"1.2.3-test.1",
	} {
		if !validPackageVersion(version) {
			t.Errorf("valid package version %q rejected", version)
		}
	}
	for _, version := range []string{"1.0.5.r", "1.0.5.r-1", "1.0.5.121"} {
		if validPackageVersion(version) {
			t.Errorf("invalid package version %q accepted", version)
		}
	}
}

func TestBuildAPKStoresASUSTORRevisionVersion(t *testing.T) {
	root := filepath.Join("..", "..", "packaging", "asustor")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "runpilot.apk")
	if err := buildAPK(root, binary, "1.0.5.r121", out); err != nil {
		t.Fatal(err)
	}
	if err := validateAPK(out, "1.0.5.r121"); err != nil {
		t.Fatal(err)
	}
}
