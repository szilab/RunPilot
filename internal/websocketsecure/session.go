package websocketsecure

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"

	"github.com/coder/websocket"
	"golang.org/x/crypto/hkdf"
)

const (
	Version       byte = 1
	ModeDisabled       = "disabled"
	ModeOptional       = "optional"
	ModeRequired       = "required"
	messageBinary      = websocket.MessageBinary
	messageText        = websocket.MessageText
	maxSequence        = ^uint64(0)
)

var (
	magic       = [4]byte{'R', 'P', 'S', '1'}
	envelopeTag = [4]byte{'R', 'P', 'W', 'E'}
	ErrRequired = errors.New("secure WebSocket negotiation required")
)

type rawConn interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Write(context.Context, websocket.MessageType, []byte) error
}

type Conn struct {
	raw        rawConn
	readKey    cipher.AEAD
	writeKey   cipher.AEAD
	readNonce  [12]byte
	writeNonce [12]byte
	readSeq    uint64
	writeSeq   uint64
	plain      bool
	pending    *frame
}

type frame struct {
	kind websocket.MessageType
	data []byte
}

func NormalizeMode(mode string) string {
	switch mode {
	case ModeOptional, ModeRequired:
		return mode
	default:
		return ModeDisabled
	}
}

// ServerHandshake negotiates one fresh session for one WebSocket connection.
// In optional mode a non-handshake first frame is retained and exposed by Read.
func ServerHandshake(ctx context.Context, raw rawConn, mode string) (*Conn, error) {
	mode = NormalizeMode(mode)
	if mode == ModeDisabled {
		return &Conn{raw: raw, plain: true}, nil
	}
	kind, data, err := raw.Read(ctx)
	if err != nil {
		return nil, err
	}
	if !isClientHello(data) {
		if mode == ModeRequired {
			return nil, ErrRequired
		}
		return &Conn{raw: raw, plain: true, pending: &frame{kind: kind, data: data}}, nil
	}
	if kind != messageBinary {
		return nil, errors.New("secure handshake must use a binary frame")
	}
	clientPublic, clientRandom, err := parseClientHello(data)
	if err != nil {
		return nil, err
	}
	serverKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate server key: %w", err)
	}
	serverRandom := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, serverRandom); err != nil {
		return nil, fmt.Errorf("generate server challenge: %w", err)
	}
	if err := raw.Write(ctx, messageBinary, serverHello(serverKey.PublicKey().Bytes(), serverRandom)); err != nil {
		return nil, err
	}
	shared, err := serverKey.ECDH(clientPublic)
	if err != nil {
		return nil, errors.New("invalid client key")
	}
	return newConn(raw, shared, clientRandom, serverRandom, false)
}

func NewClientHello() (private *ecdh.PrivateKey, hello []byte, random []byte, err error) {
	private, err = ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	random = make([]byte, 32)
	if _, err = io.ReadFull(rand.Reader, random); err != nil {
		return nil, nil, nil, err
	}
	hello = make([]byte, 0, 103)
	hello = append(hello, magic[:]...)
	hello = append(hello, Version, 1)
	hello = append(hello, private.PublicKey().Bytes()...)
	hello = append(hello, random...)
	return private, hello, random, nil
}

func ClientSession(raw rawConn, private *ecdh.PrivateKey, clientRandom, serverHelloData []byte) (*Conn, error) {
	serverPublic, serverRandom, err := parseServerHello(serverHelloData)
	if err != nil {
		return nil, err
	}
	shared, err := private.ECDH(serverPublic)
	if err != nil {
		return nil, errors.New("invalid server key")
	}
	return newConn(raw, shared, clientRandom, serverRandom, true)
}

func (c *Conn) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	if c.plain {
		if c.pending != nil {
			pending := c.pending
			c.pending = nil
			return pending.kind, pending.data, nil
		}
		return c.raw.Read(ctx)
	}
	kind, data, err := c.raw.Read(ctx)
	if err != nil {
		return 0, nil, err
	}
	if kind != messageBinary {
		return 0, nil, errors.New("encrypted WebSocket message must be binary")
	}
	if len(data) < 14 || (data[5] != byte(messageBinary) && data[5] != byte(messageText)) {
		return 0, nil, errors.New("invalid encrypted WebSocket message type")
	}
	originalKind := websocket.MessageType(data[5])
	plain, err := c.open(data, true)
	return originalKind, plain, err
}

