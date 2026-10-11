//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppCentralLifecyclePreservesDataAcrossBinaryUpgrade(t *testing.T) {
	h := newLifecycleHarness(t)
	h.installBinary("v1")
	t.Cleanup(h.stop)

	if output := h.run("start"); output != "" {
		t.Fatalf("start output = %q", output)
	}
	if output := h.run("status"); output != "RunPilot is running\n" {
		t.Fatalf("status output = %q", output)
	}
	if got, err := os.ReadFile(filepath.Join(h.dataRoot, ".runpilot-owned")); err != nil || len(got) != 0 {
		t.Fatalf("ownership marker = %q, %v", got, err)
	}

	config := filepath.Join(h.dataDir, "runpilot.yaml")
	pluginData := filepath.Join(h.dataDir, "plugins", "tasks", "data", "tasks.json")
	history := filepath.Join(h.dataDir, "plugins", "tasks", "data", "history.db")
	for path, contents := range map[string]string{config: "token: test-token\n", pluginData: "task-state", history: "history-state"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if output := h.run("stop"); output != "" {
		t.Fatalf("stop output = %q", output)
	}
	if output := h.runExpectFailure("status"); !strings.Contains(output, "RunPilot is stopped") {
		t.Fatalf("stopped status output = %q", output)
	}
	h.installBinary("v2")
	if output := h.run("start"); output != "" {
		t.Fatalf("start after upgrade output = %q", output)
	}
	if output := h.run("status"); output != "RunPilot is running\n" {
		t.Fatalf("post-upgrade status output = %q", output)
	}
	for path, want := range map[string]string{config: "token: test-token\n", pluginData: "task-state", history: "history-state"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Errorf("persistent file %s = %q, %v; want %q", path, got, err, want)
		}
	}
	if got, err := os.ReadFile(h.binaryVersions); err != nil || string(got) != "v1\nv2\n" {
		t.Errorf("binary versions invoked = %q, %v; want v1 then v2", got, err)
	}
	callLog, err := os.ReadFile(h.callLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-c admin:administrators", "-- run --bind-address 0.0.0.0 --port 9070", "-s TERM"} {
		if !strings.Contains(string(callLog), want) {
			t.Errorf("start-stop-daemon calls missing %q:\n%s", want, callLog)
		}
	}
	if output := h.run("stop"); output != "" {
		t.Fatalf("final stop output = %q", output)
	}
}

func TestAppCentralLifecycleRefusesUnmarkedOrSymlinkData(t *testing.T) {
	h := newLifecycleHarness(t)
	h.installBinary("v1")
	if err := os.MkdirAll(h.dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if output := h.runExpectFailure("start"); !strings.Contains(output, "Refusing to use unmarked data directory") {
		t.Fatalf("unmarked data start output = %q", output)
	}
	if _, err := os.Stat(filepath.Join(h.dataRoot, ".mock-running")); !os.IsNotExist(err) {
		t.Fatalf("unmarked directory started RunPilot: %v", err)
	}

	if err := os.Remove(h.dataRoot); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(h.root, "foreign-data")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, h.dataRoot); err != nil {
		t.Fatal(err)
	}
	if output := h.runExpectFailure("start"); !strings.Contains(output, "Refusing to use unmarked data directory") {
		t.Fatalf("symlink data start output = %q", output)
	}
}

