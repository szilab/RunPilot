package platform

import (
	"context"
	"errors"
	"net"
	"syscall"
)

// ErrNeighborUnavailable reports that this platform cannot read its
// IPv4 neighbor (ARP) table.
var ErrNeighborUnavailable = errors.New("neighbor table lookup is not supported on this platform")

// ErrNeighborNotFound reports that the neighbor table has no hardware
// address for the requested IPv4 address.
var ErrNeighborNotFound = errors.New("no neighbor table entry")

// WakePacket builds a Wake-on-LAN magic packet: six 0xFF bytes followed by
// the 6-byte hardware address repeated sixteen times.
func WakePacket(mac net.HardwareAddr) ([]byte, error) {
	if len(mac) != 6 {
		return nil, errors.New("Wake-on-LAN needs a 6-byte MAC address")
	}
	packet := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		packet = append(packet, 0xff)
	}
	for i := 0; i < 16; i++ {
		packet = append(packet, mac...)
	}
	return packet, nil
}

// SendUDP sends one datagram from an ephemeral port with broadcast enabled,
// so the destination may be a limited or directed broadcast address.
func SendUDP(ctx context.Context, destination *net.UDPAddr, payload []byte) error {
	config := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		var setErr error
		if err := raw.Control(func(fd uintptr) { setErr = enableBroadcast(fd) }); err != nil {
			return err
		}
		return setErr
	}}
	conn, err := config.ListenPacket(ctx, "udp4", ":0")
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
	}
	_, err = conn.WriteTo(payload, destination)
	return err
}

// InterfaceBroadcasts returns the directed IPv4 broadcast address of every
// up, non-loopback interface that supports broadcast.
func InterfaceBroadcasts() []net.IP {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.IP
	seen := map[string]bool{}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			network, ok := address.(*net.IPNet)
			if !ok {
				continue
			}
			ip := network.IP.To4()
			mask := network.Mask
			if ip == nil || len(mask) != net.IPv4len {
				continue
			}
			broadcast := make(net.IP, net.IPv4len)
			for i := range ip {
				broadcast[i] = ip[i] | ^mask[i]
			}
			if !seen[broadcast.String()] {
				seen[broadcast.String()] = true
				out = append(out, broadcast)
			}
		}
	}
	return out
}

// NeighborMAC returns the hardware address the operating system has learned
// for an IPv4 neighbor. It does not send any traffic.
func NeighborMAC(ip net.IP) (net.HardwareAddr, error) {
	ip = ip.To4()
	if ip == nil {
		return nil, errors.New("neighbor lookup needs an IPv4 address")
	}
	return neighborMAC(ip)
}
