package daemon

import (
	"context"
	"strings"
	"testing"
)

func TestListenAddrBindAddressOverride(t *testing.T) {
	tests := []struct {
		name, bind, address, want string
		port                      int
	}{
		{name: "configured port", bind: "127.0.0.1:9070", address: "0.0.0.0", want: "0.0.0.0:9070"},
		{name: "port override", bind: "127.0.0.1:9070", address: "::", port: 38471, want: "[::]:38471"},
		{name: "no override", bind: "127.0.0.1:9070", want: "127.0.0.1:9070"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := listenAddr(tt.bind, 0, tt.port, tt.address)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("listenAddr() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestListenAddrRejectsInvalidBindAddress(t *testing.T) {
	for _, address := range []string{"example.com", "0.0.0.0:9070", " 0.0.0.0", ""} {
		if address == "" {
			continue
		}
		if _, err := listenAddr("127.0.0.1:9070", 0, 9070, address); err == nil {
			t.Errorf("listenAddr accepted bind address %q", address)
		}
	}
}

func TestRunRefusesPartialProxyAuthConfiguration(t *testing.T) {
	t.Setenv("RUNPILOT_TRUSTED_PROXIES", "192.0.2.5")
	t.Setenv("RUNPILOT_PROXY_AUTH_HEADER", "")
	err := Run(context.Background(), t.TempDir(), Options{Port: 1, BindAddress: "127.0.0.1"})
	if err == nil || !strings.Contains(err.Error(), "RUNPILOT_PROXY_AUTH_HEADER") {
		t.Fatalf("Run error = %v", err)
	}
}
