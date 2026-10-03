package core

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/plugins"
)

const (
	maxPluginNetworkStreamsPerOwner = 8
	maxPluginNetworkStreamsGlobal   = 64
	maxPluginNetworkRecordsPerOwner = 32
	maxPluginNetworkRecordsGlobal   = 256
	maxPluginNetworkStreamBytes     = 64 << 10
	maxPluginNetworkLookahead       = 64 << 10
	maxPluginNetworkConnectTimeout  = 30 * time.Second
	maxPluginNetworkReadTimeout     = 5 * time.Second
	pluginNetworkStreamRetention    = 5 * time.Minute
)

type pluginNetworkStreamManager struct {
	mu        sync.Mutex
	items     map[string]*pluginNetworkStream
	emit      func(string, string)
	tlsConfig func(pluginNetworkOpen) *tls.Config
	closed    bool
}

type pluginNetworkStream struct {
	owner    string
	id       string
	conn     net.Conn
	reader   *bufio.Reader
	state    string
	attached bool
	closedAt time.Time
}

type pluginNetworkOpen struct {
	Host                  string `json:"host"`
	Port                  int    `json:"port"`
	ConnectTimeoutSeconds int    `json:"connectTimeoutSeconds"`
	TLS                   struct {
		Enabled    bool   `json:"enabled"`
		ServerName string `json:"serverName"`
	} `json:"tls"`
}

func newPluginNetworkStreamManager(emit func(string, string)) *pluginNetworkStreamManager {
	return &pluginNetworkStreamManager{items: map[string]*pluginNetworkStream{}, emit: emit, tlsConfig: pluginNetworkTLSConfig}
}

func pluginNetworkTLSConfig(in pluginNetworkOpen) *tls.Config {
	serverName := in.TLS.ServerName
	if serverName == "" && net.ParseIP(in.Host) == nil {
		serverName = in.Host
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
}

func networkInvalid(message string) error {
	return &plugins.HostFailure{Code: "invalid_argument", Message: message}
}

func decodeNetworkParams(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("extra JSON value")
	}
	return nil
}

func validNetworkHost(host string) bool {
	if host == "" || strings.TrimSpace(host) != host || len(host) > 253 || strings.ContainsAny(host, ":/?#@[]\\\x00") {
		return net.ParseIP(host) != nil && len(host) <= 45
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func (m *pluginNetworkStreamManager) open(parent context.Context, owner string, raw json.RawMessage) (json.RawMessage, error) {
	var in pluginNetworkOpen
	if err := decodeNetworkParams(raw, &in); err != nil || !validNetworkHost(in.Host) || in.Port < 1 || in.Port > 65535 || in.ConnectTimeoutSeconds < 1 || time.Duration(in.ConnectTimeoutSeconds)*time.Second > maxPluginNetworkConnectTimeout {
		return nil, networkInvalid("host, port and connect timeout are invalid")
	}
	if in.TLS.ServerName != "" && !validNetworkHost(in.TLS.ServerName) {
		return nil, networkInvalid("TLS server name is invalid")
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(in.ConnectTimeoutSeconds)*time.Second)
	defer cancel()
	address := net.JoinHostPort(in.Host, strconv.Itoa(in.Port))
	dialer := &net.Dialer{}
	var conn net.Conn
	var err error
	if in.TLS.Enabled {
		tlsDialer := &tls.Dialer{NetDialer: dialer, Config: m.tlsConfig(in)}
		conn, err = tlsDialer.DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, &plugins.HostFailure{Code: "timeout", Message: "network connection timed out"}
		}
		return nil, &plugins.HostFailure{Code: "failed", Message: "network connection failed"}
	}
	var idBytes [24]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		_ = conn.Close()
		return nil, &plugins.HostFailure{Code: "failed", Message: "could not allocate network stream"}
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes[:])
	item := &pluginNetworkStream{owner: owner, id: id, conn: conn, reader: bufio.NewReaderSize(conn, maxPluginNetworkLookahead), state: "open"}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = conn.Close()
		return nil, &plugins.HostFailure{Code: "failed_precondition", Message: "network stream manager is shutting down"}
	}
	m.pruneClosedLocked(time.Now())
	owned, active, ownerRecords := 0, 0, 0
	for _, current := range m.items {
		if current.owner == owner {
			ownerRecords++
		}
		if current.state == "open" {
			active++
			if current.owner == owner {
				owned++
			}
		}
	}
	if owned >= maxPluginNetworkStreamsPerOwner || active >= maxPluginNetworkStreamsGlobal {
		m.mu.Unlock()
		_ = conn.Close()
		return nil, &plugins.HostFailure{Code: "resource_limit", Message: "network stream limit reached"}
	}
	if ownerRecords >= maxPluginNetworkRecordsPerOwner {
		m.evictOldestClosedLocked(owner)
	}
	if len(m.items) >= maxPluginNetworkRecordsGlobal {
		m.evictOldestClosedLocked("")
	}
	if len(m.items) >= maxPluginNetworkRecordsGlobal || m.ownerRecordCountLocked(owner) >= maxPluginNetworkRecordsPerOwner {
		m.mu.Unlock()
		_ = conn.Close()
		return nil, &plugins.HostFailure{Code: "resource_limit", Message: "network stream record limit reached"}
	}
	m.items[id] = item
	m.mu.Unlock()
	return json.Marshal(map[string]string{"id": id})
}

