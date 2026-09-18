package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/model"
)

func TestRemoteTargetCRUDAndClientIsNotPublic(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	token := ctrl.Snapshot().Server.Token
	body := []byte(`{"name":"Firefox","provider":"xpra","type":"application","command":{"path":"firefox","interpreter":"direct"}}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/remote/targets", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", response.Code, response.Body.String())
	}
	var created model.RemoteTarget
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Xpra == nil || created.Xpra.Encoding != "webp" || created.Xpra.Video == nil || *created.Xpra.Video || created.Xpra.LaunchAfterConnect == nil || !*created.Xpra.LaunchAfterConnect {
		t.Fatalf("legacy/default target was not normalized: %#v", created.Xpra)
	}
	invalid := []byte(`{"name":"Broken","provider":"xpra","type":"application","command":{"path":"xterm"},"xpra":{"encoding":"h264"}}`)
	request = httptest.NewRequest(http.MethodPost, "/api/v1/remote/targets", bytes.NewReader(invalid))
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte("picture encoding")) {
		t.Fatalf("invalid display config=%d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/remote/targets", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("Firefox")) {
		t.Fatalf("list=%d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/remote/sessions/not-owned/client/", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated client=%d", response.Code)
	}
}

func TestRemoteClientTicketStaysOnProxyPath(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl, "/runpilot")
	if err != nil {
		t.Fatal(err)
	}
	server.remoteTickets["ticket"] = remoteClientTicket{SessionID: "session", Expires: time.Now().Add(time.Minute)}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/runpilot/api/v1/remote/sessions/session/client/?ticket=ticket", nil))
	if response.Code != http.StatusFound {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	want := "/runpilot/api/v1/remote/sessions/session/client/"
	if got := response.Header().Get("Location"); got != want {
		t.Fatalf("redirect = %q, want %q", got, want)
	}
}

func TestRemoteClientRedirectAddsOnlyTicketSnapshotParameters(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	server.remoteTickets["ticket"] = remoteClientTicket{SessionID: "session", ClientParams: map[string]string{"encoding": "webp", "video": "no", "toolbar_position": "top-left"}, Expires: time.Now().Add(time.Minute)}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/remote/sessions/session/client/?ticket=ticket&encoding=rgb", nil))
	if response.Code != http.StatusFound {
		t.Fatalf("status = %d", response.Code)
	}
	location := response.Header().Get("Location")
	if !strings.Contains(location, "encoding=webp") || strings.Contains(location, "encoding=rgb") || !strings.Contains(location, "toolbar_position=top-left") {
		t.Fatalf("redirect did not use the session snapshot: %q", location)
	}
}

func TestRDPRemoteTargetHasNoCommandOrPasswordAndIssuesTransportTicket(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	_, err = ctrl.UpdateGuacdConfig(model.GuacdConfig{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, ConnectTimeoutSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	token := ctrl.Snapshot().Server.Token
	body := []byte(`{"name":"Local desktop","provider":"rdp","type":"desktop","rdp":{"host":"127.0.0.1","username":"admin"}}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/remote/targets", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create RDP=%d %s", response.Code, response.Body.String())
	}
	var target model.RemoteTarget
	if err := json.NewDecoder(response.Body).Decode(&target); err != nil {
		t.Fatal(err)
	}
	if target.Command.Path != "" || target.RDP == nil || target.RDP.Port != 3389 {
		t.Fatalf("RDP target = %#v", target)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/remote/targets/"+target.ID+"/sessions", bytes.NewBufferString(`{"password":"very-secret"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("start RDP=%d %s", response.Code, response.Body.String())
	}
	var session model.RemoteSession
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	if session.RDP == nil || strings.Contains(response.Body.String(), "password") || strings.Contains(response.Body.String(), "very-secret") {
		t.Fatalf("unsafe RDP session response: %s", response.Body.String())
	}
	_, diagnostics, err := ctrl.Remote().Diagnostics(session.ID)
	if err != nil || strings.Contains(diagnostics, "very-secret") {
		t.Fatalf("unsafe RDP diagnostics=%q err=%v", diagnostics, err)
	}
	if session.Message != "" {
		t.Fatalf("RDP session must not expose a placeholder browser message: %q", session.Message)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/remote/sessions/"+session.ID+"/transport-ticket", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), "ticket") {
		t.Fatalf("RDP ticket=%d %s", response.Code, response.Body.String())
	}
}

