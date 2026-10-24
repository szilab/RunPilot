//go:build linux

package platform

import (
	"net"
	"testing"
)

func TestParseProcNetARP(t *testing.T) {
	table := `IP address       HW type     Flags       HW address            Mask     Device
10.0.0.20        0x1         0x2         3c:7c:3f:12:34:56     *        eth0
10.0.0.30        0x1         0x0         00:00:00:00:00:00     *        eth0
10.0.0.40        0x1         0x6         aa:bb:cc:dd:ee:01     *        eth0
`
	if mac, err := parseProcNetARP(table, net.ParseIP("10.0.0.20").To4()); err != nil || mac.String() != "3c:7c:3f:12:34:56" {
		t.Fatalf("complete entry = %v, %v", mac, err)
	}
	if mac, err := parseProcNetARP(table, net.ParseIP("10.0.0.40").To4()); err != nil || mac.String() != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("permanent entry = %v, %v", mac, err)
	}
	for _, ip := range []string{"10.0.0.30", "10.0.0.99"} {
		if _, err := parseProcNetARP(table, net.ParseIP(ip).To4()); err != ErrNeighborNotFound {
			t.Errorf("%s: error = %v", ip, err)
		}
	}
}

func TestWakePacket(t *testing.T) {
	mac, _ := net.ParseMAC("01:23:45:67:89:ab")
	packet, err := WakePacket(mac)
	if err != nil || len(packet) != 102 || packet[0] != 0xff || packet[5] != 0xff || packet[6] != 0x01 || packet[101] != 0xab {
		t.Fatalf("packet = % x, %v", packet, err)
	}
	if _, err := WakePacket(net.HardwareAddr{1, 2, 3}); err == nil {
		t.Fatal("short MAC accepted")
	}
}
