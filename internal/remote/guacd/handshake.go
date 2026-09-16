package guacd

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/szilab/RunPilot/internal/model"
)

type Credentials struct{ Username, Domain, Password string }
type ClientInfo struct {
	Width, Height, DPI int
	TimeZone           string
	ImageMimetypes     []string
}

// ConnectRDP uses guacd's returned args list instead of assuming a positional
// RDP parameter order. The caller owns conn after a successful handshake.
func ConnectRDP(ctx context.Context, conn net.Conn, options model.RDPRemoteOptions, credentials Credentials, client ClientInfo) (*bufio.Reader, error) {
	return ConnectRDPWithStages(ctx, conn, options, credentials, client, nil)
}

// ConnectRDPWithStages reports only fixed protocol stages; it never exposes
// connection argument values and is therefore safe for diagnostics.
func ConnectRDPWithStages(ctx context.Context, conn net.Conn, options model.RDPRemoteOptions, credentials Credentials, client ClientInfo, stage func(string)) (*bufio.Reader, error) {
	if err := WriteInstruction(conn, "select", "rdp"); err != nil {
		return nil, fmt.Errorf("send select rdp: %w", err)
	}
	stageIf(stage, "sent select rdp")
	reader := bufio.NewReader(conn)
	args, err := DecodeInstruction(reader)
	if err != nil {
		return nil, fmt.Errorf("receive args: %w", err)
	}
	if args.Opcode != "args" {
		return nil, fmt.Errorf("guacd did not accept RDP: %s", args.Opcode)
	}
	stageIf(stage, "received args")
	values := connectionValues(options, credentials, client)
	connect := make([]string, len(args.Args))
	for i, name := range args.Args {
		connect[i] = values[name]
	}
	if err := WriteInstruction(conn, "connect", connect...); err != nil {
		return nil, fmt.Errorf("send connect: %w", err)
	}
	stageIf(stage, "sent connect")
	ready, err := DecodeInstruction(reader)
	if err != nil {
		return nil, fmt.Errorf("receive ready: %w", err)
	}
	if ready.Opcode != "ready" {
		return nil, fmt.Errorf("guacd RDP connection failed: %s", ready.Opcode)
	}
	stageIf(stage, "received ready")
	return reader, nil
}

func stageIf(stage func(string), value string) {
	if stage != nil {
		stage(value)
	}
}

func connectionValues(options model.RDPRemoteOptions, credentials Credentials, client ClientInfo) map[string]string {
	value := map[string]string{"hostname": options.Host, "port": strconv.Itoa(options.Port), "username": credentials.Username, "password": credentials.Password, "domain": credentials.Domain, "security": mapSecurity(options.SecurityMode), "timeout": strconv.Itoa(options.TimeoutSeconds), "enable-drive": "false", "enable-printing": "false", "disable-audio": "true", "disable-copy": strconv.FormatBool(options.Copy == nil || !*options.Copy), "disable-paste": strconv.FormatBool(options.Paste == nil || !*options.Paste), "normalize-clipboard": options.ClipboardNormalization}
	if client.Width > 0 {
		value["width"] = strconv.Itoa(client.Width)
	}
	if client.Height > 0 {
		value["height"] = strconv.Itoa(client.Height)
	}
	if client.DPI > 0 {
		value["dpi"] = strconv.Itoa(client.DPI)
	}
	// Only advertise image formats the embedded Guacamole browser client can
	// render. Audio is deliberately disabled for this provider.
	if len(client.ImageMimetypes) > 0 {
		value["image"] = strings.Join(client.ImageMimetypes, ",")
	}
	if options.ServerLayout != "" {
		value["server-layout"] = options.ServerLayout
	}
	if options.ResizeMethod != "fixed" {
		value["resize-method"] = options.ResizeMethod
	}
	if options.ColorDepth != 0 {
		value["color-depth"] = strconv.Itoa(options.ColorDepth)
	}
	if options.TimeZone != "" {
		value["timezone"] = options.TimeZone
	} else {
		value["timezone"] = client.TimeZone
	}
	switch options.CertificatePolicy {
	case model.RDPCertificateTOFU:
		value["cert-tofu"] = "true"
	case model.RDPCertificateIgnore:
		value["ignore-cert"] = "true"
	case model.RDPCertificateFingerprint:
		value["cert-fingerprints"] = options.CertificateFingerprint
	}
	wallpaper, theming, smoothing, drag, composition, animations := "false", "false", "true", "false", "false", "false"
	if options.PerformanceProfile == "quality" {
		wallpaper, theming, drag, composition, animations = "true", "true", "true", "true", "true"
	}
	if options.PerformanceProfile == "low-bandwidth" {
		smoothing = "false"
	}
	value["enable-wallpaper"], value["enable-theming"], value["enable-font-smoothing"], value["enable-full-window-drag"], value["enable-desktop-composition"], value["enable-menu-animations"] = wallpaper, theming, smoothing, drag, composition, animations
	return value
}

func mapSecurity(mode model.RDPSecurityMode) string {
	if mode == model.RDPSecurityAutomatic {
		return "any"
	}
	return string(mode)
}
