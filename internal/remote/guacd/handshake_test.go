package guacd

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/model"
)

type handshakeCapture struct {
	instructions []Instruction
	err          error
}

func captureHandshake(conn net.Conn, args []string, handshakeCount int, readyArgs ...string) <-chan handshakeCapture {
	result := make(chan handshakeCapture, 1)
	go func() {
		defer conn.Close()
		reader := bufio.NewReader(conn)
		captured := make([]Instruction, 0, handshakeCount+2)
		for i := 0; i < handshakeCount+2; i++ {
			instruction, err := DecodeInstruction(reader)
			if err != nil {
				result <- handshakeCapture{err: err}
				return
			}
			captured = append(captured, instruction)
			if i == 0 {
				if instruction.Opcode != "select" || !reflect.DeepEqual(instruction.Args, []string{"rdp"}) {
					result <- handshakeCapture{err: fmt.Errorf("select=%#v", instruction)}
					return
				}
				if err := WriteInstruction(conn, "args", args...); err != nil {
					result <- handshakeCapture{err: err}
					return
				}
			}
		}
		if captured[len(captured)-1].Opcode != "connect" {
			result <- handshakeCapture{err: fmt.Errorf("last instruction=%#v", captured[len(captured)-1])}
			return
		}
		if err := WriteInstruction(conn, "ready", readyArgs...); err != nil {
			result <- handshakeCapture{err: err}
			return
		}
		result <- handshakeCapture{instructions: captured}
	}()
	return result
}

func handshakeOptions(t *testing.T) model.RDPRemoteOptions {
	t.Helper()
	options, err := model.NormalizeRDPRemoteOptions(&model.RDPRemoteOptions{
		Host:              "safe-target",
		Port:              3390,
		ServerLayout:      "hu-hu-qwertz",
		SecurityMode:      model.RDPSecurityNLA,
		CertificatePolicy: model.RDPCertificateIgnore,
		Copy:              boolPointer(false),
		ResizeMethod:      "display-update",
		TimeZone:          "Europe/Budapest",
	})
	if err != nil {
		t.Fatal(err)
	}
	return options
}

func handshakeClient() ClientInfo {
	return ClientInfo{
		Width:          1280,
		Height:         800,
		DPI:            96,
		TimeZone:       "America/New_York",
		Name:           "RunPilot",
		ImageMimetypes: []string{"image/png", "image/jpeg"},
	}
}

func TestConnectRDPMatchesConfiguredGuacamoleSocketHandshake(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	args := []string{"VERSION_1_5_0", "password", "hostname", "server-layout", "security", "port", "ignore-cert", "disable-copy", "resize-method", "timezone", "unknown-parameter"}
	result := captureHandshake(serverConn, args, 6, "connection")
	stages := []string{}
	if _, err := ConnectRDPWithStages(context.Background(), clientConn, handshakeOptions(t), Credentials{Password: "secret"}, handshakeClient(), func(stage string) { stages = append(stages, stage) }); err != nil {
		t.Fatal(err)
	}
	captured := <-result
	if captured.err != nil {
		t.Fatal(captured.err)
	}
	want := []Instruction{
		{Opcode: "select", Args: []string{"rdp"}},
		{Opcode: "size", Args: []string{"1280", "800", "96"}},
		{Opcode: "audio"},
		{Opcode: "video"},
		{Opcode: "image", Args: []string{"image/png", "image/jpeg"}},
		{Opcode: "timezone", Args: []string{"Europe/Budapest"}},
		{Opcode: "name", Args: []string{"RunPilot"}},
		{Opcode: "connect", Args: []string{"VERSION_1_5_0", "secret", "safe-target", "hu-hu-qwertz", "nla", "3390", "true", "true", "display-update", "Europe/Budapest", ""}},
	}
	if !equalInstructions(captured.instructions, want) {
		t.Fatalf("instructions=%#v want=%#v", captured.instructions, want)
	}
	wantStages := "sent select rdp,received args,negotiated protocol version: VERSION_1_5_0,sent size,sent audio,sent video,sent image,sent timezone,sent name,sent connect,received ready"
	if got := strings.Join(stages, ","); got != wantStages {
		t.Fatalf("stages=%q want=%q", got, wantStages)
	}
}