func TestAppCentralLifecycleUsesPersistedCustomDataRoot(t *testing.T) {
	h := newLifecycleHarness(t)
	h.installBinary("v1")
	t.Cleanup(h.stop)
	custom := filepath.Join(h.root, "volume2", "RunPilot")
	if err := os.MkdirAll(custom, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.locator, []byte(custom+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output := h.run("start"); output != "" {
		t.Fatalf("start output=%q", output)
	}
	if _, err := os.Stat(filepath.Join(custom, ".mock-running")); err != nil {
		t.Fatalf("custom root was not used: %v", err)
	}
	if _, err := os.Stat(filepath.Join(custom, ".runpilot-owned")); !os.IsNotExist(err) {
		t.Fatalf("lifecycle changed custom-root ownership metadata: %v", err)
	}
	if output := h.run("stop"); output != "" {
		t.Fatalf("stop output=%q", output)
	}
	if got, err := os.ReadFile(h.locator); err != nil || strings.TrimSpace(string(got)) != custom {
		t.Fatalf("custom root locator=%q err=%v", got, err)
	}
}

type lifecycleHarness struct {
	t              *testing.T
	root           string
	appDir         string
	dataRoot       string
	dataDir        string
	locator        string
	callLog        string
	binaryVersions string
	control        string
	path           string
}

func newLifecycleHarness(t *testing.T) *lifecycleHarness {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("POSIX shell is unavailable")
	}
	root := t.TempDir()
	appDir := filepath.Join(root, "App Central package")
	metaDir := filepath.Join(root, "persistent metadata")
	dataRoot := filepath.Join(metaDir, "data")
	dataDir := dataRoot
	binDir := filepath.Join(root, "mock ADM commands")
	for _, dir := range []string{filepath.Join(appDir, "bin"), binDir, metaDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	control, err := os.ReadFile(filepath.Join("..", "..", "packaging", "asustor", "CONTROL", "start-stop.sh"))
	if err != nil {
		t.Fatal(err)
	}
	const originalRoot = "META_DIR=/volume1/.@plugins/AppCentral/runpilot"
	if !strings.Contains(string(control), originalRoot) {
		t.Fatal("App Central hook data root changed without updating this test")
	}
	controlPath := filepath.Join(root, "start-stop.sh")
	controlContents := strings.Replace(string(control), originalRoot, "META_DIR='"+filepath.Dir(dataDir)+"'", 1)
	if err := os.WriteFile(controlPath, []byte(controlContents), 0o700); err != nil {
		t.Fatal(err)
	}

	h := &lifecycleHarness{
		t:              t,
		root:           root,
		appDir:         appDir,
		dataRoot:       dataRoot,
		dataDir:        dataDir,
		locator:        filepath.Join(metaDir, "data-root"),
		callLog:        filepath.Join(root, "daemon-calls.log"),
		binaryVersions: filepath.Join(root, "binary-versions.log"),
		control:        controlPath,
		path:           binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
	writeExecutable(t, filepath.Join(binDir, "chown"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "start-stop-daemon"), mockStartStopDaemon)
	t.Setenv("CALL_LOG", h.callLog)
	t.Setenv("DATA_DIR", h.dataDir)
	t.Setenv("BINARY_VERSIONS", h.binaryVersions)
	return h
}

const mockStartStopDaemon = `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$CALL_LOG"
mode=
pid_file=
executable=
signal=TERM
query=false
while [ "$#" -gt 0 ]; do
	case "$1" in
		-S|-K) mode=$1; shift ;;
		-p) pid_file=$2; shift 2 ;;
		-x) executable=$2; shift 2 ;;
		-s) signal=$2; shift 2 ;;
		-t) query=true; shift ;;
		-c|-u) shift 2 ;;
		-q|-b|-m|-o) shift ;;
		--) shift; break ;;
		*) shift ;;
	esac
done
case "$mode" in
	-S)
		"$executable" "$@" &
		printf '%s\n' "$!" > "$pid_file"
		: > "$DATA_DIR/.mock-running"
		;;
	-K)
		[ -f "$DATA_DIR/.mock-running" ] && [ -f "$pid_file" ] || exit 1
		pid=$(cat "$pid_file")
		kill -0 "$pid" 2>/dev/null || { [ "$query" = false ] && exit 1; rm -f "$DATA_DIR/.mock-running"; exit 1; }
		if [ "$query" = false ]; then
			kill -s "$signal" "$pid" 2>/dev/null || true
			rm -f "$DATA_DIR/.mock-running"
		fi
		;;
	*) exit 2 ;;
esac
`

func (h *lifecycleHarness) installBinary(version string) {
	h.t.Helper()
	contents := "#!/bin/sh\nprintf '%s\\n' '" + version + "' >> \"$BINARY_VERSIONS\"\nexec sleep 60\n"
	writeExecutable(h.t, filepath.Join(h.appDir, "bin", "runpilot"), contents)
}

func (h *lifecycleHarness) run(action string) string {
	h.t.Helper()
	return h.invoke(action, false)
}

func (h *lifecycleHarness) runExpectFailure(action string) string {
	h.t.Helper()
	return h.invoke(action, true)
}

func (h *lifecycleHarness) invoke(action string, wantFailure bool) string {
	h.t.Helper()
	cmd := exec.Command("sh", h.control, action)
	cmd.Env = append(os.Environ(), "APKG_PKG_NAME=runpilot", "APKG_PKG_DIR="+h.appDir, "PATH="+h.path)
	output, err := cmd.CombinedOutput()
	if wantFailure {
		if err == nil {
			h.t.Fatalf("hook %s unexpectedly succeeded", action)
		}
	} else if err != nil {
		h.t.Fatalf("hook %s failed: %v\n%s", action, err, output)
	}
	return string(output)
}

func (h *lifecycleHarness) stop() {
	pidPath := filepath.Join(h.dataRoot, "runpilot.pid")
	if _, err := os.Stat(pidPath); err == nil {
		_ = h.invoke("stop", false)
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}