func (c *Conn) Write(ctx context.Context, kind websocket.MessageType, data []byte) error {
	if c.plain {
		return c.raw.Write(ctx, kind, data)
	}
	if c.writeSeq == maxSequence {
		return errors.New("secure WebSocket sequence exhausted")
	}
	sequence := c.writeSeq
	header := make([]byte, 14)
	copy(header, envelopeTag[:])
	header[4] = Version
	header[5] = byte(kind)
	binary.BigEndian.PutUint64(header[6:], sequence)
	sealed := c.writeKey.Seal(nil, nonce(c.writeNonce, sequence), data, header)
	c.writeSeq++
	return c.raw.Write(ctx, messageBinary, append(header, sealed...))
}

func (c *Conn) open(data []byte, inbound bool) ([]byte, error) {
	if len(data) < 14 {
		return nil, errors.New("encrypted WebSocket envelope is truncated")
	}
	if string(data[:4]) != string(envelopeTag[:]) || data[4] != Version {
		return nil, errors.New("unsupported encrypted WebSocket envelope")
	}
	sequence := binary.BigEndian.Uint64(data[6:14])
	key, base := c.readKey, c.readNonce
	if !inbound {
		key, base = c.writeKey, c.writeNonce
	}
	expected := c.readSeq
	if !inbound {
		expected = c.writeSeq
	}
	if sequence != expected || sequence == maxSequence {
		return nil, errors.New("invalid encrypted WebSocket sequence")
	}
	plain, err := key.Open(nil, nonce(base, sequence), data[14:], data[:14])
	if err != nil {
		return nil, errors.New("encrypted WebSocket authentication failed")
	}
	if inbound {
		c.readSeq++
	} else {
		c.writeSeq++
	}
	return plain, nil
}

func newConn(raw rawConn, shared, clientRandom, serverRandom []byte, client bool) (*Conn, error) {
	material := make([]byte, 88)
	reader := hkdf.New(sha256Hash, shared, append(append([]byte{}, clientRandom...), serverRandom...), []byte("RunPilot WebSocket secure channel v1"))
	if _, err := io.ReadFull(reader, material); err != nil {
		return nil, err
	}
	c2s, err := cipherFor(material[:32])
	if err != nil {
		return nil, err
	}
	s2c, err := cipherFor(material[32:64])
	if err != nil {
		return nil, err
	}
	var cNonce, sNonce [12]byte
	copy(cNonce[:], material[64:76])
	copy(sNonce[:], material[76:88])
	if client {
		return &Conn{raw: raw, readKey: s2c, writeKey: c2s, readNonce: sNonce, writeNonce: cNonce}, nil
	}
	return &Conn{raw: raw, readKey: c2s, writeKey: s2c, readNonce: cNonce, writeNonce: sNonce}, nil
}

func cipherFor(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func nonce(base [12]byte, sequence uint64) []byte {
	out := append([]byte(nil), base[:]...)
	binary.BigEndian.PutUint64(out[4:], binary.BigEndian.Uint64(out[4:])^sequence)
	return out
}

func isClientHello(data []byte) bool {
	return len(data) >= 6 && string(data[:4]) == string(magic[:]) && data[4] == Version && data[5] == 1
}

func parseClientHello(data []byte) (*ecdh.PublicKey, []byte, error) {
	if len(data) != 103 {
		return nil, nil, errors.New("invalid secure client hello")
	}
	key, err := ecdh.P256().NewPublicKey(data[6:71])
	if err != nil {
		return nil, nil, errors.New("invalid secure client public key")
	}
	return key, append([]byte(nil), data[71:]...), nil
}

func serverHello(public, random []byte) []byte {
	return append(append(append(append([]byte{}, magic[:]...), Version, 2), public...), random...)
}

func parseServerHello(data []byte) (*ecdh.PublicKey, []byte, error) {
	if len(data) != 103 || string(data[:4]) != string(magic[:]) || data[4] != Version || data[5] != 2 {
		return nil, nil, errors.New("invalid secure server hello")
	}
	key, err := ecdh.P256().NewPublicKey(data[6:71])
	if err != nil {
		return nil, nil, errors.New("invalid secure server public key")
	}
	return key, append([]byte(nil), data[71:]...), nil
}

func sha256Hash() hash.Hash { return sha256.New() }
