package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/szilab/RunPilot/internal/core"
)

func TestDownloadTicketAtRootUsesLocalPath(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.bin"), []byte{1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	path := "root" + filepath.ToSlash(root) + "/report.bin"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage/local/download-ticket", bytes.NewBufferString(`{"path":"`+path+`"}`))
	req.Header.Set("Authorization", "Bearer "+ctrl.Snapshot().Server.Token)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("ticket status = %d: %s", response.Code, response.Body.String())
	}
	var ticket struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(response.Body).Decode(&ticket); err != nil {
		t.Fatal(err)
	}
	if len(ticket.URL) < 5 || ticket.URL[:5] != "/api/" {
		t.Fatalf("ticket URL = %q", ticket.URL)
	}
	download := httptest.NewRequest(http.MethodGet, ticket.URL, nil)
	got := httptest.NewRecorder()
	s.Handler().ServeHTTP(got, download)
	if got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), []byte{1, 2, 3}) {
		t.Fatalf("download = %d, %v", got.Code, got.Body.Bytes())
	}
}

func TestStorageTextEndpointCreatesNewLocalFile(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	root := t.TempDir()
	s, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	path := "root" + filepath.ToSlash(root) + "/new-note.txt"
	request := httptest.NewRequest(http.MethodPut, "/api/v1/storage/local/text", bytes.NewBufferString(`{"path":"`+path+`","content":"hello"}`))
	request.Header.Set("Authorization", "Bearer "+ctrl.Snapshot().Server.Token)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("create status = %d: %s", response.Code, response.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(root, "new-note.txt"))
	if err != nil || string(content) != "hello" {
		t.Fatalf("created file = %q, %v", content, err)
	}
}

func TestStorageLocationsAreRuntimeOnly(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	s, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	token := ctrl.Snapshot().Server.Token
	request := httptest.NewRequest(http.MethodGet, "/api/v1/storage", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"id":"local"`)) {
		t.Fatalf("locations = %d %s", response.Code, response.Body.String())
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		request = httptest.NewRequest(method, "/api/v1/storage/unused", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response = httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s storage CRUD status = %d", method, response.Code)
		}
	}
}

func TestSoftwareProviderConfigurationRequiresAuth(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	s, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRequest(http.MethodPut, "/api/v1/software/providers/scoop", bytes.NewBufferString(`{"scoop":{"root":"C:\\RunPilotSoftware"}}`))
	r := httptest.NewRecorder()
	s.Handler().ServeHTTP(r, unauthorized)
	if r.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", r.Code)
	}
	authorized := httptest.NewRequest(http.MethodPut, "/api/v1/software/providers/scoop", bytes.NewBufferString(`{"scoop":{"root":"C:\\RunPilotSoftware"}}`))
	authorized.Header.Set("Authorization", "Bearer "+ctrl.Snapshot().Server.Token)
	r = httptest.NewRecorder()
	s.Handler().ServeHTTP(r, authorized)
	wantStatus := http.StatusOK
	if runtime.GOOS != "windows" {
		wantStatus = http.StatusNotFound
	}
	if r.Code != wantStatus {
		t.Fatalf("configuration status = %d: %s", r.Code, r.Body.String())
	}
	if runtime.GOOS != "windows" {
		return
	}
	var provider struct{ ID string }
	if err := json.NewDecoder(r.Body).Decode(&provider); err != nil {
		t.Fatal(err)
	}
	if provider.ID != "scoop" {
		t.Fatalf("provider = %#v", provider)
	}
	unauthorizedBuckets := httptest.NewRequest(http.MethodGet, "/api/v1/software/providers/scoop/buckets", nil)
	r = httptest.NewRecorder()
	s.Handler().ServeHTTP(r, unauthorizedBuckets)
	if r.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated bucket status = %d", r.Code)
	}
}
