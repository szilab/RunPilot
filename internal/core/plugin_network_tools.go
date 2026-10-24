package core

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/platform"
	"github.com/szilab/RunPilot/internal/plugins"
)

// Generic, stateless network tools for plugins: TCP reachability probes,
// IPv4 neighbor (ARP) lookups and Wake-on-LAN. They grant no authority beyond
// network.stream.* (plugins can already connect anywhere the service can).
const (
	maxProbeTargets      = 64
	maxConcurrentProbes  = 16
	defaultProbeTimeout  = 1500 * time.Millisecond
	maxProbeTimeout      = 5 * time.Second
	neighborResolveWait  = 2 * time.Second
	neighborRefreshWait  = 600 * time.Millisecond
	neighborRefreshProbe = 9 // UDP discard port; any datagram triggers ARP resolution.
	defaultWakePort      = 9
)

// decodeToolParams decodes strict JSON parameters as an invalid_argument failure.
func decodeToolParams(raw json.RawMessage, value any) error {
	if err := decodeNetworkParams(raw, value); err != nil {
		return networkInvalid("invalid parameters")
	}
	return nil
}

type probeTarget struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type probeResult struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	// State is "open" (connected), "closed" (the host refused the port),
	// "unreachable" (timeout or no route) or "unresolved" (name lookup failed).
	State               string `json:"state"`
	LatencyMilliseconds int64  `json:"latencyMilliseconds,omitempty"`
}

// NetworkProbe checks TCP reachability of up to 64 host:port pairs in
// parallel and returns one result per target, in request order.
func (h controllerPluginHost) NetworkProbe(ctx context.Context, _ string, raw json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Targets             []probeTarget `json:"targets"`
		TimeoutMilliseconds int           `json:"timeoutMilliseconds"`
	}
	if err := decodeToolParams(raw, &request); err != nil {
		return nil, err
	}
	if len(request.Targets) == 0 || len(request.Targets) > maxProbeTargets {
		return nil, networkInvalid("network.probe needs 1 to 64 targets")
	}
	timeout := defaultProbeTimeout
	if request.TimeoutMilliseconds != 0 {
		timeout = time.Duration(request.TimeoutMilliseconds) * time.Millisecond
		if timeout < 100*time.Millisecond || timeout > maxProbeTimeout {
			return nil, networkInvalid("timeoutMilliseconds must be between 100 and 5000")
		}
	}
	for _, target := range request.Targets {
		if !validNetworkHost(target.Host) || target.Port < 1 || target.Port > 65535 {
			return nil, networkInvalid("every probe target needs a valid host and port")
		}
	}
	results := make([]probeResult, len(request.Targets))
	limit := make(chan struct{}, maxConcurrentProbes)
	var wg sync.WaitGroup
	for i, target := range request.Targets {
		wg.Add(1)
		go func(i int, target probeTarget) {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			results[i] = probeTCP(ctx, target, timeout)
		}(i, target)
	}
	wg.Wait()
	return json.Marshal(map[string]any{"results": results})
}

func probeTCP(ctx context.Context, target probeTarget, timeout time.Duration) probeResult {
	result := probeResult{Host: target.Host, Port: target.Port}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	conn, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", net.JoinHostPort(target.Host, strconv.Itoa(target.Port)))
	if err == nil {
		_ = conn.Close()
		result.State = "open"
		result.LatencyMilliseconds = time.Since(started).Milliseconds()
		return result
	}
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr):
		result.State = "unresolved"
	case isConnectionRefused(err):
		result.State = "closed"
		result.LatencyMilliseconds = time.Since(started).Milliseconds()
	default:
		result.State = "unreachable"
	}
	return result
}

func isConnectionRefused(err error) bool {
	// syscall.ECONNREFUSED differs per platform; the error text is stable.
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "connection refused") || strings.Contains(text, "actively refused")
}

