package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/plugins"
)

func dockerPackage(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..", "plugins", "docker")
	name := filepath.Join(t.TempDir(), "docker.rpplugin")
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		relative, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		entry, e := archive.Create(filepath.ToSlash(relative))
		if e != nil {
			return e
		}
		source, e := os.Open(path)
		if e != nil {
			return e
		}
		defer source.Close()
		_, e = io.Copy(entry, source)
		return e
	})
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return name
}
func TestDockerPluginABI2CommonWebSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Docker plugin is Linux only")
	}
	bin := t.TempDir()
	script := `#!/bin/sh
printf '%s|%s\n' "$PWD" "$*" >> "$RUNPILOT_DOCKER_TEST_LOG"
case "$1 $2" in
  'compose version') echo 'Docker Compose version v2.0.0';;
  'info ') echo 'ready';;
  'ps -a') ;;
  'compose ls') echo '[]';;
  'volume ls') ;;
  'network ls') ;;
  'ps -aq') ;;
  'compose --project-name') ;;
  *) echo "unexpected docker command: $*" >&2; exit 1;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	logPath := filepath.Join(t.TempDir(), "docker.log")
	t.Setenv("RUNPILOT_DOCKER_TEST_LOG", logPath)
	dataDir := t.TempDir()
	if _, err := plugins.InstallPackage(filepath.Join(dataDir, "plugins"), dockerPackage(t), ""); err != nil {
		t.Fatal(err)
	}
	store, err := config.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(cfg *model.Config) error {
		enabled := true
		cfg.Plugins = map[string]model.PluginSettings{"docker": {Enabled: &enabled}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	controller, err := core.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	server, err := New(controller)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/ws/ticket", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+controller.Snapshot().Server.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("ticket: %s", response.Status)
	}
	var ticket struct {
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(response.Body).Decode(&ticket); err != nil {
		t.Fatal(err)
	}
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/v1/ws?ticket="+ticket.Ticket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	call := func(id, method string, params any) json.RawMessage {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"id": id, "plugin": "docker", "method": method, "params": params})
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
			t.Fatal(err)
		}
		_, reply, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			ID     string          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  any             `json:"error"`
		}
		if err := json.Unmarshal(reply, &out); err != nil {
			t.Fatal(err)
		}
		if out.ID != id || out.Error != nil || bytes.Contains(out.Result, []byte(`"error"`)) {
			t.Fatalf("%s: %s", method, reply)
		}
		return out.Result
	}
	created := call("1", "docker.projects.create", map[string]string{"name": "jellyfin"})
	if !bytes.Contains(created, []byte(`"managed":true`)) {
		t.Fatalf("create: %s", created)
	}
	call("2", "docker.projects.file.set", map[string]string{"name": "jellyfin", "kind": "env", "content": "TOKEN=abc"})
	call("3", "docker.projects.action", map[string]string{"name": "jellyfin", "action": "up"})
	snapshot := call("4", "docker.snapshot", map[string]any{})
	if !bytes.Contains(snapshot, []byte(`"state":"ready"`)) || !bytes.Contains(snapshot, []byte(`"name":"jellyfin"`)) {
		t.Fatalf("snapshot: %s", snapshot)
	}
	content, err := os.ReadFile(filepath.Join(dataDir, "plugins", "docker", "data", "workspace", "projects", "jellyfin", ".env"))
	if err != nil || string(content) != "TOKEN=abc" {
		t.Fatalf("workspace file: %s %v", content, err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "projects/jellyfin|compose --project-name jellyfin -f compose.yaml up -d") {
		t.Fatalf("Compose was not run in plugin workspace: %s", log)
	}
}
