package core

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPluginNetworkStreamTCPReadWriteOwnershipAndAttach(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()
	manager := newPluginNetworkStreamManager(nil)
	defer manager.close()
	address := listener.Addr().(*net.TCPAddr)
	opened, err := manager.open(context.Background(), "remote.rdp", mustJSON(t, pluginNetworkOpen{Host: "127.0.0.1", Port: address.Port, ConnectTimeoutSeconds: 2}))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(opened, &result); err != nil || result.ID == "" {
		t.Fatalf("open result %s, %v", opened, err)
	}
	peer := <-accepted
	defer peer.Close()
	encoded := base64.StdEncoding.EncodeToString([]byte("client-data"))
	if _, err := manager.write(context.Background(), "remote.rdp", mustJSON(t, map[string]string{"id": result.ID, "data": encoded})); err != nil {
		t.Fatal(err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("client-data"))
	if _, err := io.ReadFull(peer, got); err != nil || string(got) != "client-data" {
		t.Fatalf("peer read %q, %v", got, err)
	}
	if _, err := peer.Write([]byte("server-data")); err != nil {
		t.Fatal(err)
	}
	read, err := manager.read(context.Background(), "remote.rdp", mustJSON(t, map[string]any{"id": result.ID, "maxBytes": 64, "timeoutMilliseconds": 1000}))
	if err != nil {
		t.Fatal(err)
	}
	var readResult struct {
		Data  string `json:"data"`
		EOF   bool   `json:"eof"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(read, &readResult); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(readResult.Data)
	if err != nil || string(decoded) != "server-data" || readResult.EOF || readResult.State != "open" {
		t.Fatalf("read result %#v, decoded=%q err=%v", readResult, decoded, err)
	}
	if _, err := manager.read(context.Background(), "other.plugin", mustJSON(t, map[string]any{"id": result.ID, "maxBytes": 1, "timeoutMilliseconds": 10})); !isHostFailure(err, "not_found") {
		t.Fatalf("foreign read error = %v", err)
	}
	if _, _, err := manager.attach("other.plugin", result.ID); !isHostFailure(err, "not_found") {
		t.Fatalf("foreign attach error = %v", err)
	}
	if _, err := manager.write(context.Background(), "other.plugin", mustJSON(t, map[string]string{"id": result.ID, "data": base64.StdEncoding.EncodeToString([]byte("x"))})); !isHostFailure(err, "not_found") {
		t.Fatalf("foreign write error = %v", err)
	}
	if _, err := manager.closeStream("other.plugin", mustJSON(t, map[string]string{"id": result.ID})); !isHostFailure(err, "not_found") {
		t.Fatalf("foreign close error = %v", err)
	}
	conn, release, err := manager.attach("remote.rdp", result.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.attach("remote.rdp", result.ID); !isHostFailure(err, "failed_precondition") {
		t.Fatalf("second attach error = %v", err)
	}
	release()
	if _, err := manager.write(context.Background(), "remote.rdp", mustJSON(t, map[string]string{"id": result.ID, "data": encoded})); !isHostFailure(err, "failed_precondition") {
		t.Fatalf("closed stream write error = %v", err)
	}
	if _, err := manager.read(context.Background(), "remote.rdp", mustJSON(t, map[string]any{"id": result.ID, "maxBytes": 1, "timeoutMilliseconds": 10})); !isHostFailure(err, "failed_precondition") {
		t.Fatalf("closed stream read error = %v", err)
	}
	if _, err := conn.Write([]byte("closed")); err == nil {
		t.Fatal("released stream still accepts writes")
	}
	if _, err := manager.closeStream("remote.rdp", mustJSON(t, map[string]string{"id": result.ID})); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
	if _, err := manager.closeStream("remote.rdp", mustJSON(t, map[string]string{"id": "already-expired"})); err != nil {
		t.Fatalf("close after record eviction: %v", err)
	}
}

func TestPluginNetworkStreamPeekPreservesBrowserBytes(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	peerReady := make(chan net.Conn, 1)
	go func() {
		peer, acceptErr := listener.Accept()
		if acceptErr == nil {
			peerReady <- peer
		}
	}()
	manager := newPluginNetworkStreamManager(nil)
	defer manager.close()
	address := listener.Addr().(*net.TCPAddr)
	opened, err := manager.open(context.Background(), "owner", mustJSON(t, pluginNetworkOpen{Host: "127.0.0.1", Port: address.Port, ConnectTimeoutSeconds: 2}))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(opened, &result); err != nil {
		t.Fatal(err)
	}
	peer := <-peerReady
	defer peer.Close()
	if _, err := peer.Write([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	peeked, err := manager.read(context.Background(), "owner", mustJSON(t, map[string]any{"id": result.ID, "maxBytes": 3, "timeoutMilliseconds": 1000, "peek": true, "offset": 2}))
	if err != nil {
		t.Fatal(err)
	}
	var peekResult struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(peeked, &peekResult); err != nil {
		t.Fatal(err)
	}
	peekBytes, err := base64.StdEncoding.DecodeString(peekResult.Data)
	if err != nil || string(peekBytes) != "cde" {
		t.Fatalf("peek bytes=%q err=%v", peekBytes, err)
	}
	consumed, err := manager.read(context.Background(), "owner", mustJSON(t, map[string]any{"id": result.ID, "maxBytes": 6, "timeoutMilliseconds": 1000}))
	if err != nil {
		t.Fatal(err)
	}
	var consumeResult struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(consumed, &consumeResult); err != nil {
		t.Fatal(err)
	}
	consumedBytes, err := base64.StdEncoding.DecodeString(consumeResult.Data)
	if err != nil || string(consumedBytes) != "abcdef" {
		t.Fatalf("peek consumed bytes: %q err=%v", consumedBytes, err)
	}
	conn, release, err := manager.attach("owner", result.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := peer.Write([]byte("browser")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len("browser"))
	if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "browser" {
		t.Fatalf("attached stream data=%q err=%v", buffer, err)
	}
}

func TestPluginNetworkStreamBoundsTimeoutAndShutdown(t *testing.T) {
	manager := newPluginNetworkStreamManager(nil)
	defer manager.close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	opened, err := manager.open(context.Background(), "owner", mustJSON(t, pluginNetworkOpen{Host: "127.0.0.1", Port: address.Port, ConnectTimeoutSeconds: 2}))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(opened, &result); err != nil {
		t.Fatal(err)
	}
	peer := <-accepted
	defer peer.Close()
	if _, err := manager.read(context.Background(), "owner", mustJSON(t, map[string]any{"id": result.ID, "maxBytes": maxPluginNetworkStreamBytes + 1, "timeoutMilliseconds": 10})); !isHostFailure(err, "invalid_argument") {
		t.Fatalf("oversized read error = %v", err)
	}
	if _, err := manager.write(context.Background(), "owner", mustJSON(t, map[string]string{"id": result.ID, "data": base64.StdEncoding.EncodeToString(make([]byte, maxPluginNetworkStreamBytes+1))})); !isHostFailure(err, "invalid_argument") {
		t.Fatalf("oversized write error = %v", err)
	}
	if _, err := manager.read(context.Background(), "owner", mustJSON(t, map[string]any{"id": result.ID, "maxBytes": 1, "timeoutMilliseconds": 10})); !isHostFailure(err, "timeout") {
		t.Fatalf("read timeout error = %v", err)
	}
	manager.stopOwner("owner")
	if _, err := manager.write(context.Background(), "owner", mustJSON(t, map[string]string{"id": result.ID, "data": base64.StdEncoding.EncodeToString([]byte("x"))})); !isHostFailure(err, "not_found") {
		t.Fatalf("shutdown cleanup lookup error = %v", err)
	}
}

func TestPluginNetworkStreamRejectsInvalidAddresses(t *testing.T) {
	manager := newPluginNetworkStreamManager(nil)
	defer manager.close()
	for _, input := range []pluginNetworkOpen{
		{Host: "https://localhost", Port: 4822, ConnectTimeoutSeconds: 5},
		{Host: "localhost/path", Port: 4822, ConnectTimeoutSeconds: 5},
		{Host: "127.0.0.1", Port: 0, ConnectTimeoutSeconds: 5},
		{Host: "127.0.0.1", Port: 65536, ConnectTimeoutSeconds: 5},
		{Host: "127.0.0.1", Port: 4822, ConnectTimeoutSeconds: 31},
	} {
		if _, err := manager.open(context.Background(), "owner", mustJSON(t, input)); !isHostFailure(err, "invalid_argument") {
			t.Fatalf("invalid input %#v error = %v", input, err)
		}
	}
}

func TestPluginNetworkStreamTLSUsesCertificateValidationAndConnectDeadline(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	address := server.Listener.Addr().(*net.TCPAddr)
	trustedManager := newPluginNetworkStreamManager(nil)
	defer trustedManager.close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	trustedManager.tlsConfig = func(in pluginNetworkOpen) *tls.Config {
		value := pluginNetworkTLSConfig(in)
		value.RootCAs = roots
		return value
	}
	trustedRequest := pluginNetworkOpen{Host: "127.0.0.1", Port: address.Port, ConnectTimeoutSeconds: 2}
	trustedRequest.TLS.Enabled = true
	trustedRequest.TLS.ServerName = "127.0.0.1"
	opened, err := trustedManager.open(context.Background(), "owner", mustJSON(t, trustedRequest))
	if err != nil {
		t.Fatalf("trusted TLS connection failed: %v", err)
	}
	var stream struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(opened, &stream); err != nil || stream.ID == "" {
		t.Fatalf("trusted TLS stream result=%s err=%v", opened, err)
	}
	if _, err := trustedManager.closeStream("owner", mustJSON(t, map[string]string{"id": stream.ID})); err != nil {
		t.Fatal(err)
	}
	manager := newPluginNetworkStreamManager(nil)
	defer manager.close()
	request := pluginNetworkOpen{Host: "127.0.0.1", Port: address.Port, ConnectTimeoutSeconds: 2}
	request.TLS.Enabled = true
	request.TLS.ServerName = "localhost"
	if _, err := manager.open(context.Background(), "owner", mustJSON(t, request)); !isHostFailure(err, "failed") {
		t.Fatalf("untrusted TLS certificate was accepted or misclassified: %v", err)
	}
	if len(manager.items) != 0 {
		t.Fatalf("failed TLS handshake retained streams: %#v", manager.items)
	}
	connectContext, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	request = pluginNetworkOpen{Host: "127.0.0.1", Port: 1, ConnectTimeoutSeconds: 2}
	if _, err := manager.open(connectContext, "owner", mustJSON(t, request)); !isHostFailure(err, "timeout") {
		t.Fatalf("expired connection deadline error = %v", err)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
