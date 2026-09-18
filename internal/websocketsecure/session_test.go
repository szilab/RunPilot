package websocketsecure

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type memoryConn struct {
	in  chan frame
	out chan frame
}

type limitedConn struct {
	*memoryConn
	limit int64
}

type blockingConn struct{}

func (blockingConn) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	<-ctx.Done()
	return 0, nil, ctx.Err()
}

func (blockingConn) Write(ctx context.Context, _ websocket.MessageType, _ []byte) error {
	<-ctx.Done()
	return ctx.Err()
}

func (c *limitedConn) Write(ctx context.Context, kind websocket.MessageType, data []byte) error {
	if int64(len(data)) > c.limit {
		return errors.New("message exceeds configured read limit")
	}
	return c.memoryConn.Write(ctx, kind, data)
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

func TestSecureSessionPreservesConsecutiveServerBinaryFrames(t *testing.T) {
	client, server := securePair(t)
	payloads := [][]byte{[]byte("prompt> "), []byte("rapid command\r\n"), {0, 1, 2, 255}, []byte("next prompt> ")}
	for _, payload := range payloads {
		if err := server.Write(context.Background(), websocket.MessageBinary, payload); err != nil {
			t.Fatal(err)
		}
		kind, got, err := client.Read(context.Background())
		if err != nil || kind != websocket.MessageBinary {
			t.Fatalf("client read kind=%v err=%v", kind, err)
		}
		if string(got) != string(payload) {
			t.Fatalf("payload changed: got %v, want %v", got, payload)
		}
	}
	if server.writeSeq != uint64(len(payloads)) || client.readSeq != uint64(len(payloads)) {
		t.Fatalf("sequence server=%d client=%d", server.writeSeq, client.readSeq)
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

func TestEncryptedReadLimitIncludesEnvelopeOverhead(t *testing.T) {
	client, server := securePair(t)
	client.raw = &limitedConn{memoryConn: client.raw.(*memoryConn), limit: MaxEncryptedMessageSize}

	if err := client.Write(context.Background(), websocket.MessageBinary, make([]byte, MaxPlaintextMessageSize)); err != nil {
		t.Fatalf("maximum plaintext payload was rejected: %v", err)
	}
	if _, payload, err := server.Read(context.Background()); err != nil || int64(len(payload)) != MaxPlaintextMessageSize {
		t.Fatalf("maximum encrypted payload read failed: bytes=%d err=%v", len(payload), err)
	}
	if err := client.Write(context.Background(), websocket.MessageBinary, make([]byte, MaxPlaintextMessageSize+1)); err == nil {
		t.Fatal("payload exceeding configured encrypted limit was accepted")
	}
}

func TestHandshakeHonorsContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := ServerHandshake(ctx, blockingConn{}, ModeRequired); err == nil {
		t.Fatal("handshake remained pending after context cancellation")
	}
}
