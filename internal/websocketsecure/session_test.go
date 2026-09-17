package websocketsecure

import (
	"context"
	"testing"

	"github.com/coder/websocket"
)

type memoryConn struct {
	in  chan frame
	out chan frame
}

func (c *memoryConn) Read(context.Context) (websocket.MessageType, []byte, error) {
	message := <-c.in
	return message.kind, message.data, nil
}

func (c *memoryConn) Write(_ context.Context, kind websocket.MessageType, data []byte) error {
	c.out <- frame{kind: kind, data: append([]byte(nil), data...)}
	return nil
}

func newMemoryPair() (*memoryConn, *memoryConn) {
	left := make(chan frame, 8)
	right := make(chan frame, 8)
	return &memoryConn{in: left, out: right}, &memoryConn{in: right, out: left}
}

func securePair(t *testing.T) (*Conn, *Conn) {
	t.Helper()
	clientRaw, serverRaw := newMemoryPair()
	private, hello, random, err := NewClientHello()
	if err != nil {
		t.Fatal(err)
	}
	if err := clientRaw.Write(context.Background(), websocket.MessageBinary, hello); err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan struct {
		conn *Conn
		err  error
	}, 1)
	go func() {
		conn, err := ServerHandshake(context.Background(), serverRaw, ModeRequired)
		serverDone <- struct {
			conn *Conn
			err  error
		}{conn, err}
	}()
	serverResult := <-serverDone
	if serverResult.err != nil {
		t.Fatal(serverResult.err)
	}
	serverHello := (<-clientRaw.in).data
	client, err := ClientSession(clientRaw, private, random, serverHello)
	if err != nil {
		t.Fatal(err)
	}
	return client, serverResult.conn
}

func TestSecureSessionRoundTripAndSequence(t *testing.T) {
	client, server := securePair(t)
	payloads := [][]byte{nil, {}, []byte("small"), make([]byte, 64*1024)}
	for _, payload := range payloads {
		if err := client.Write(context.Background(), websocket.MessageBinary, payload); err != nil {
			t.Fatal(err)
		}
		kind, got, err := server.Read(context.Background())
		if err != nil || kind != websocket.MessageBinary {
			t.Fatalf("server read kind=%v err=%v", kind, err)
		}
		if string(got) != string(payload) {
			t.Fatalf("payload changed: got %d bytes, want %d", len(got), len(payload))
		}
	}
	if client.writeSeq != uint64(len(payloads)) || server.readSeq != uint64(len(payloads)) {
		t.Fatalf("sequence client=%d server=%d", client.writeSeq, server.readSeq)
	}
	if err := server.Write(context.Background(), websocket.MessageText, []byte("response")); err != nil {
		t.Fatal(err)
	}
	kind, got, err := client.Read(context.Background())
	if err != nil || kind != websocket.MessageText || string(got) != "response" {
		t.Fatalf("client response kind=%v payload=%q err=%v", kind, got, err)
	}
}

func TestSecureSessionRejectsReplayAndTampering(t *testing.T) {
	client, server := securePair(t)
	if err := client.Write(context.Background(), websocket.MessageBinary, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	wire := <-server.raw.(*memoryConn).in
	if _, err := server.open(wire.data, true); err != nil {
		t.Fatal(err)
	}
	if _, err := server.open(wire.data, true); err == nil {
		t.Fatal("replayed envelope was accepted")
	}
	client2, server2 := securePair(t)
	if err := client2.Write(context.Background(), websocket.MessageBinary, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	wire = <-server2.raw.(*memoryConn).in
	wire.data[len(wire.data)-1] ^= 1
	if _, err := server2.open(wire.data, true); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}

func TestSecureSessionRejectsHeaderTampering(t *testing.T) {
	client, server := securePair(t)
	if err := client.Write(context.Background(), websocket.MessageBinary, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	wire := <-server.raw.(*memoryConn).in
	wire.data[13] = 1
	if _, err := server.open(wire.data, true); err == nil {
		t.Fatal("tampered sequence header was accepted")
	}
}

func TestHandshakeModes(t *testing.T) {
	clientRaw, serverRaw := newMemoryPair()
	plain := []byte("legacy")
	if err := clientRaw.Write(context.Background(), websocket.MessageText, plain); err != nil {
		t.Fatal(err)
	}
	conn, err := ServerHandshake(context.Background(), serverRaw, ModeOptional)
	if err != nil {
		t.Fatal(err)
	}
	kind, got, err := conn.Read(context.Background())
	if err != nil || kind != websocket.MessageText || string(got) != string(plain) {
		t.Fatalf("optional fallback kind=%v got=%q err=%v", kind, got, err)
	}
	clientRaw, serverRaw = newMemoryPair()
	if err := clientRaw.Write(context.Background(), websocket.MessageText, plain); err != nil {
		t.Fatal(err)
	}
	if _, err := ServerHandshake(context.Background(), serverRaw, ModeRequired); err == nil {
		t.Fatal("required mode accepted plaintext")
	}
}

func TestWrongSessionKeyFails(t *testing.T) {
	client, server := securePair(t)
	if err := client.Write(context.Background(), websocket.MessageBinary, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	wire := <-server.raw.(*memoryConn).in
	wrongKey, err := cipherFor(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	server.readKey = wrongKey
	server.readSeq = 0
	wire.data[14] ^= 1
	if _, err := server.open(wire.data, true); err == nil {
		t.Fatal("wrong session key accepted payload")
	}
}
