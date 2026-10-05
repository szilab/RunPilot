package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/szilab/RunPilot/internal/config"
	"github.com/szilab/RunPilot/internal/core"
	"github.com/szilab/RunPilot/internal/httpgateway"
	"github.com/szilab/RunPilot/internal/model"
	"github.com/szilab/RunPilot/internal/websocketsecure"
)

type openedGateway struct {
	Ticket  string `json:"ticket"`
	Session struct {
		ID            string `json:"id"`
		PublicationID string `json:"publicationId"`
		PublicPrefix  string `json:"publicPrefix"`
	} `json:"session"`
}

func webAppsController(t *testing.T, base string, dataDirs ...string) *core.Controller {
	t.Helper()
	dir := ""
	if len(dataDirs) > 0 {
		dir = dataDirs[0]
	} else {
		dir = t.TempDir()
	}
	source := filepath.Join("..", "..", "plugins", "web-apps")
	dest := filepath.Join(dir, "plugins", "web.apps", "0.1.0")
	if err := filepath.WalkDir(source, func(filename string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(source, filename)
		if e != nil {
			return e
		}
		if strings.HasPrefix(rel, "backend"+string(filepath.Separator)) && rel != filepath.Join("backend", "plugin.wasm") {
			return nil
		}
		data, e := os.ReadFile(filename)
		if e != nil {
			return e
		}
		out := filepath.Join(dest, rel)
		if e = os.MkdirAll(filepath.Dir(out), 0755); e != nil {
			return e
		}
		return os.WriteFile(out, data, 0644)
	}); err != nil {
		t.Fatal(err)
	}
	store, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Update(func(cfg *model.Config) error {
		enabled := true
		cfg.Plugins = map[string]model.PluginSettings{"web.apps": {Enabled: &enabled}}
		cfg.Server.BasePath = base
		cfg.Server.WebSocketPayloadMode = "disabled"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c, err := core.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
func appsRPC(t *testing.T, c *core.Controller, method string, params any) json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(params)
	result, f := c.PluginCall(context.Background(), "web.apps", method, raw)
	if f != nil {
		t.Fatal(f)
	}
	if bytes.Contains(result, []byte(`"error"`)) {
		t.Fatalf("%s: %s", method, result)
	}
	return result
}
func openWebApp(t *testing.T, c *core.Controller, upstream string, upstreamBases ...string) openedGateway {
	t.Helper()
	upstreamBase := "/app"
	if len(upstreamBases) > 0 {
		upstreamBase = upstreamBases[0]
	}
	appsRPC(t, c, "apps.targets.save", map[string]any{"target": map[string]string{"id": "", "name": "Test app", "mountPath": "/app", "upstreamURL": upstream, "upstreamBasePath": upstreamBase}})
	var opened openedGateway
	if err := json.Unmarshal(appsRPC(t, c, "apps.session.open", map[string]string{"targetId": "app-1"}), &opened); err != nil || opened.Ticket == "" {
		t.Fatal(opened, err)
	}
	return opened
}
func dialGateway(t *testing.T, url string, opened openedGateway) (*websocket.Conn, *websocketsecure.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	raw, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(url, "http")+"/api/v1/ws?ticket="+opened.Ticket, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.CloseNow() })
	key, hello, random, err := websocketsecure.NewClientHello()
	if err != nil {
		t.Fatal(err)
	}
	if err = raw.Write(ctx, websocket.MessageBinary, hello); err != nil {
		t.Fatal(err)
	}
	_, serverHello, err := raw.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secure, err := websocketsecure.ClientSession(raw, key, random, serverHello)
	if err != nil {
		t.Fatal(err)
	}
	return raw, secure, ctx
}
func TestWebAppsWASMEncryptedGatewayAndPublication(t *testing.T) {
	for _, base := range []string{"/", "/p", "/tenant/pilot"} {
		t.Run(base, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/app/" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "text/html")
				_, _ = io.WriteString(w, `<h1>upstream HTML</h1><script src="app.js"></script>`)
			}))
			defer upstream.Close()
			c := webAppsController(t, base)
			opened := openWebApp(t, c, upstream.URL)
			s, err := New(c, base)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(s.Handler())
			defer server.Close()
			prefix := strings.TrimSuffix(base, "/")
			req, _ := http.NewRequest("GET", server.URL+opened.Session.PublicPrefix+"/", nil)
			req.Header.Set("Accept", "text/html")
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if bytes.Contains(data, []byte("upstream HTML")) || !bytes.Contains(data, []byte("RUNPILOT_PUBLICATION")) {
				t.Fatal("HTTP exposed application data", string(data))
			}
			response, err = http.Get(server.URL + opened.Session.PublicPrefix + "/app.js")
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != 503 {
				t.Fatal("resource fell through to HTTP", response.StatusCode)
			}
			response, err = http.Get(server.URL + opened.Session.PublicPrefix + "/__runpilot__/sw.js?publication=" + opened.Session.PublicationID)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.Header.Get("Service-Worker-Allowed") != opened.Session.PublicPrefix+"/" {
				t.Fatal("broad SW scope", response.Header)
			}
			response, err = http.Get(server.URL + opened.Session.PublicPrefix + "/__runpilot__/sw.js?publication=stale")
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != 410 {
				t.Fatal("stale publication accepted")
			}
			var foreign openedGateway
			json.Unmarshal(appsRPC(t, c, "apps.session.open", map[string]string{"targetId": "app-1"}), &foreign)
			_, secure, ctx := dialGateway(t, server.URL+prefix, opened)
			rpc := []byte(`{"id":"x","plugin":"terminal","method":"terminal.open","params":{}}`)
			if secure.Write(ctx, websocket.MessageText, rpc) != nil {
				t.Fatal("write failed")
			}
			_, data, err = secure.Read(ctx)
			if err != nil || !bytes.Contains(data, []byte("forbidden")) {
				t.Fatal("scoped ticket allowed RPC", string(data), err)
			}
			for _, control := range []map[string]string{{"type": "stream.attach", "plugin": "remote.vnc", "streamId": opened.Session.ID}, {"type": "stream.attach", "plugin": "web.apps", "streamId": foreign.Session.ID}} {
				raw, _ := json.Marshal(control)
				_ = secure.Write(ctx, websocket.MessageText, raw)
				_, data, err = secure.Read(ctx)
				if err != nil || !bytes.Contains(data, []byte("forbidden")) {
					t.Fatal("foreign stream attached", string(data), err)
				}
			}
			attach, _ := json.Marshal(map[string]string{"type": "stream.attach", "plugin": "web.apps", "streamId": opened.Session.ID})
			_ = secure.Write(ctx, websocket.MessageText, attach)
			_, data, err = secure.Read(ctx)
			if err != nil || !bytes.Contains(data, []byte("stream.attached")) {
				t.Fatal(string(data), err)
			}
			send := func(kind byte, data []byte) {
				var b bytes.Buffer
				if err := httpgateway.WriteFrame(&b, httpgateway.Frame{Type: kind, ID: 1, Data: data}); err != nil {
					t.Fatal(err)
				}
				frame := encodeApplicationStreamFrame(opened.Session.ID, b.Bytes())
				frame[4] = 1
				if err := secure.Write(ctx, websocket.MessageBinary, frame); err != nil {
					t.Fatal(err)
				}
			}
			meta, _ := json.Marshal(httpgateway.Request{Method: "GET", Path: opened.Session.PublicPrefix + "/"})
			send(httpgateway.Start, meta)
			send(httpgateway.End, nil)
			send(httpgateway.DownloadCredit, httpgateway.Credit(httpgateway.Window))
			var buffer []byte
			var body bytes.Buffer
			finished := false
			for !finished {
				kind, raw, err := secure.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if kind != websocket.MessageBinary {
					continue
				}
				_, payload, err := decodeApplicationStreamFrame(raw, 2)
				if err != nil {
					t.Fatal(err)
				}
				buffer = append(buffer, payload...)
				for len(buffer) >= 10 {
					length := int(uint32(buffer[6])<<24 | uint32(buffer[7])<<16 | uint32(buffer[8])<<8 | uint32(buffer[9]))
					if len(buffer) < 10+length {
						break
					}
					frame, err := httpgateway.ReadFrame(bytes.NewReader(buffer[:10+length]))
					if err != nil {
						t.Fatal(err)
					}
					buffer = buffer[10+length:]
					switch frame.Type {
					case httpgateway.ResponseBody:
						body.Write(frame.Data)
					case httpgateway.ResponseEnd:
						finished = true
					case httpgateway.ResponseError:
						t.Fatal(string(frame.Data))
					}
				}
			}
			if !strings.Contains(body.String(), "upstream HTML") {
				t.Fatal(body.String())
			}
			// A ticket has no REST authority and cannot be replayed at the common socket.
			req, _ = http.NewRequest("GET", server.URL+prefix+"/api/v1/system", nil)
			req.Header.Set("Authorization", "Bearer "+opened.Ticket)
			response, err = http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != 401 {
				t.Fatal("ticket became REST auth")
			}
			_, res, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL+prefix, "http")+"/api/v1/ws?ticket="+opened.Ticket, nil)
			if err == nil || res.StatusCode != 401 {
				t.Fatal("ticket replay accepted", err)
			}
			appsRPC(t, c, "apps.session.close", map[string]string{"id": opened.Session.ID})
			appsRPC(t, c, "apps.session.close", map[string]string{"id": foreign.Session.ID})
			appsRPC(t, c, "apps.targets.delete", map[string]string{"id": "app-1"})
			if _, ok := c.BrowserPublication("/app/"); ok {
				t.Fatal("deleted publication remains")
			}
		})
	}
}
func TestWebAppsRejectPlaintextEvenWhenNormalWebSocketEncryptionDisabled(t *testing.T) {
	c := webAppsController(t, "/")
	opened := openWebApp(t, c, "http://127.0.0.1:1234")
	s, _ := New(c)
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/ws?ticket="+opened.Ticket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.CloseNow()
	_ = raw.Write(ctx, websocket.MessageText, []byte(`{"type":"stream.attach","plugin":"web.apps","streamId":"`+opened.Session.ID+`"}`))
	if _, _, err = raw.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatal("plaintext accepted", err)
	}
}
func TestWebAppsFrontendContracts(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node required for frontend contracts")
	}
	root := filepath.Join("..", "..")
	for _, script := range []string{"internal/web/auth_storage_test.js", "internal/web/secure_websocket_test.js", "plugins/web-apps/web/tunnel.test.cjs", "plugins/web-apps/web/frontend.test.mjs"} {
		cmd := exec.Command(node, script)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", script, err, out)
		}
	}
}