func TestVNCTransportIsSessionScopedAndBridgesBinaryRFB(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	received := make(chan []byte, 1)
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("RFB 003.008\n"))
		buffer := make([]byte, 32)
		count, _ := conn.Read(buffer)
		received <- append([]byte(nil), buffer[:count]...)
		_, _ = conn.Write([]byte("server-response"))
	}()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	token := ctrl.Snapshot().Server.Token
	port := upstream.Addr().(*net.TCPAddr).Port
	request := httptest.NewRequest(http.MethodPost, "/api/v1/remote/targets", bytes.NewBufferString(`{"name":"Local VNC","provider":"vnc","type":"desktop","vnc":{"host":"127.0.0.1","port":`+strconv.Itoa(port)+`}}`))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create VNC=%d %s", response.Code, response.Body.String())
	}
	var target model.RemoteTarget
	if err := json.NewDecoder(response.Body).Decode(&target); err != nil {
		t.Fatal(err)
	}
	if target.Command.Path != "" || target.VNC == nil || target.VNC.Port != port {
		t.Fatalf("VNC target=%#v", target)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/remote/targets/"+target.ID+"/sessions", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("start VNC=%d %s", response.Code, response.Body.String())
	}
	var session model.RemoteSession
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	if session.VNC == nil || session.VNC.Host != "127.0.0.1" || strings.Contains(response.Body.String(), "password") {
		t.Fatalf("unsafe VNC session response=%s", response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/remote/sessions/"+session.ID+"/transport-ticket", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("VNC ticket=%d %s", response.Code, response.Body.String())
	}
	var ticket struct{ Ticket string }
	if err := json.NewDecoder(response.Body).Decode(&ticket); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/v1/remote/sessions/" + session.ID + "/transport?ticket=" + ticket.Ticket
	ws, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	if err := ws.Write(context.Background(), websocket.MessageBinary, []byte("client-request")); err != nil {
		t.Fatal(err)
	}
	if got := <-received; !bytes.Equal(got, []byte("client-request")) {
		t.Fatalf("upstream received %q", got)
	}
	readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	want := []byte("RFB 003.008\nserver-response")
	var got []byte
	for len(got) < len(want) {
		messageType, data, err := ws.Read(readCtx)
		if err != nil || messageType != websocket.MessageBinary {
			t.Fatalf("RFB message type=%v data=%q err=%v", messageType, data, err)
		}
		got = append(got, data...)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("RFB stream data=%q, want %q", got, want)
	}
}

func TestGuacdCandidateTestDoesNotPersist(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	if _, err := ctrl.UpdateGuacdConfig(model.GuacdConfig{Host: "saved-guacd", Port: 4822, ConnectTimeoutSeconds: 5}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	token := ctrl.Snapshot().Server.Token
	candidate := model.GuacdConfig{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, ConnectTimeoutSeconds: 5}
	body, _ := json.Marshal(candidate)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/remote/guacd/test", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("candidate test=%d %s", response.Code, response.Body.String())
	}
	if got := ctrl.Snapshot().Remote.Guacd; got.Host != "saved-guacd" || got.Port != 4822 {
		t.Fatalf("candidate test persisted %#v", got)
	}
	bad := httptest.NewRequest(http.MethodPost, "/api/v1/remote/guacd/test", bytes.NewBufferString(`{"host":"","port":4822,"connectTimeoutSeconds":5}`))
	bad.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, bad)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid candidate=%d %s", response.Code, response.Body.String())
	}
}

func TestRDPCredentialsInteractiveLoginAndNLA(t *testing.T) {
	target := model.RemoteTarget{Provider: "rdp", RDP: &model.RDPRemoteOptions{Username: "stored-user", Domain: "stored-domain", SecurityMode: model.RDPSecurityAutomatic}}
	got, err := effectiveRDPCredentials(target, rdpCredentials{Username: "dialog-user", Domain: "dialog-domain"})
	if err != nil || got.Username != "" || got.Domain != "" || got.Password != "" {
		t.Fatalf("interactive credentials=%#v err=%v", got, err)
	}
	if target.RDP.Username != "stored-user" || target.RDP.Domain != "stored-domain" {
		t.Fatalf("interactive login changed configured target identity: %#v", target.RDP)
	}
	got, err = effectiveRDPCredentials(target, rdpCredentials{Password: "secret"})
	if err != nil || got.Username != "stored-user" || got.Domain != "stored-domain" || got.Password != "secret" {
		t.Fatalf("supplied credentials=%#v err=%v", got, err)
	}
	got, err = effectiveRDPCredentials(target, rdpCredentials{Username: "dialog-user", Domain: "dialog-domain", Password: "secret"})
	if err != nil || got.Username != "dialog-user" || got.Domain != "dialog-domain" {
		t.Fatalf("dialog credentials=%#v err=%v", got, err)
	}
	target.RDP.SecurityMode = model.RDPSecurityNLA
	if _, err := effectiveRDPCredentials(target, rdpCredentials{}); err == nil || !strings.Contains(err.Error(), "NLA requires credentials") {
		t.Fatalf("NLA error=%v", err)
	}
}

func TestRDPDiagnosticsNeverContainCredentials(t *testing.T) {
	options := model.RDPRemoteOptions{Host: "10.0.0.20", Port: 3389, SecurityMode: model.RDPSecurityAutomatic, ServerLayout: "hu-hu-qwertz", Username: "admin", Domain: "example"}
	log := rdpSetupDiagnostics("remote-test", "127.0.0.1", 4822, options, true)
	for _, secret := range []string{"admin", "example", "password", "secret"} {
		if strings.Contains(log, secret) {
			t.Fatalf("diagnostics exposed %q: %s", secret, log)
		}
	}
	if !strings.Contains(log, "credentials: supplied") || !strings.Contains(log, "server-layout: hu-hu-qwertz") {
		t.Fatalf("diagnostics=%s", log)
	}
}
