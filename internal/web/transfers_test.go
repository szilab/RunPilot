package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceTransferRoutesStreamFilesSafely(t *testing.T) {
	ctrl, err := openReadyController(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "page.html"), []byte("<script>alert(1)</script> and more"), 0o644); err != nil {
		t.Fatal(err)
	}
	token := ctrl.Snapshot().Server.Token
	issue := func(kind string, params map[string]any) string {
		t.Helper()
		raw, _ := json.Marshal(params)
		value, err := ctrl.IssueWorkspaceTransfer("files", kind, raw)
		if err != nil {
			t.Fatal(err)
		}
		var issued struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(value, &issued)
		return "/" + issued.URL
	}
	request := func(method, path string, body string, authorized bool, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if authorized {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, req)
		return recorder
	}

	download := issue("download", map[string]any{"root": root, "path": "page.html", "inline": true})
	if response := request(http.MethodGet, download, "", false, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous download = %d", response.Code)
	}
	response := request(http.MethodGet, download, "", true, nil)
	if response.Code != http.StatusOK || response.Body.String() != "<script>alert(1)</script> and more" {
		t.Fatalf("download = %d %q", response.Code, response.Body.String())
	}
	header := response.Header()
	if header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(header.Get("Content-Disposition"), "attachment") || header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("download headers = %v", header)
	}
	partial := request(http.MethodGet, download, "", true, map[string]string{"Range": "bytes=0-7"})
	if partial.Code != http.StatusPartialContent || partial.Body.String() != "<script>" {
		t.Fatalf("ranged download = %d %q", partial.Code, partial.Body.String())
	}
	if response := request(http.MethodGet, "/api/v1/transfers/unknown", "", true, nil); response.Code != http.StatusNotFound {
		t.Fatalf("unknown ticket = %d", response.Code)
	}

	upload := issue("upload", map[string]any{"root": root, "directory": "", "name": "uploaded.txt"})
	if response := request(http.MethodPut, upload, "body", false, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous upload = %d", response.Code)
	}
	if response := request(http.MethodPut, upload, "uploaded body", true, nil); response.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", response.Code, response.Body.String())
	}
	if data, _ := os.ReadFile(filepath.Join(root, "uploaded.txt")); string(data) != "uploaded body" {
		t.Fatalf("uploaded content = %q", data)
	}
	if response := request(http.MethodPut, upload, "again", true, nil); response.Code != http.StatusNotFound {
		t.Fatalf("reused upload ticket = %d", response.Code)
	}
	conflict := issue("upload", map[string]any{"root": root, "name": "uploaded.txt"})
	if response := request(http.MethodPut, conflict, "x", true, nil); response.Code != http.StatusConflict {
		t.Fatalf("conflicting upload = %d", response.Code)
	}
}
