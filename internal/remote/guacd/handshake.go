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
	Name               string
	AudioMimetypes     []string
	VideoMimetypes     []string
	ImageMimetypes     []string
}

// protocolVersion mirrors GuacamoleProtocolVersion in guacamole-common 1.6.0.
// VERSION_1_5_0 is the newest protocol version implemented by that release,
// despite the product release being numbered 1.6.0.
type protocolVersion struct{ major, minor, patch int }

var (
	legacyProtocolVersion = protocolVersion{major: 1, minor: 0, patch: 0}
	latestProtocolVersion = protocolVersion{major: 1, minor: 5, patch: 0}
)

func (v protocolVersion) String() string {
	return fmt.Sprintf("VERSION_%d_%d_%d", v.major, v.minor, v.patch)
}

func (v protocolVersion) atLeast(other protocolVersion) bool {
	if v.major != other.major {
		return v.major > other.major
	}
	if v.minor != other.minor {
		return v.minor > other.minor
	}
	return v.patch >= other.patch
}

func parseProtocolVersion(value string) (protocolVersion, bool) {
	const prefix = "VERSION_"
	if !strings.HasPrefix(value, prefix) {
		return protocolVersion{}, false
	}
	parts := strings.Split(strings.TrimPrefix(value, prefix), "_")
	if len(parts) != 3 {
		return protocolVersion{}, false
	}
	version := protocolVersion{}
	values := []*int{&version.major, &version.minor, &version.patch}
	for i, part := range parts {
		if part == "" {
			return protocolVersion{}, false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return protocolVersion{}, false
			}
		}
		parsed, err := strconv.Atoi(part)
		if err != nil {
			return protocolVersion{}, false
		}
		*values[i] = parsed
	}
	return version, true
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
	values := connectionValues(options, credentials)
	connect := make([]string, len(args.Args))
	negotiatedVersion := legacyProtocolVersion
	for i, name := range args.Args {
		if i == 0 {
			if version, ok := parseProtocolVersion(name); ok {
				// This exactly follows ConfiguredGuacamoleSocket: guacd provides
				// its maximum supported version and the client caps it at its own.
				if version.atLeast(latestProtocolVersion) {
					version = latestProtocolVersion
				}
				connect[i] = version.String()
				negotiatedVersion = version
				stageIf(stage, "negotiated protocol version: "+connect[i])
				continue
			}
		}
		connect[i] = values[name]
	}
	if err := WriteInstruction(conn, "size", strconv.Itoa(client.Width), strconv.Itoa(client.Height), strconv.Itoa(client.DPI)); err != nil {
		return nil, fmt.Errorf("send size: %w", err)
	}
	stageIf(stage, "sent size")
	if err := WriteInstruction(conn, "audio", client.AudioMimetypes...); err != nil {
		return nil, fmt.Errorf("send audio: %w", err)
	}
	stageIf(stage, "sent audio")
	if err := WriteInstruction(conn, "video", client.VideoMimetypes...); err != nil {
		return nil, fmt.Errorf("send video: %w", err)
	}
	stageIf(stage, "sent video")
	if err := WriteInstruction(conn, "image", client.ImageMimetypes...); err != nil {
		return nil, fmt.Errorf("send image: %w", err)
	}
	stageIf(stage, "sent image")
	if negotiatedVersion.atLeast(protocolVersion{major: 1, minor: 1, patch: 0}) {
		timezone := effectiveTimeZone(options, client)
		if timezone != "" {
			if err := WriteInstruction(conn, "timezone", timezone); err != nil {
				return nil, fmt.Errorf("send timezone: %w", err)
			}
			stageIf(stage, "sent timezone")
		}
	}
	if negotiatedVersion.atLeast(protocolVersion{major: 1, minor: 5, patch: 0}) && client.Name != "" {
		if err := WriteInstruction(conn, "name", client.Name); err != nil {
			return nil, fmt.Errorf("send name: %w", err)
		}
		stageIf(stage, "sent name")
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
	if len(ready.Args) == 0 || ready.Args[0] == "" {
		return nil, fmt.Errorf("guacd ready instruction did not include a connection ID")
	}
	stageIf(stage, "received ready")
	return reader, nil
}

func stageIf(stage func(string), value string) {
	if stage != nil {
		stage(value)
	}
}

func effectiveTimeZone(options model.RDPRemoteOptions, client ClientInfo) string {
	if options.TimeZone != "" {
		return options.TimeZone
	}
	return client.TimeZone
}

func connectionValues(options model.RDPRemoteOptions, credentials Credentials) map[string]string {
	value := map[string]string{"hostname": options.Host, "port": strconv.Itoa(options.Port), "username": credentials.Username, "password": credentials.Password, "domain": credentials.Domain, "security": mapSecurity(options.SecurityMode), "timeout": strconv.Itoa(options.TimeoutSeconds), "enable-drive": "false", "enable-printing": "false", "disable-audio": "true", "disable-copy": strconv.FormatBool(options.Copy == nil || !*options.Copy), "disable-paste": strconv.FormatBool(options.Paste == nil || !*options.Paste), "normalize-clipboard": options.ClipboardNormalization}
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
		// This remains an RDP connection parameter when guacd explicitly
		// includes "timezone" in its args list. Browser timezone is sent only
		// through the capability-gated timezone handshake above.
		value["timezone"] = options.TimeZone
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
