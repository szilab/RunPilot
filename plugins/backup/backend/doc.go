// Package main is the first-party Backup plugin backend. It owns backup
// jobs, their schedules and the restic, rsync and robocopy command plans;
// RunPilot core only provides generic storage, scheduler, process, history,
// workspace and event capabilities.
package main

//go:generate go run generate.go
