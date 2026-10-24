//go:build !linux && !windows

package platform

import (
	"net"
	"syscall"
)

func enableBroadcast(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
}

func neighborMAC(net.IP) (net.HardwareAddr, error) { return nil, ErrNeighborUnavailable }
