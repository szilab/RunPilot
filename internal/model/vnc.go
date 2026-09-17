package model

import (
	"fmt"
	"net"
	"strings"
)

func DefaultVNCRemoteOptions() VNCRemoteOptions {
	return VNCRemoteOptions{Port: 5900, ConnectTimeoutSeconds: 10}
}

// NormalizeVNCRemoteOptions applies safe endpoint defaults and rejects URL-like
// values. A target is the only place the VNC destination is configured.
func NormalizeVNCRemoteOptions(input *VNCRemoteOptions) (VNCRemoteOptions, error) {
	out := DefaultVNCRemoteOptions()
	if input == nil {
		return VNCRemoteOptions{}, fmt.Errorf("VNC settings are required")
	}
	out.Host = strings.TrimSpace(input.Host)
	if out.Host == "" {
		return VNCRemoteOptions{}, fmt.Errorf("VNC host is required")
	}
	if strings.ContainsAny(out.Host, "/\\@") || strings.Contains(out.Host, "://") || strings.Contains(out.Host, ":") {
		return VNCRemoteOptions{}, fmt.Errorf("VNC host must be a hostname or IP address without a URL or port")
	}
	if net.ParseIP(out.Host) == nil && strings.ContainsAny(out.Host, " \t\r\n") {
		return VNCRemoteOptions{}, fmt.Errorf("VNC host must be a hostname or IP address without a URL or port")
	}
	if input.Port != 0 {
		out.Port = input.Port
	}
	if out.Port < 1 || out.Port > 65535 {
		return VNCRemoteOptions{}, fmt.Errorf("VNC port must be between 1 and 65535")
	}
	if input.ConnectTimeoutSeconds != 0 {
		out.ConnectTimeoutSeconds = input.ConnectTimeoutSeconds
	}
	if out.ConnectTimeoutSeconds < 1 || out.ConnectTimeoutSeconds > 120 {
		return VNCRemoteOptions{}, fmt.Errorf("VNC connect timeout must be between 1 and 120 seconds")
	}
	return out, nil
}
