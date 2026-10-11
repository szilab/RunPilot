package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/core"
)

func TestFirstRunCreatesSessionWithoutAuthorizationToken(t *testing.T) {
	t.Setenv("RUNPILOT_DATA_ROOT_FILE", filepath.Join(t.TempDir(), "deployment", "data-root"))
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	h := server.Handler()
	initial := ctrl.Snapshot().Server.Token
	for _, target := range []string{"/api/v1/system", "/api/v1/ws/ticket", "/api/v1/plugins", "/api/v1/storage"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, target, nil))
		if r.Code != http.StatusPreconditionRequired {
			t.Errorf("%s status=%d", target, r.Code)
		}
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "within 5 minutes") {
		t.Fatal("setup UI is missing the setup-window instructions")
	}
	if !strings.Contains(page.Body.String(), `href="/styles.css"`) {
		t.Fatal("setup UI does not reference the embedded RunPilot stylesheet")
	}
	if !strings.Contains(page.Body.String(), `prefers-color-scheme: dark`) || strings.Contains(page.Body.String(), "authorization token") {
		t.Fatal("setup theme preference or authorization wording is missing")
	}
	if !strings.Contains(page.Body.String(), `name="username" value="admin"`) || !strings.Contains(page.Body.String(), `name="dataDir" type="text" value="`+ctrl.DataDir()+`"`) {
		t.Fatal("setup UI does not prefill the administrator and current data directory")
	}
	stylesheet := httptest.NewRecorder()
	h.ServeHTTP(stylesheet, httptest.NewRequest(http.MethodGet, "/styles.css", nil))
	if stylesheet.Code != http.StatusOK || !strings.Contains(stylesheet.Body.String(), ".setup-screen") {
		t.Fatalf("setup stylesheet unavailable before setup: status=%d", stylesheet.Code)
	}
	payload := map[string]string{"username": "admin", "password": "correct horse battery staple", "confirm": "correct horse battery staple"}
	body, _ := json.Marshal(payload)
	setup := httptest.NewRecorder()
	h.ServeHTTP(setup, httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewReader(body)))
	if setup.Code != http.StatusOK {
		t.Fatalf("setup status=%d body=%s", setup.Code, setup.Body.String())
	}
	if len(setup.Result().Cookies()) != 2 || setup.Result().Cookies()[0].Name != adminSessionCookie {
		t.Fatal("setup did not issue HttpOnly session and CSRF cookies")
	}
	legacyBearer := httptest.NewRequest(http.MethodGet, "/api/v1/system", nil)
	legacyBearer.Header.Set("Authorization", "Bearer "+initial)
	legacyResponse := httptest.NewRecorder()
	h.ServeHTTP(legacyResponse, legacyBearer)
	if legacyResponse.Code != http.StatusUnauthorized {
		t.Fatalf("fresh install bearer auth status=%d", legacyResponse.Code)
	}
	if strings.Contains(string(mustConfig(t, ctrl.ConfigPath())), payload["password"]) {
		t.Fatal("plaintext password persisted")
	}
	if !verifyPassword(ctrl.Snapshot().Server.AdminPasswordHash, payload["password"]) {
		t.Fatal("stored Argon2id password hash did not verify")
	}
	login := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"correct horse battery staple"}`))
	h.ServeHTTP(login, loginReq)
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d", login.Code)
	}
	cookies := login.Result().Cookies()
	authReq := httptest.NewRequest(http.MethodGet, "/api/v1/system", nil)
	authReq.AddCookie(cookies[0])
	authResp := httptest.NewRecorder()
	h.ServeHTTP(authResp, authReq)
	if authResp.Code != http.StatusOK {
		t.Fatalf("session auth status=%d", authResp.Code)
	}
	csrfDenied := httptest.NewRecorder()
	csrfReq := httptest.NewRequest(http.MethodPut, "/api/v1/launcher", strings.NewReader(`{"items":[]}`))
	csrfReq.AddCookie(cookies[0])
	h.ServeHTTP(csrfDenied, csrfReq)
	if csrfDenied.Code != http.StatusForbidden {
		t.Fatalf("state change without CSRF token status=%d", csrfDenied.Code)
	}
	connectionClosed := false
	unregister := server.registerSessionConnection(cookies[0].Value, func() { connectionClosed = true })
	defer unregister()
	changeReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", strings.NewReader(`{"current":"correct horse battery staple","next":"a new correct horse battery","confirm":"a new correct horse battery"}`))
	changeReq.AddCookie(cookies[0])
	changeReq.AddCookie(cookies[1])
	changeReq.Header.Set("X-RunPilot-CSRF", cookies[1].Value)
	change := httptest.NewRecorder()
	h.ServeHTTP(change, changeReq)
	if change.Code != http.StatusOK {
		t.Fatalf("password change status=%d body=%s", change.Code, change.Body.String())
	}
	if !connectionClosed {
		t.Fatal("password change did not close session-bound connections")
	}
	deniedAfter := httptest.NewRecorder()
	h.ServeHTTP(deniedAfter, authReq)
	if deniedAfter.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status=%d", deniedAfter.Code)
	}
	loginNew := httptest.NewRecorder()
	h.ServeHTTP(loginNew, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"a new correct horse battery"}`)))
	if loginNew.Code != http.StatusOK {
		t.Fatalf("new password login status=%d", loginNew.Code)
	}
	newCookies := loginNew.Result().Cookies()
	logoutReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logoutReq.AddCookie(newCookies[0])
	logoutReq.AddCookie(newCookies[1])
	logoutReq.Header.Set("X-RunPilot-CSRF", newCookies[1].Value)
	logout := httptest.NewRecorder()
	h.ServeHTTP(logout, logoutReq)
	if logout.Code != http.StatusOK {
		t.Fatalf("logout status=%d", logout.Code)
	}
}