func (m *pluginNetworkStreamManager) pruneClosedLocked(now time.Time) {
	for id, item := range m.items {
		if item.state != "open" && now.Sub(item.closedAt) >= pluginNetworkStreamRetention {
			delete(m.items, id)
		}
	}
}

func (m *pluginNetworkStreamManager) ownerRecordCountLocked(owner string) int {
	count := 0
	for _, item := range m.items {
		if item.owner == owner {
			count++
		}
	}
	return count
}

func (m *pluginNetworkStreamManager) evictOldestClosedLocked(owner string) {
	var oldest *pluginNetworkStream
	for _, item := range m.items {
		if item.state == "open" || owner != "" && item.owner != owner {
			continue
		}
		if oldest == nil || item.closedAt.Before(oldest.closedAt) {
			oldest = item
		}
	}
	if oldest != nil {
		delete(m.items, oldest.id)
	}
}

func (m *pluginNetworkStreamManager) lookup(owner, id string) (*pluginNetworkStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item := m.items[id]
	if item == nil || item.owner != owner {
		return nil, &plugins.HostFailure{Code: "not_found", Message: "unknown network stream"}
	}
	return item, nil
}

func (m *pluginNetworkStreamManager) read(ctx context.Context, owner string, raw json.RawMessage) (json.RawMessage, error) {
	var in struct {
		ID                  string `json:"id"`
		MaxBytes            int    `json:"maxBytes"`
		TimeoutMilliseconds int    `json:"timeoutMilliseconds"`
		Peek                bool   `json:"peek,omitempty"`
		Offset              int    `json:"offset,omitempty"`
	}
	if err := decodeNetworkParams(raw, &in); err != nil || in.ID == "" {
		return nil, networkInvalid("id and read parameters are required")
	}
	if in.MaxBytes < 1 || in.MaxBytes > maxPluginNetworkStreamBytes {
		return nil, networkInvalid("maxBytes must be between 1 and 65536")
	}
	if in.TimeoutMilliseconds < 1 || time.Duration(in.TimeoutMilliseconds)*time.Millisecond > maxPluginNetworkReadTimeout {
		return nil, networkInvalid("timeoutMilliseconds must be between 1 and 5000")
	}
	if in.Offset < 0 || in.Peek && in.Offset+in.MaxBytes > maxPluginNetworkLookahead {
		return nil, networkInvalid("lookahead offset and maxBytes exceed the 65536-byte limit")
	}
	item, err := m.lookup(owner, in.ID)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if item.state != "open" || item.attached {
		state := item.state
		m.mu.Unlock()
		return nil, &plugins.HostFailure{Code: "failed_precondition", Message: "network stream is not available for reads: " + state}
	}
	deadline := time.Now().Add(time.Duration(in.TimeoutMilliseconds) * time.Millisecond)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = item.conn.SetReadDeadline(deadline)
	reader := item.reader
	m.mu.Unlock()
	buffer := make([]byte, in.MaxBytes)
	var n int
	var readErr error
	if in.Peek {
		if _, readErr = reader.Peek(in.Offset + 1); readErr == nil {
			available := reader.Buffered() - in.Offset
			if available > 0 {
				if available > in.MaxBytes {
					available = in.MaxBytes
				}
				var buffered []byte
				buffered, readErr = reader.Peek(in.Offset + available)
				if readErr == nil {
					n = copy(buffer, buffered[in.Offset:in.Offset+available])
				}
			}
		}
	} else {
		n, readErr = reader.Read(buffer)
	}
	if readErr != nil && n == 0 && !errors.Is(readErr, io.EOF) {
		if networkTimeout(readErr) {
			return nil, &plugins.HostFailure{Code: "timeout", Message: "network stream read timed out"}
		}
		m.markClosed(item, "failed")
		return nil, &plugins.HostFailure{Code: "failed", Message: "network stream read failed"}
	}
	eof := errors.Is(readErr, io.EOF)
	if eof {
		m.markClosed(item, "eof")
	}
	return json.Marshal(map[string]any{"data": base64.StdEncoding.EncodeToString(buffer[:n]), "eof": eof, "state": map[bool]string{true: "eof", false: "open"}[eof]})
}

