//go:build !windows

package storage

func filesystemRoots() []string { return []string{"/"} }
