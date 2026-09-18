package guacd

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestProtocolCodec(t *testing.T) {
	data, err := EncodeInstruction("select", "rdp")
	if err != nil || string(data) != "6.select,3.rdp;" {
		t.Fatalf("encode=%q %v", data, err)
	}
	data, _ = EncodeInstruction("args", "hostname", "", "ő")
	got, err := DecodeInstruction(bufio.NewReader(bytes.NewReader(data)))
	if err != nil || got.Opcode != "args" || len(got.Args) != 3 || got.Args[2] != "ő" {
		t.Fatalf("decode=%#v %v", got, err)
	}
	reader := bufio.NewReader(strings.NewReader("4.args,4.port;5.ready,1.x;"))
	first, _ := DecodeInstruction(reader)
	second, _ := DecodeInstruction(reader)
	if first.Args[0] != "port" || second.Opcode != "ready" {
		t.Fatal("sequential decode failed")
	}
}
func TestProtocolCodecRejectsBadInput(t *testing.T) {
	for _, value := range []string{"x.foo;", "3.foo!", "5.foo;", "9999999.x;"} {
		if _, err := DecodeInstruction(bufio.NewReader(strings.NewReader(value))); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestTunnelReadyAndClientForwarding(t *testing.T) {
	ready, err := TunnelReady("session-1")
	if err != nil || string(ready) != "0.,9.session-1;" {
		t.Fatalf("ready=%q err=%v", ready, err)
	}
	keepalive, err := TunnelKeepalive()
	if err != nil || string(keepalive) != "0.,4.ping,6.server;" {
		t.Fatalf("keepalive=%q err=%v", keepalive, err)
	}
	var forwarded bytes.Buffer
	controls, err := ForwardClientInstructions([]byte("0.,4.ping,1.x;3.key,1.a;"), &forwarded)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(controls); got != "0.,4.ping,1.x;" {
		t.Fatalf("controls=%q", got)
	}
	if got := forwarded.String(); got != "3.key,1.a;" {
		t.Fatalf("forwarded=%q", got)
	}
}

func TestSizeInstructionIncludesDPI(t *testing.T) {
	data, err := EncodeInstruction("size", "640", "480", "96")
	if err != nil || string(data) != "4.size,3.640,3.480,2.96;" {
		t.Fatalf("size=%q err=%v", data, err)
	}
}
