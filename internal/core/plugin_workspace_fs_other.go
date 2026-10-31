//go:build !linux && !windows

package core

func renameNoReplace(int, string, int, string) (bool, error) { return false, nil }