func equalInstructions(got, want []Instruction) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Opcode != want[i].Opcode || !slices.Equal(got[i].Args, want[i].Args) {
			return false
		}
	}
	return true
}

func TestConnectRDPProtocolVersionNegotiation(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantVersion string
		steps       int
	}{
		{name: "advertised supported version", args: []string{"VERSION_1_1_0", "hostname"}, wantVersion: "VERSION_1_1_0", steps: 5},
		{name: "legacy without version", args: []string{"hostname"}, wantVersion: "", steps: 4},
		{name: "newer server version", args: []string{"VERSION_9_0_0", "hostname"}, wantVersion: "VERSION_1_5_0", steps: 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			defer clientConn.Close()
			result := captureHandshake(serverConn, tc.args, tc.steps, "connection")
			client := handshakeClient()
			client.Name = ""
			if _, err := ConnectRDP(context.Background(), clientConn, handshakeOptions(t), Credentials{}, client); err != nil {
				t.Fatal(err)
			}
			captured := <-result
			if captured.err != nil {
				t.Fatal(captured.err)
			}
			connect := captured.instructions[len(captured.instructions)-1]
			if tc.wantVersion == "" {
				if !reflect.DeepEqual(connect.Args, []string{"safe-target"}) {
					t.Fatalf("legacy connect args=%q", connect.Args)
				}
				for _, instruction := range captured.instructions {
					if instruction.Opcode == "timezone" || instruction.Opcode == "name" {
						t.Fatalf("legacy sent unsupported %#v", instruction)
					}
				}
				return
			}
			if len(connect.Args) != 2 || connect.Args[0] != tc.wantVersion || connect.Args[1] != "safe-target" {
				t.Fatalf("connect args=%q", connect.Args)
			}
		})
	}
}

func TestCredentiallessConnectionOmitsTargetIdentity(t *testing.T) {
	options, err := model.NormalizeRDPRemoteOptions(&model.RDPRemoteOptions{Host: "target", Username: "stored", Domain: "domain"})
	if err != nil {
		t.Fatal(err)
	}
	values := connectionValues(options, Credentials{})
	if values["username"] != "" || values["password"] != "" || values["domain"] != "" {
		t.Fatalf("credentialless values=%#v", values)
	}
}

func TestEffectiveTimeZonePrefersTargetOverride(t *testing.T) {
	client := ClientInfo{TimeZone: "America/New_York"}
	if got := effectiveTimeZone(model.RDPRemoteOptions{TimeZone: "Europe/Budapest"}, client); got != "Europe/Budapest" {
		t.Fatalf("target timezone=%q", got)
	}
	if got := effectiveTimeZone(model.RDPRemoteOptions{}, client); got != "America/New_York" {
		t.Fatalf("browser timezone=%q", got)
	}
}

func TestCredentiallessConnectSendsEmptyIdentityArguments(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	result := captureHandshake(serverConn, []string{"username", "password", "domain", "hostname"}, 4, "connection")
	options, err := model.NormalizeRDPRemoteOptions(&model.RDPRemoteOptions{Host: "target", Username: "stored-user", Domain: "stored-domain"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ConnectRDP(context.Background(), clientConn, options, Credentials{}, ClientInfo{}); err != nil {
		t.Fatal(err)
	}
	select {
	case captured := <-result:
		if captured.err != nil {
			t.Fatal(captured.err)
		}
		got := captured.instructions[len(captured.instructions)-1]
		want := []string{"", "", "", "target"}
		if got.Opcode != "connect" || !reflect.DeepEqual(got.Args, want) {
			t.Fatalf("connect=%#v want=%q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("guacd did not receive connect")
	}
}

func TestConnectRDPRejectsReadyWithoutConnectionID(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	result := captureHandshake(serverConn, []string{"hostname"}, 4)
	_, err := ConnectRDP(context.Background(), clientConn, handshakeOptions(t), Credentials{}, ClientInfo{})
	if err == nil || !strings.Contains(err.Error(), "connection ID") {
		t.Fatalf("err=%v", err)
	}
	if captured := <-result; captured.err != nil {
		t.Fatal(captured.err)
	}
}

func boolPointer(value bool) *bool { return &value }
