package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/model"
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
	d, err := ctrl.UpsertStorage(model.StorageDefinition{Name: "test", Type: model.StorageLocal, Local: &model.LocalStorageSpec{Scope: model.LocalStorageScopeRoot, Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage/"+d.ID+"/download-ticket", bytes.NewBufferString(`{"path":"report.bin"}`))
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
