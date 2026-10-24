package core

import (
	"errors"
	"os/user"
	"testing"
)

func TestServiceIdentityFallsBackWithoutAccountDatabase(t *testing.T) {
	missing := func() (*user.User, error) { return nil, errors.New("user: unknown userid 1000") }
	env := func(name string) string {
		if name == "USER" {
			return "runpilot"
		}
		return ""
	}
	if name, uid := serviceIdentity(missing, func() int { return 1000 }, env); name != "runpilot" || uid != "1000" {
		t.Fatalf("fallback identity = %q/%q", name, uid)
	}
	if name, uid := serviceIdentity(missing, func() int { return 1000 }, func(string) string { return "" }); name != "" || uid != "1000" {
		t.Fatalf("UID-only identity = %q/%q", name, uid)
	}
	known := func() (*user.User, error) { return &user.User{Username: "admin", Uid: "501"}, nil }
	if name, uid := serviceIdentity(known, func() int { return 1000 }, env); name != "admin" || uid != "501" {
		t.Fatalf("resolved identity = %q/%q", name, uid)
	}
}