// NeighborLookup returns the MAC address the host has learned for a target
// on the local network. With refresh, it first sends one UDP datagram to
// the target so the operating system resolves the address.
func (h controllerPluginHost) NeighborLookup(ctx context.Context, _ string, raw json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Host    string `json:"host"`
		Refresh bool   `json:"refresh"`
	}
	if err := decodeToolParams(raw, &request); err != nil {
		return nil, err
	}
	if !validNetworkHost(request.Host) {
		return nil, networkInvalid("network.neighbor.lookup needs a valid host")
	}
	ip, err := resolveIPv4(ctx, request.Host)
	if err != nil {
		return nil, err
	}
	mac, err := platform.NeighborMAC(ip)
	if errors.Is(err, platform.ErrNeighborNotFound) && request.Refresh {
		refreshCtx, cancel := context.WithTimeout(ctx, neighborRefreshWait)
		_ = platform.SendUDP(refreshCtx, &net.UDPAddr{IP: ip, Port: neighborRefreshProbe}, nil)
		for refreshCtx.Err() == nil {
			if mac, err = platform.NeighborMAC(ip); !errors.Is(err, platform.ErrNeighborNotFound) {
				break
			}
			select {
			case <-refreshCtx.Done():
			case <-time.After(100 * time.Millisecond):
			}
		}
		cancel()
	}
	switch {
	case errors.Is(err, platform.ErrNeighborUnavailable):
		return nil, &plugins.HostFailure{Code: "unavailable", Message: err.Error()}
	case errors.Is(err, platform.ErrNeighborNotFound):
		return nil, &plugins.HostFailure{Code: "not_found", Message: "no MAC address is known for " + ip.String() + "; the host may be offline or outside the local network"}
	case err != nil:
		return nil, &plugins.HostFailure{Code: "failed", Message: "neighbor lookup failed"}
	}
	return json.Marshal(map[string]string{"ip": ip.String(), "mac": mac.String()})
}

func resolveIPv4(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() == nil {
			return nil, networkInvalid("neighbor lookup supports IPv4 addresses only")
		}
		return ip.To4(), nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, neighborResolveWait)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIP(lookupCtx, "ip4", host)
	if err != nil || len(addresses) == 0 {
		return nil, &plugins.HostFailure{Code: "not_found", Message: "could not resolve " + host + " to an IPv4 address"}
	}
	return addresses[0].To4(), nil
}

// NetworkWake sends a Wake-on-LAN magic packet. Without an explicit address
// it goes to 255.255.255.255 and to every local interface's directed
// broadcast address, so multi-homed hosts reach the right segment.
func (h controllerPluginHost) NetworkWake(ctx context.Context, _ string, raw json.RawMessage) (json.RawMessage, error) {
	var request struct {
		MAC     string `json:"mac"`
		Address string `json:"address"`
		Port    int    `json:"port"`
	}
	if err := decodeToolParams(raw, &request); err != nil {
		return nil, err
	}
	mac, err := net.ParseMAC(strings.TrimSpace(request.MAC))
	if err != nil || len(mac) != 6 {
		return nil, networkInvalid("network.wake needs a 6-byte MAC address")
	}
	port := request.Port
	if port == 0 {
		port = defaultWakePort
	}
	if port < 1 || port > 65535 {
		return nil, networkInvalid("network.wake port is invalid")
	}
	var destinations []net.IP
	if request.Address != "" {
		ip := net.ParseIP(request.Address).To4()
		if ip == nil {
			return nil, networkInvalid("network.wake address must be an IPv4 address")
		}
		destinations = []net.IP{ip}
	} else {
		destinations = append([]net.IP{net.IPv4bcast}, platform.InterfaceBroadcasts()...)
	}
	packet, err := platform.WakePacket(mac)
	if err != nil {
		return nil, networkInvalid(err.Error())
	}
	sendCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	sent := []string{}
	for _, ip := range destinations {
		if platform.SendUDP(sendCtx, &net.UDPAddr{IP: ip, Port: port}, packet) == nil {
			sent = append(sent, ip.String())
		}
	}
	if len(sent) == 0 {
		return nil, &plugins.HostFailure{Code: "failed", Message: "the Wake-on-LAN packet could not be sent on any network"}
	}
	return json.Marshal(map[string]any{"mac": mac.String(), "port": port, "sentTo": sent})
}
