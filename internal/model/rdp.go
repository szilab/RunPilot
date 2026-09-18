package model

import (
	"fmt"
	"net"
	"strings"
	"time"
)

func rdpBool(value bool) *bool { return &value }

var rdpLayouts = map[string]bool{"": true, "en-us-qwerty": true, "en-gb-qwerty": true, "de-de-qwertz": true, "de-ch-qwertz": true, "fr-fr-azerty": true, "fr-be-azerty": true, "fr-ch-qwertz": true, "hu-hu-qwertz": true, "it-it-qwerty": true, "es-es-qwerty": true, "es-latam-qwerty": true, "sv-se-qwerty": true, "no-no-qwerty": true, "tr-tr-qwerty": true, "pt-br-qwerty": true, "ja-jp-qwerty": true, "failsafe": true}

func DefaultGuacdConfig() GuacdConfig {
	return GuacdConfig{Host: "127.0.0.1", Port: 4822, ConnectTimeoutSeconds: 5}
}

func NormalizeGuacdConfig(input GuacdConfig) (GuacdConfig, error) {
	out := DefaultGuacdConfig()
	if strings.TrimSpace(input.Host) != "" {
		out.Host = strings.TrimSpace(input.Host)
	}
	if strings.Contains(out.Host, "://") || strings.ContainsAny(out.Host, "/?#") || (strings.Contains(out.Host, ":") && net.ParseIP(out.Host) == nil) {
		return GuacdConfig{}, fmt.Errorf("guacd host must be a hostname or IP address without a URL or port")
	}
	if input.Port != 0 {
		out.Port = input.Port
	}
	if out.Port < 1 || out.Port > 65535 {
		return GuacdConfig{}, fmt.Errorf("guacd port must be between 1 and 65535")
	}
	out.TLS = input.TLS
	if input.ConnectTimeoutSeconds != 0 {
		out.ConnectTimeoutSeconds = input.ConnectTimeoutSeconds
	}
	if out.ConnectTimeoutSeconds < 1 || out.ConnectTimeoutSeconds > 60 {
		return GuacdConfig{}, fmt.Errorf("guacd connect timeout must be between 1 and 60 seconds")
	}
	return out, nil
}

func DefaultRDPRemoteOptions() RDPRemoteOptions {
	return RDPRemoteOptions{Port: 3389, SecurityMode: RDPSecurityAutomatic, Clipboard: rdpBool(true), DynamicResize: rdpBool(true), ResizeMethod: "display-update", DPIMode: "auto", CertificatePolicy: RDPCertificateValidate, Copy: rdpBool(true), Paste: rdpBool(true), ClipboardNormalization: "preserve", PerformanceProfile: "balanced", TimeoutSeconds: 10}
}

