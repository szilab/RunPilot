//go:build windows

package platform

import (
	"encoding/binary"
	"net"
	"syscall"
	"unsafe"
)

var sendARP = syscall.NewLazyDLL("iphlpapi.dll").NewProc("SendARP")

func enableBroadcast(fd uintptr) error {
	return syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
}

// neighborMAC uses SendARP, which answers from the ARP cache or resolves the
// address on the local subnet.
func neighborMAC(ip net.IP) (net.HardwareAddr, error) {
	if err := sendARP.Find(); err != nil {
		return nil, ErrNeighborUnavailable
	}
	var buffer [8]byte
	length := uint32(len(buffer))
	destination := binary.LittleEndian.Uint32(ip.To4())
	result, _, _ := sendARP.Call(uintptr(destination), 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&length)))
	if result != 0 || length < 6 {
		return nil, ErrNeighborNotFound
	}
	mac := net.HardwareAddr(append([]byte(nil), buffer[:6]...))
	if mac.String() == "00:00:00:00:00:00" {
		return nil, ErrNeighborNotFound
	}
	return mac, nil
}
