//go:build linux

package platform

import (
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func enableBroadcast(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
}

func neighborMAC(ip net.IP) (net.HardwareAddr, error) {
	table, err := os.ReadFile("/proc/net/arp")
	if err != nil {
		return nil, err
	}
	return parseProcNetARP(string(table), ip)
}

// parseProcNetARP reads /proc/net/arp:
//
//	IP address  HW type  Flags  HW address  Mask  Device
//
// Only complete entries (ATF_COM, 0x2) with a non-zero address count.
func parseProcNetARP(table string, ip net.IP) (net.HardwareAddr, error) {
	for _, line := range strings.Split(table, "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 || !net.ParseIP(fields[0]).Equal(ip) {
			continue
		}
		flags, err := strconv.ParseUint(strings.TrimPrefix(fields[2], "0x"), 16, 32)
		if err != nil || flags&0x2 == 0 {
			continue
		}
		mac, err := net.ParseMAC(fields[3])
		if err != nil || len(mac) != 6 || mac.String() == "00:00:00:00:00:00" {
			continue
		}
		return mac, nil
	}
	return nil, ErrNeighborNotFound
}
