package guacd

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/model"
)

func TestConnectRDPUsesGuacdArgumentOrderAndSnapshot(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	connect := make(chan Instruction, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		selectInstruction, _ := DecodeInstruction(reader)
		if selectInstruction.Opcode != "select" || len(selectInstruction.Args) != 1 || selectInstruction.Args[0] != "rdp" {
			return
		}
		_ = WriteInstruction(conn, "args", "password", "hostname", "server-layout", "security", "port", "ignore-cert", "disable-copy", "resize-method")
		instruction, _ := DecodeInstruction(reader)
		connect <- instruction
		_ = WriteInstruction(conn, "ready", "connection")
	}()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	options, err := model.NormalizeRDPRemoteOptions(&model.RDPRemoteOptions{Host: "safe-target", Port: 3390, ServerLayout: "hu-hu-qwertz", SecurityMode: model.RDPSecurityNLA, CertificatePolicy: model.RDPCertificateIgnore, Copy: boolPointer(false), ResizeMethod: "display-update"})
	if err != nil {
		t.Fatal(err)
	}
	stages := []string{}
	if _, err := ConnectRDPWithStages(context.Background(), conn, options, Credentials{Password: "secret"}, ClientInfo{Width: 1280, Height: 800, DPI: 96}, func(stage string) { stages = append(stages, stage) }); err != nil {
		t.Fatal(err)
	}
	got := <-connect
	if got.Opcode != "connect" {
		t.Fatalf("opcode=%q", got.Opcode)
	}
	want := []string{"secret", "safe-target", "hu-hu-qwertz", "nla", "3390", "true", "true", "display-update"}
	for i := range want {
		if got.Args[i] != want[i] {
			t.Fatalf("arg %d=%q want %q", i, got.Args[i], want[i])
		}
	}
	if got := strings.Join(stages, ","); got != "sent select rdp,received args,sent connect,received ready" {
		t.Fatalf("stages=%q", got)
	}
}

func TestCredentiallessConnectionOmitsTargetIdentity(t *testing.T) {
	options, err := model.NormalizeRDPRemoteOptions(&model.RDPRemoteOptions{Host: "target", Username: "stored", Domain: "domain"})
	if err != nil {
		t.Fatal(err)
	}
	values := connectionValues(options, Credentials{}, ClientInfo{})
	if values["username"] != "" || values["password"] != "" || values["domain"] != "" {
		t.Fatalf("credentialless values=%#v", values)
	}
}

func boolPointer(value bool) *bool { return &value }

func TestCredentiallessConnectSendsEmptyIdentityArguments(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	connect := make(chan Instruction, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		if selectInstruction, err := DecodeInstruction(reader); err != nil || selectInstruction.Opcode != "select" {
			return
		}
		_ = WriteInstruction(conn, "args", "username", "password", "domain", "hostname")
		instruction, err := DecodeInstruction(reader)
		if err != nil {
			return
		}
		connect <- instruction
		_ = WriteInstruction(conn, "ready", "connection")
	}()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	options, err := model.NormalizeRDPRemoteOptions(&model.RDPRemoteOptions{Host: "target", Username: "stored-user", Domain: "stored-domain"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ConnectRDP(context.Background(), conn, options, Credentials{}, ClientInfo{}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-connect:
		want := []string{"", "", "", "target"}
		if got.Opcode != "connect" || len(got.Args) != len(want) {
			t.Fatalf("unexpected connect instruction")
		}
		for i := range want {
			if got.Args[i] != want[i] {
				t.Fatalf("arg %d=%q want %q", i, got.Args[i], want[i])
			}
		}
	case <-time.After(time.Second):
		t.Fatal("guacd did not receive connect")
	}
}
