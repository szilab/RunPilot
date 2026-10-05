package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/model"
)

func TestDashboardStatusAndLauncherAPI(t *testing.T) {
	ctrl, err := core.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	server, err := New(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+ctrl.Snapshot().Server.Token)
		result := httptest.NewRecorder()
		server.Handler().ServeHTTP(result, req)
		return result
	}
	status := request("GET", "/api/v1/dashboard/status", "")
	if status.Code != 200 {
		t.Fatalf("status: %d %s", status.Code, status.Body.String())
	}
	var data struct {
		Host              model.HostStatus `json:"host"`
		HostUptimeSeconds int64            `json:"hostUptimeSeconds"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.Host.OS == "" || data.Host.CPUCount < 1 || data.HostUptimeSeconds < 0 {
		t.Fatalf("bad status: %+v", data)
	}
	saved := request("PUT", "/api/v1/launcher", `{"entries":[{"name":"Docs","url":"https://example.com","openMode":"external"}]}`)
	if saved.Code != 200 {
		t.Fatalf("save: %d %s", saved.Code, saved.Body.String())
	}
	var body struct {
		Entries []model.LauncherEntry `json:"entries"`
	}
	if err := json.Unmarshal(saved.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 1 || body.Entries[0].ID == "" {
		t.Fatalf("bad save: %+v", body)
	}
	read := request("GET", "/api/v1/launcher", "")
	if read.Code != 200 || !bytes.Contains(read.Body.Bytes(), []byte(body.Entries[0].ID)) {
		t.Fatalf("bad read: %s", read.Body.String())
	}
	denied := httptest.NewRecorder()
	server.Handler().ServeHTTP(denied, httptest.NewRequest("GET", "/api/v1/launcher", nil))
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", denied.Code)
	}
}
