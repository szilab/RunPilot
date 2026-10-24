package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/plugins"
)

func hostFailureCode(err error) string {
	var failure *plugins.HostFailure
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}

func TestNetworkProbeReportsPortStates(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	openPort := listener.Addr().(*net.TCPAddr).Port
	closedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closedListener.Addr().(*net.TCPAddr).Port
	_ = closedListener.Close()

	host := controllerPluginHost{}
	params := `{"targets":[{"host":"127.0.0.1","port":` + strconv.Itoa(openPort) + `},{"host":"127.0.0.1","port":` + strconv.Itoa(closedPort) + `},{"host":"runpilot-probe.invalid","port":80}],"timeoutMilliseconds":1000}`
	raw, err := host.NetworkProbe(context.Background(), "remote", json.RawMessage(params))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Results []probeResult `json:"results"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, result := range response.Results {
		got = append(got, result.State)
	}
	if strings.Join(got, ",") != "open,closed,unresolved" {
		t.Fatalf("states = %v", got)
	}
	if response.Results[0].Port != openPort {
		t.Fatal("results are not in request order")
	}

	many := make([]string, 65)
	for i := range many {
		many[i] = `{"host":"127.0.0.1","port":1}`
	}
	for _, invalid := range []string{
		`{"targets":[]}`,
		`{"targets":[` + strings.Join(many, ",") + `]}`,
		`{"targets":[{"host":"127.0.0.1","port":0}]}`,
		`{"targets":[{"host":"bad host","port":80}]}`,
		`{"targets":[{"host":"127.0.0.1","port":80}],"timeoutMilliseconds":9000}`,
		`{"targets":[{"host":"127.0.0.1","port":80}],"extra":true}`,
	} {
		if _, err := host.NetworkProbe(context.Background(), "remote", json.RawMessage(invalid)); hostFailureCode(err) != "invalid_argument" {
			t.Errorf("%s: error = %v", invalid, err)
		}
	}
}

func TestNetworkWakeSendsMagicPacket(t *testing.T) {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	port := conn.LocalAddr().(*net.UDPAddr).Port
	host := controllerPluginHost{}
	raw, err := host.NetworkWake(context.Background(), "remote", json.RawMessage(`{"mac":"AA-BB-CC-DD-EE-FF","address":"127.0.0.1","port":`+strconv.Itoa(port)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"mac":"aa:bb:cc:dd:ee:ff"`) || !strings.Contains(string(raw), `"sentTo":["127.0.0.1"]`) {
		t.Fatalf("response = %s", raw)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	packet := make([]byte, 200)
	n, _, err := conn.ReadFrom(packet)
	if err != nil {
		t.Fatal(err)
	}
	want := append(bytes.Repeat([]byte{0xff}, 6), bytes.Repeat([]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}, 16)...)
	if !bytes.Equal(packet[:n], want) {
		t.Fatalf("magic packet = % x", packet[:n])
	}
	for _, invalid := range []string{`{"mac":"aa:bb:cc"}`, `{"mac":"00:00:5e:00:53:01:00:00"}`, `{"mac":"aa:bb:cc:dd:ee:ff","address":"::1"}`, `{"mac":"aa:bb:cc:dd:ee:ff","port":70000}`} {
		if _, err := host.NetworkWake(context.Background(), "remote", json.RawMessage(invalid)); hostFailureCode(err) != "invalid_argument" {
			t.Errorf("%s: error = %v", invalid, err)
		}
	}
}

func TestNeighborLookupValidatesAndReportsMissingEntries(t *testing.T) {
	host := controllerPluginHost{}
	for _, invalid := range []string{`{"host":""}`, `{"host":"::1"}`, `{"host":"bad host"}`} {
		if _, err := host.NeighborLookup(context.Background(), "remote", json.RawMessage(invalid)); hostFailureCode(err) != "invalid_argument" {
			t.Errorf("%s: error = %v", invalid, err)
		}
	}
	// Loopback never has a neighbor-table entry.
	_, err := host.NeighborLookup(context.Background(), "remote", json.RawMessage(`{"host":"127.0.0.1"}`))
	if code := hostFailureCode(err); code != "not_found" && code != "unavailable" {
		t.Fatalf("loopback lookup error = %v", err)
	}
}
