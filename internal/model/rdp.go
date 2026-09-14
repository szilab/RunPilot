package model

import (
	"fmt"
	"net"
	"strings"
)

func rdpBool(value bool) *bool { return &value }

func DefaultRDPRemoteOptions() RDPRemoteOptions {
	return RDPRemoteOptions{
		Port:          3389,
		SecurityMode:  RDPSecurityAutomatic,
		Clipboard:     rdpBool(true),
		DynamicResize: rdpBool(false),
	}
}

// NormalizeRDPRemoteOptions applies the safe desktop defaults and validates
// only fields which may be persisted in runpilot.yaml.
func NormalizeRDPRemoteOptions(input *RDPRemoteOptions) (RDPRemoteOptions, error) {
	out := DefaultRDPRemoteOptions()
	if input == nil {
		return RDPRemoteOptions{}, fmt.Errorf("RDP settings are required")
	}
	out.Host = strings.TrimSpace(input.Host)
	if out.Host == "" {
		return RDPRemoteOptions{}, fmt.Errorf("RDP host is required")
	}
	if strings.Contains(out.Host, "://") || strings.ContainsAny(out.Host, "/?#") {
		return RDPRemoteOptions{}, fmt.Errorf("RDP host must not include a URL scheme or path")
	}
	if strings.Contains(out.Host, ":") && net.ParseIP(out.Host) == nil {
		return RDPRemoteOptions{}, fmt.Errorf("RDP host must not include a port")
	}
	if input.Port != 0 {
		out.Port = input.Port
	}
	if out.Port < 1 || out.Port > 65535 {
		return RDPRemoteOptions{}, fmt.Errorf("RDP port must be between 1 and 65535")
	}
	out.Username = strings.TrimSpace(input.Username)
	out.Domain = strings.TrimSpace(input.Domain)
	if input.SecurityMode != "" {
		out.SecurityMode = input.SecurityMode
	}
	if out.SecurityMode != RDPSecurityAutomatic && out.SecurityMode != RDPSecurityNLA && out.SecurityMode != RDPSecurityTLS {
		return RDPRemoteOptions{}, fmt.Errorf("RDP security mode must be automatic, nla, or tls")
	}
	if input.Clipboard != nil {
		out.Clipboard = rdpBool(*input.Clipboard)
	}
	if input.DynamicResize != nil {
		out.DynamicResize = rdpBool(*input.DynamicResize)
	}
	return out, nil
}