// NormalizeRDPRemoteOptions preserves old target fields while validating the
// small, typed subset exposed by RunPilot's Guacamole RDP provider.
func NormalizeRDPRemoteOptions(input *RDPRemoteOptions) (RDPRemoteOptions, error) {
	out := DefaultRDPRemoteOptions()
	if input == nil {
		return RDPRemoteOptions{}, fmt.Errorf("RDP settings are required")
	}
	out.Host = strings.TrimSpace(input.Host)
	if out.Host == "" {
		return RDPRemoteOptions{}, fmt.Errorf("RDP host is required")
	}
	if strings.Contains(out.Host, "://") || strings.ContainsAny(out.Host, "/?#") || (strings.Contains(out.Host, ":") && net.ParseIP(out.Host) == nil) {
		return RDPRemoteOptions{}, fmt.Errorf("RDP host must not include a URL, path, or port")
	}
	if input.Port != 0 {
		out.Port = input.Port
	}
	if out.Port < 1 || out.Port > 65535 {
		return RDPRemoteOptions{}, fmt.Errorf("RDP port must be between 1 and 65535")
	}
	out.Username, out.Domain = strings.TrimSpace(input.Username), strings.TrimSpace(input.Domain)
	if input.SecurityMode != "" {
		out.SecurityMode = input.SecurityMode
	}
	if out.SecurityMode != RDPSecurityAutomatic && out.SecurityMode != RDPSecurityNLA && out.SecurityMode != RDPSecurityNLAExt && out.SecurityMode != RDPSecurityTLS && out.SecurityMode != RDPSecurityRDP {
		return RDPRemoteOptions{}, fmt.Errorf("invalid RDP security mode")
	}
	if input.Clipboard != nil {
		out.Clipboard = rdpBool(*input.Clipboard)
		out.Copy, out.Paste = rdpBool(*input.Clipboard), rdpBool(*input.Clipboard)
	}
	if input.DynamicResize != nil {
		out.DynamicResize = rdpBool(*input.DynamicResize)
		if !*input.DynamicResize {
			out.ResizeMethod = "fixed"
		}
	}
	if input.Copy != nil {
		out.Copy = rdpBool(*input.Copy)
	}
	if input.Paste != nil {
		out.Paste = rdpBool(*input.Paste)
	}
	if input.ServerLayout != "" {
		out.ServerLayout = input.ServerLayout
	}
	if !rdpLayouts[out.ServerLayout] {
		return RDPRemoteOptions{}, fmt.Errorf("unsupported RDP server keyboard layout")
	}
	if input.ResizeMethod != "" {
		out.ResizeMethod = input.ResizeMethod
	}
	if out.ResizeMethod != "display-update" && out.ResizeMethod != "reconnect" && out.ResizeMethod != "fixed" {
		return RDPRemoteOptions{}, fmt.Errorf("invalid RDP resize method")
	}
	if input.DPIMode != "" {
		out.DPIMode = input.DPIMode
	}
	if out.DPIMode != "auto" && out.DPIMode != "96" && out.DPIMode != "custom" {
		return RDPRemoteOptions{}, fmt.Errorf("invalid RDP DPI mode")
	}
	if out.DPIMode == "custom" {
		out.DPI = input.DPI
		if out.DPI < 72 || out.DPI > 240 {
			return RDPRemoteOptions{}, fmt.Errorf("RDP DPI must be between 72 and 240")
		}
	} else if out.DPIMode == "96" {
		out.DPI = 96
	}
	if input.ColorDepth != 0 {
		out.ColorDepth = input.ColorDepth
	}
	if out.ColorDepth != 0 && out.ColorDepth != 24 && out.ColorDepth != 16 && out.ColorDepth != 8 {
		return RDPRemoteOptions{}, fmt.Errorf("invalid RDP color depth")
	}
	if input.CertificatePolicy != "" {
		out.CertificatePolicy = input.CertificatePolicy
	}
	if out.CertificatePolicy != RDPCertificateValidate && out.CertificatePolicy != RDPCertificateTOFU && out.CertificatePolicy != RDPCertificateIgnore && out.CertificatePolicy != RDPCertificateFingerprint {
		return RDPRemoteOptions{}, fmt.Errorf("invalid RDP certificate policy")
	}
	out.CertificateFingerprint = strings.TrimSpace(input.CertificateFingerprint)
	if out.CertificatePolicy == RDPCertificateFingerprint && out.CertificateFingerprint == "" {
		return RDPRemoteOptions{}, fmt.Errorf("certificate fingerprint is required")
	}
	if input.ClipboardNormalization != "" {
		out.ClipboardNormalization = input.ClipboardNormalization
	}
	if out.ClipboardNormalization != "preserve" && out.ClipboardNormalization != "unix" && out.ClipboardNormalization != "windows" {
		return RDPRemoteOptions{}, fmt.Errorf("invalid clipboard normalization")
	}
	if input.PerformanceProfile != "" {
		out.PerformanceProfile = input.PerformanceProfile
	}
	if out.PerformanceProfile != "balanced" && out.PerformanceProfile != "quality" && out.PerformanceProfile != "low-bandwidth" && out.PerformanceProfile != "custom" {
		return RDPRemoteOptions{}, fmt.Errorf("invalid RDP performance profile")
	}
	if input.TimeoutSeconds != 0 {
		out.TimeoutSeconds = input.TimeoutSeconds
	}
	if out.TimeoutSeconds < 1 || out.TimeoutSeconds > 120 {
		return RDPRemoteOptions{}, fmt.Errorf("RDP timeout must be between 1 and 120 seconds")
	}
	out.TimeZone = strings.TrimSpace(input.TimeZone)
	if out.TimeZone != "" {
		if _, err := time.LoadLocation(out.TimeZone); err != nil {
			return RDPRemoteOptions{}, fmt.Errorf("invalid RDP timezone")
		}
	}
	return out, nil
}