func TestWebAppsUseActualListenerBasePathOverride(t *testing.T) {
	c := webAppsController(t, "/")
	s, err := New(c, "/cli/override/")
	if err != nil {
		t.Fatal(err)
	}
	opened := openWebApp(t, c, "http://127.0.0.1:1234")
	if opened.Session.PublicPrefix != "/cli/override/app" {
		t.Fatal("gateway ignored listener override", opened.Session.PublicPrefix)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/cli/override/app/", nil)
	request.Header.Set("Accept", "text/html")
	s.Handler().ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"publicPrefix":"/cli/override/app"`) {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestWebAppsRestartKeepsTargetsButDropsRuntimeSessions(t *testing.T) {
	dir := t.TempDir()
	var opened openedGateway
	var oldStream io.Reader
	t.Run("initial", func(t *testing.T) {
		c := webAppsController(t, "/", dir)
		opened = openWebApp(t, c, "http://127.0.0.1:1234")
		conn, _, err := c.AttachBrowserStream("web.apps", opened.Session.ID)
		if err != nil {
			t.Fatal(err)
		}
		oldStream = conn
	}) // Controller shutdown drains all gateway goroutines before returning.
	if _, err := oldStream.Read(make([]byte, 1)); err == nil {
		t.Fatal("host shutdown left gateway open")
	}
	t.Run("restart", func(t *testing.T) {
		c := webAppsController(t, "/", dir)
		list := appsRPC(t, c, "apps.targets.list", nil)
		if !bytes.Contains(list, []byte(`"id":"app-1"`)) {
			t.Fatal("target lost", string(list))
		}
		sessions := appsRPC(t, c, "apps.sessions.list", nil)
		if !bytes.Contains(sessions, []byte(`"sessions":[]`)) {
			t.Fatal("runtime session persisted", string(sessions))
		}
		p, ok := c.BrowserPublication("/app/")
		if !ok || p.ID == opened.Session.PublicationID {
			t.Fatal("publication generation retained")
		}
		if _, ok := c.ConsumeBrowserStreamTicket(opened.Ticket); ok {
			t.Fatal("bootstrap credential retained across restart")
		}
	})
}