func (m *pluginNetworkStreamManager) write(ctx context.Context, owner string, raw json.RawMessage) (json.RawMessage, error) {
	var in struct {
		ID   string `json:"id"`
		Data string `json:"data"`
	}
	if err := decodeNetworkParams(raw, &in); err != nil || in.ID == "" || len(in.Data) == 0 || len(in.Data) > base64.StdEncoding.EncodedLen(maxPluginNetworkStreamBytes) {
		return nil, networkInvalid("id and data containing at most 65536 bytes are required")
	}
	data, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil || len(data) == 0 || len(data) > maxPluginNetworkStreamBytes {
		return nil, networkInvalid("data must be valid base64 containing at most 65536 bytes")
	}
	item, err := m.lookup(owner, in.ID)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if item.state != "open" || item.attached {
		m.mu.Unlock()
		return nil, &plugins.HostFailure{Code: "failed_precondition", Message: "network stream is not available for writes"}
	}
	deadline := time.Now().Add(maxPluginNetworkReadTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = item.conn.SetWriteDeadline(deadline)
	conn := item.conn
	m.mu.Unlock()
	for len(data) > 0 {
		n, writeErr := conn.Write(data)
		if writeErr != nil {
			if networkTimeout(writeErr) {
				return nil, &plugins.HostFailure{Code: "timeout", Message: "network stream write timed out"}
			}
			m.markClosed(item, "failed")
			return nil, &plugins.HostFailure{Code: "failed", Message: "network stream write failed"}
		}
		if n == 0 {
			return nil, &plugins.HostFailure{Code: "failed", Message: "network stream write failed"}
		}
		data = data[n:]
	}
	return json.RawMessage(`{}`), nil
}

func (m *pluginNetworkStreamManager) closeStream(owner string, raw json.RawMessage) (json.RawMessage, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := decodeNetworkParams(raw, &in); err != nil || in.ID == "" {
		return nil, networkInvalid("id is required")
	}
	m.mu.Lock()
	item := m.items[in.ID]
	if item == nil {
		m.mu.Unlock()
		return json.RawMessage(`{}`), nil
	}
	if item.owner != owner {
		m.mu.Unlock()
		return nil, &plugins.HostFailure{Code: "not_found", Message: "unknown network stream"}
	}
	m.mu.Unlock()
	m.markClosed(item, "closed")
	return json.RawMessage(`{}`), nil
}

func (m *pluginNetworkStreamManager) markClosed(item *pluginNetworkStream, state string) {
	m.mu.Lock()
	changed := false
	if item.state == "open" {
		item.state = state
		item.closedAt = time.Now()
		_ = item.conn.Close()
		changed = true
		time.AfterFunc(pluginNetworkStreamRetention, func() {
			m.mu.Lock()
			if m.items[item.id] == item {
				delete(m.items, item.id)
			}
			m.mu.Unlock()
		})
	}
	m.mu.Unlock()
	if changed && m.emit != nil {
		m.emit(item.owner, item.id)
	}
}

func (m *pluginNetworkStreamManager) attach(owner, id string) (net.Conn, func(), error) {
	item, err := m.lookup(owner, id)
	if err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	if item.state != "open" || item.attached {
		m.mu.Unlock()
		return nil, nil, &plugins.HostFailure{Code: "failed_precondition", Message: "network stream is not available for attachment"}
	}
	item.attached = true
	conn := item.conn
	_ = conn.SetDeadline(time.Time{})
	m.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { m.markClosed(item, "closed") }) }
	return pluginNetworkAttachedConn{Conn: conn, reader: item.reader}, release, nil
}

type pluginNetworkAttachedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (conn pluginNetworkAttachedConn) Read(data []byte) (int, error) {
	return conn.reader.Read(data)
}

func (m *pluginNetworkStreamManager) stopOwner(owner string) {
	m.mu.Lock()
	var items []*pluginNetworkStream
	for id, item := range m.items {
		if item.owner == owner {
			items = append(items, item)
			delete(m.items, id)
		}
	}
	m.mu.Unlock()
	for _, item := range items {
		_ = item.conn.Close()
	}
}

func (m *pluginNetworkStreamManager) close() {
	m.mu.Lock()
	m.closed = true
	items := make([]*pluginNetworkStream, 0, len(m.items))
	for _, item := range m.items {
		items = append(items, item)
	}
	m.items = map[string]*pluginNetworkStream{}
	m.mu.Unlock()
	for _, item := range items {
		_ = item.conn.Close()
	}
}

func networkTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
