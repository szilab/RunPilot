package xpra

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStatusReportsMissingOrAvailableBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only provider behavior")
	}
	missing := New()
	missing.lookPath = func(string) (string, error) { return "", errors.New("missing") }
	if status := missing.Status(context.Background()); status.State != "not-installed" {
		t.Fatalf("missing state=%q", status.State)
	}
	available := New()
	available.lookPath = func(string) (string, error) { return "/usr/bin/xpra", nil }
	available.run = func(context.Context, string, ...string) ([]byte, error) { return []byte("6.3\n"), nil }
	if status := available.Status(context.Background()); status.State != "available" || status.Version != "6.3" {
		t.Fatalf("available=%#v", status)
	}
}
func TestReadinessTimeoutAndShellQuoting(t *testing.T) {
	if err := waitEndpoint(context.Background(), "127.0.0.1:1", time.Millisecond, nil); err == nil || !strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("readiness error=%v", err)
	}
	if got := shellQuote("it's safe"); got != "'it'\\''s safe'" {
		t.Fatalf("quote=%q", got)
	}
}