func TestSetupHidesEditableDataDirWhenStartupOverrideIsFixed(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	server.SetDataRootSwitchAllowed(false)
	page := httptest.NewRecorder()
	server.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if strings.Contains(page.Body.String(), `name="dataDir"`) || !strings.Contains(page.Body.String(), "fixed by startup configuration") {
		t.Fatal("setup should display the fixed data directory without an editable field")
	}
}

func mustConfig(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLoginRateLimitAndSessionExpiry(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	hash, err := hashPassword("long enough test password")
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.ConfigureAdmin("admin", hash); err != nil {
		t.Fatal(err)
	}
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	server.loginFailures["192.0.2.1"] = loginFailure{start: time.Now(), count: 10}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"wrong password"}`))
	resp := httptest.NewRecorder()
	server.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limit status=%d", resp.Code)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	id := server.issueSession(w, r)
	if id == "" {
		t.Fatal("session issue failed")
	}
	server.sessionMu.Lock()
	item := server.sessions[id]
	item.expires = time.Now().Add(-time.Second)
	server.sessions[id] = item
	server.sessionMu.Unlock()
	auth := httptest.NewRequest(http.MethodGet, "/api/v1/system", nil)
	auth.AddCookie(w.Result().Cookies()[0])
	denied := httptest.NewRecorder()
	server.Handler().ServeHTTP(denied, auth)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("expired session status=%d", denied.Code)
	}
}

func TestExpiredSetupWindowRequiresServiceRestart(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	restartRequested := false
	server.restartHandler = func() { restartRequested = true }
	server.startSetupWindow(-time.Second)
	if server.setupWindowOpen() {
		t.Fatal("expired setup window remains open")
	}
	for _, target := range []string{"/", "/setup"} {
		page := httptest.NewRecorder()
		server.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, target, nil))
		if page.Code != http.StatusGone || !strings.Contains(page.Body.String(), "Setup window expired") || strings.Contains(page.Body.String(), `id="setup"`) {
			t.Fatalf("expired setup page %s status=%d body=%s", target, page.Code, page.Body.String())
		}
	}
	response := httptest.NewRecorder()
	body := strings.NewReader(`{"username":"admin","password":"correct horse battery staple","confirm":"correct horse battery staple"}`)
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/setup", body))
	if response.Code != http.StatusGone || !strings.Contains(response.Body.String(), "restart the RunPilot service") {
		t.Fatalf("expired setup status=%d body=%s", response.Code, response.Body.String())
	}
	if restartRequested {
		t.Fatal("setup expiry restarted the application")
	}
}

func TestInitialSetupCopiesToSelectedDataRootAndPersistsLocator(t *testing.T) {
	base := t.TempDir()
	t.Setenv("RUNPILOT_DATA_ROOT_FILE", filepath.Join(base, "bootstrap", "data-root"))
	source := filepath.Join(base, "source")
	if err := config.WriteDataRoot(source); err != nil {
		t.Fatal(err)
	}
	ctrl, err := core.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "managed-state.txt"), []byte("retain me"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	server.SetRestartHandler(func() {})
	selected := filepath.Join(base, "volume", "RunPilot")
	if err := os.Mkdir(filepath.Dir(selected), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := map[string]string{"username": "admin", "password": "another correct horse battery", "confirm": "another correct horse battery", "dataDir": selected}
	body, _ := json.Marshal(payload)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("selected-root setup status=%d body=%s", response.Code, response.Body.String())
	}
	ctrl.Close()
	if err := config.ApplyPendingDataRoot(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(selected, "managed-state.txt"))
	if err != nil || string(got) != "retain me" {
		t.Fatalf("copied state=%q err=%v", got, err)
	}
	store, err := config.Open(selected)
	if err != nil {
		t.Fatal(err)
	}
	if store.Snapshot().Server.AdminUsername != "admin" || !verifyPassword(store.Snapshot().Server.AdminPasswordHash, payload["password"]) {
		t.Fatal("selected root did not receive administrator credentials")
	}
	resolved, err := config.ResolveDataDir("")
	if err != nil || resolved != selected {
		t.Fatalf("persistent locator resolved %q err=%v", resolved, err)
	}
}

func TestConfigDirDoesNotPinSetupDataDirectory(t *testing.T) {
	base := t.TempDir()
	t.Setenv("RUNPILOT_DATA_ROOT_FILE", filepath.Join(base, "bootstrap", "data-root"))
	source := filepath.Join(base, "source")
	configDir := filepath.Join(base, "config")
	if err := config.WriteDataRoot(source); err != nil {
		t.Fatal(err)
	}
	ctrl, err := core.OpenWithConfigDir(source, configDir)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	page := httptest.NewRecorder()
	server.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if !strings.Contains(page.Body.String(), `name="dataDir"`) {
		t.Fatal("--config-dir should leave the setup data-directory choice available")
	}
	selected := filepath.Join(base, "selected-data")
	payload := map[string]string{"username": "admin", "password": "another correct horse battery", "confirm": "another correct horse battery", "dataDir": selected}
	body, _ := json.Marshal(payload)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("setup status=%d body=%s", response.Code, response.Body.String())
	}
	if ctrl.ConfigPath() != filepath.Join(configDir, "runpilot.yaml") {
		t.Fatalf("config path changed during data-directory setup: %s", ctrl.ConfigPath())
	}
	ctrl.Close()
	if err := config.ApplyPendingDataRoot(); err != nil {
		t.Fatal(err)
	}
	store, err := config.OpenWithConfigDir(selected, configDir)
	if err != nil {
		t.Fatal(err)
	}
	if store.Snapshot().Server.AdminUsername != "admin" {
		t.Fatal("administrator configuration was not retained in the configured config directory")
	}
}
