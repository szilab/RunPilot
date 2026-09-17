package dockercompose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/storage"
)

type call struct {
	name string
	args []string
}
type fakeRunner struct {
	found   bool
	results map[string]Result
	calls   []call
}

func (f *fakeRunner) LookPath(string) (string, error) {
	if f.found {
		return "docker", nil
	}
	return "", errors.New("not found")
}
func (f *fakeRunner) Run(_ context.Context, name string, args ...string) Result {
	f.calls = append(f.calls, call{name, append([]string(nil), args...)})
	return f.results[join(args)]
}
func join(args []string) string {
	out := ""
	for _, v := range args {
		out += "\x00" + v
	}
	return out
}
func response(args []string, output string, err error) map[string]Result {
	return map[string]Result{join(args): {Output: output, Err: err}}
}

func TestRuntimeStates(t *testing.T) {
	ctx := context.Background()
	missing := &fakeRunner{}
	if got := NewManagerForTest(t.TempDir(), missing, true).Runtime(ctx).State; got != "cli-missing" {
		t.Fatalf("got %s", got)
	}
	cases := []struct {
		name    string
		results map[string]Result
		state   string
	}{
		{"compose", response([]string{"compose", "version"}, "unknown command", errors.New("exit")), "compose-missing"},
		{"daemon", map[string]Result{join([]string{"compose", "version"}): {}, join([]string{"info"}): {Output: "Cannot connect", Err: errors.New("exit")}}, "daemon-unavailable"},
		{"permission", map[string]Result{join([]string{"compose", "version"}): {}, join([]string{"info"}): {Output: "permission denied while trying to connect", Err: errors.New("exit")}}, "permission-denied"},
		{"ready", map[string]Result{join([]string{"compose", "version"}): {}, join([]string{"info"}): {}}, "ready"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{found: true, results: tc.results}
			if got := NewManagerForTest(t.TempDir(), f, true).Runtime(ctx).State; got != tc.state {
				t.Fatalf("got %s", got)
			}
		})
	}
}

func TestActionUsesExactComposeArguments(t *testing.T) {
	dir := t.TempDir()
	f := &fakeRunner{found: true, results: map[string]Result{join([]string{"compose", "version"}): {}, join([]string{"info"}): {}}}
	m := NewManagerForTest(dir, f, true)
	if _, err := m.Create("web"); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteFile("web", "compose", "services: {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := m.Action(context.Background(), "web", "up"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(m.Root(), "web", "compose.yaml")
	want := []string{"compose", "--project-name", "web", "--project-directory", filepath.Join(m.Root(), "web"), "-f", p, "up", "-d"}
	got := f.calls[len(f.calls)-1].args
	if len(got) != len(want) {
		t.Fatalf("args %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d=%q want %q", i, got[i], want[i])
		}
	}
}

func TestManagedFilesystemSecurityAndFiles(t *testing.T) {
	m := NewManagerForTest(t.TempDir(), &fakeRunner{}, true)
	for _, name := range []string{"", "..", "a/b", "A", "-bad", " bad"} {
		if ValidName(name) {
			t.Fatalf("accepted %q", name)
		}
	}
	if _, err := m.Create("app"); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteFile("app", "compose", "services: {}\n"); err != nil {
		t.Fatal(err)
	}
	if got, err := m.ReadFile("app", "compose"); err != nil || got != "services: {}\n" {
		t.Fatalf("read %q %v", got, err)
	}
	if err := m.WriteFile("app", "env", "SECRET=value\n"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(m.Root(), "app", ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("env mode %o", info.Mode().Perm())
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(m.Root(), "link")); err == nil {
		if _, err := m.ReadFile("link", "env"); err == nil {
			t.Fatal("symlinked project was accepted")
		}
	}
	if err := m.WriteFile("app", "compose", string(make([]byte, MaxFileSize+1))); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("size error %v", err)
	}
}

func TestDiscoveryMergesManagedAndExternalProjects(t *testing.T) {
	dir := t.TempDir()
	results := map[string]Result{
		join([]string{"compose", "version"}): {}, join([]string{"info"}): {},
		join([]string{"ps", "-a", "--format", "{{json .}}"}):         {Output: `{"ID":"1","Names":"managed-web","Image":"nginx","State":"running","Status":"Up","Labels":"com.docker.compose.project=managed,com.docker.compose.service=web"}`},
		join([]string{"compose", "ls", "--all", "--format", "json"}): {Output: `[{"Name":"managed","ConfigFiles":"/srv/managed/compose.yaml"},{"Name":"external","ConfigFiles":["/opt/external/compose.yml"]}]`},
	}
	m := NewManagerForTest(dir, &fakeRunner{found: true, results: results}, true)
	if _, err := m.Create("managed"); err != nil {
		t.Fatal(err)
	}
	runtime, projects, err := m.List(context.Background())
	if err != nil || !runtime.Available {
		t.Fatalf("list: %v %#v", err, runtime)
	}
	if len(projects) != 2 {
		t.Fatalf("projects %#v", projects)
	}
	var managed, external *Project
	for i := range projects {
		if projects[i].Name == "managed" {
			managed = &projects[i]
		}
		if projects[i].Name == "external" {
			external = &projects[i]
		}
	}
	if managed == nil || !managed.Managed || managed.ReadOnly || len(managed.Containers) != 1 {
		t.Fatalf("managed %#v", managed)
	}
	if external == nil || !external.ReadOnly || external.ConfigPath != "/opt/external/compose.yml" {
		t.Fatalf("external %#v", external)
	}
}

func TestProjectStatesAndDeleteRequiresNoContainers(t *testing.T) {
	if got := projectState(nil); got != "down" {
		t.Fatal(got)
	}
	if got := projectState([]Container{{State: "running", Tone: "green"}}); got != "running" {
		t.Fatal(got)
	}
	if got := projectState([]Container{{State: "exited", Tone: "red"}}); got != "stopped" {
		t.Fatal(got)
	}
	if got := projectState([]Container{{State: "running", Tone: "green"}, {State: "exited", Tone: "red"}}); got != "partial" {
		t.Fatal(got)
	}
	if got := projectState([]Container{{State: "running", Health: "unhealthy", Tone: "red"}}); got != "degraded" {
		t.Fatal(got)
	}
	dir := t.TempDir()
	f := &fakeRunner{found: true, results: map[string]Result{join([]string{"compose", "version"}): {}, join([]string{"info"}): {}, join([]string{"ps", "-a", "--format", "{{json .}}"}): {Output: `{"ID":"1","Names":"app","Image":"x","State":"exited","Status":"Exited","Labels":"com.docker.compose.project=app"}`}}}
	m := NewManagerForTest(dir, f, true)
	_, _ = m.Create("app")
	if err := m.Delete(context.Background(), "app"); !errors.Is(err, ErrProjectNotDown) {
		t.Fatalf("delete error %v", err)
	}
	f.results[join([]string{"ps", "-a", "--format", "{{json .}}"})] = Result{}
	if err := m.Delete(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
}

func TestComposeContainerActionsStayScoped(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	inspectStopped := `[{"Id":"` + id + `","State":{"Running":false},"Config":{"Labels":{"com.docker.compose.project":"app"}}}]`
	results := map[string]Result{
		join([]string{"compose", "version"}): {},
		join([]string{"info"}):               {},
		join([]string{"inspect", id}):        {Output: inspectStopped},
	}
	f := &fakeRunner{found: true, results: results}
	m := NewManagerForTest(t.TempDir(), f, true)
	if _, err := m.Create("app"); err != nil {
		t.Fatal(err)
	}
	if err := m.ContainerAction(context.Background(), id, "start"); err != nil {
		t.Fatal(err)
	}
	if got := f.calls[len(f.calls)-1].args; join(got) != join([]string{"container", "start", id}) {
		t.Fatalf("start args %q", got)
	}
	if err := m.ContainerAction(context.Background(), id, "delete"); err != nil {
		t.Fatal(err)
	}
	if got := f.calls[len(f.calls)-1].args; join(got) != join([]string{"container", "rm", id}) {
		t.Fatalf("delete args %q", got)
	}
	results[join([]string{"inspect", id})] = Result{Output: `[{"Id":"` + id + `","State":{"Running":true},"Config":{"Labels":{"com.docker.compose.project":"app"}}}]`}
	if err := m.ContainerAction(context.Background(), id, "delete"); !errors.Is(err, ErrContainerRunning) {
		t.Fatalf("running delete %v", err)
	}
	if err := m.TerminalContainer(context.Background(), id); err != nil {
		t.Fatalf("attachable running container: %v", err)
	}
	results[join([]string{"inspect", id})] = Result{Output: `[{"Id":"` + id + `","State":{"Running":false},"Config":{"Labels":{}}}]`}
	if err := m.ContainerAction(context.Background(), id, "stop"); !errors.Is(err, ErrContainerReadOnly) {
		t.Fatalf("unlabelled container %v", err)
	}
	results[join([]string{"inspect", id})] = Result{Output: `[{"Id":"` + id + `","State":{"Running":false},"Config":{"Labels":{"com.docker.compose.project":"external"}}}]`}
	if err := m.ContainerAction(context.Background(), id, "start"); !errors.Is(err, ErrContainerReadOnly) {
		t.Fatalf("external container %v", err)
	}
}

func TestComposeContainerLogsAreBoundedAndScoped(t *testing.T) {
	const id = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	results := map[string]Result{
		join([]string{"compose", "version"}):        {},
		join([]string{"info"}):                      {},
		join([]string{"inspect", id}):               {Output: `[{"Id":"` + id + `","Config":{"Labels":{"com.docker.compose.project":"app"}}}]`},
		join([]string{"logs", "--tail", "800", id}): {Output: "last log line\n"},
	}
	f := &fakeRunner{found: true, results: results}
	m := NewManagerForTest(t.TempDir(), f, true)
	if _, err := m.Create("app"); err != nil {
		t.Fatal(err)
	}
	logs, err := m.ContainerLogs(context.Background(), id)
	if err != nil || logs != "last log line\n" {
		t.Fatalf("logs %q %v", logs, err)
	}
	if got := f.calls[len(f.calls)-1].args; join(got) != join([]string{"logs", "--tail", "800", id}) {
		t.Fatalf("log args %q", got)
	}
	if ValidContainerID("not-a-container") || !ValidContainerID(id) {
		t.Fatal("container ID validation")
	}
}

func TestComposeContainerLogsKeepNewestOutputWhenBounded(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	logs := strings.Repeat("old log line\n", 1000) + "latest diagnostic\n"
	inspect := `[{"Id":"` + id + `","State":{"Running":true},"Config":{"Labels":{"com.docker.compose.project":"app"}}}]`
	f := &fakeRunner{found: true, results: map[string]Result{
		join([]string{"compose", "version"}): {}, join([]string{"info"}): {},
		join([]string{"inspect", id}):               {Output: inspect},
		join([]string{"logs", "--tail", "800", id}): {Output: logs},
	}}
	m := NewManagerForTest(t.TempDir(), f, true)
	if _, err := m.Create("app"); err != nil {
		t.Fatal(err)
	}
	got, err := m.ContainerLogs(context.Background(), id)
	if err != nil || !strings.Contains(got, "latest diagnostic\n") || !strings.HasPrefix(got, "(earlier log output truncated)\n") {
		t.Fatalf("logs do not retain newest output: %q, %v", got, err)
	}
}

func TestVolumeDiscoveryUsageAndLifecycle(t *testing.T) {
	volumeList := `{"Name":"app_db","Driver":"local","Scope":"local","Labels":"com.docker.compose.project=app,com.docker.compose.volume=db"}` + "\n" + `{"Name":"remote","Driver":"nfs","Scope":"global","Labels":""}`
	inspectContainers := `[{"Name":"/postgres","State":{"Running":true},"Config":{"Labels":{"com.docker.compose.service":"db"}},"Mounts":[{"Type":"volume","Name":"app_db"}]},{"Name":"/old","State":{"Running":false},"Mounts":[{"Type":"volume","Name":"app_db"}]}]`
	results := map[string]Result{
		join([]string{"compose", "version"}): {}, join([]string{"info"}): {},
		join([]string{"volume", "ls", "--format", "json"}): {Output: volumeList},
		join([]string{"ps", "-aq"}):                        {Output: "one\ntwo\n"}, join([]string{"inspect", "one", "two"}): {Output: inspectContainers},
	}
	f := &fakeRunner{found: true, results: results}
	m := NewManagerForTest(t.TempDir(), f, true)
	_, volumes, err := m.ListVolumes(context.Background())
	if err != nil || len(volumes) != 2 {
		t.Fatalf("volumes %#v %v", volumes, err)
	}
	if v := volumes[0]; v.ComposeProject != "app" || v.ComposeVolume != "db" || !v.InUse || !v.RunningUse || len(v.UsedBy) != 2 {
		t.Fatalf("volume %#v", v)
	}
	if volumes[1].Driver != "nfs" {
		t.Fatalf("driver %#v", volumes[1])
	}
	if _, err := m.CreateVolume(context.Background(), "new_data"); err != nil {
		t.Fatal(err)
	}
	if got := f.calls[len(f.calls)-1].args; join(got) != join([]string{"volume", "create", "--driver", "local", "--label", "com.runpilot.managed=true", "new_data"}) {
		t.Fatalf("create args %q", got)
	}
	if err := m.DeleteVolume(context.Background(), "app_db"); !errors.Is(err, ErrVolumeInUse) {
		t.Fatalf("used delete %v", err)
	}
	results[join([]string{"ps", "-aq"})] = Result{} // new_data is unused
	if err := m.DeleteVolume(context.Background(), "new_data"); err != nil {
		t.Fatal(err)
	}
	if got := f.calls[len(f.calls)-1].args; join(got) != join([]string{"volume", "rm", "new_data"}) {
		t.Fatalf("delete args %q", got)
	}
}

func TestVolumeHelperPathsStayStructured(t *testing.T) {
	if ValidVolumeName("../escape") || ValidVolumeName("bad name") || !ValidVolumeName("my.data_1") {
		t.Fatal("volume name validation")
	}
	for _, value := range []string{"../escape", "/absolute", "one//two", "one\\two"} {
		if _, err := helperRelativePath(value); err == nil {
			t.Fatalf("accepted helper path %q", value)
		}
	}
}

func TestDockerVolumeHelperUsesDockerMountAndKeepsLatestEntries(t *testing.T) {
	results := map[string]Result{
		join([]string{"image", "inspect", volumeHelperImage}): {},
		join([]string{"run", "--rm", "--mount", "type=volume,src=postgres_data,dst=/runpilot-volume,readonly", volumeHelperImage, "sh", "-c", volumeHelperListScript, "runpilot-volume-list", "/runpilot-volume"}): {Output: "data\x00d\x00config.yml\x00f\x00"},
		join([]string{"run", "--rm", "--mount", "type=volume,src=postgres_data,dst=/runpilot-volume,readonly", volumeHelperImage, "cat", "/runpilot-volume/config.yml"}):                                           {Output: "enabled: true\n"},
	}
	f := &fakeRunner{found: true, results: results}
	m := NewManagerForTest(t.TempDir(), f, true)
	entries, err := m.ListDockerVolume(context.Background(), "postgres_data", "", storage.ListOptions{})
	if err != nil || len(entries) != 2 || entries[0].Name != "data" || entries[1].Name != "config.yml" {
		t.Fatalf("entries %#v, %v", entries, err)
	}
	data, err := m.ReadDockerVolume(context.Background(), "postgres_data", "config.yml")
	if err != nil || string(data) != "enabled: true\n" {
		t.Fatalf("read %q, %v", data, err)
	}
	imageInspects := 0
	for _, call := range f.calls {
		if join(call.args) == join([]string{"image", "inspect", volumeHelperImage}) {
			imageInspects++
		}
		if join(call.args) == join([]string{"volume", "inspect", "postgres_data"}) || strings.Contains(join(call.args), "/var/lib/docker") {
			t.Fatalf("helper used host volume access: %#v", call)
		}
	}
	if imageInspects != 1 {
		t.Fatalf("helper image was checked %d times, want cached once", imageInspects)
	}
}

func TestDockerVolumeHelperReportsExplicitPullFailure(t *testing.T) {
	f := &fakeRunner{found: true, results: map[string]Result{
		join([]string{"image", "inspect", volumeHelperImage}): {Err: errors.New("missing")},
		join([]string{"pull", volumeHelperImage}):             {Output: "pull denied", Err: errors.New("exit")},
	}}
	m := NewManagerForTest(t.TempDir(), f, true)
	if _, err := m.ReadDockerVolume(context.Background(), "postgres_data", "config.yml"); err == nil || !strings.Contains(err.Error(), "obtain Docker volume helper image") {
		t.Fatalf("helper pull error = %v", err)
	}
}

func TestNetworkDiscoveryUsageAndLifecycle(t *testing.T) {
	results := map[string]Result{
		join([]string{"compose", "version"}): {}, join([]string{"info"}): {},
		join([]string{"network", "ls", "--format", "json"}): {Output: `{"Name":"app_net","Driver":"bridge","Scope":"local","Labels":"com.docker.compose.project=app,com.docker.compose.network=default"}`},
		join([]string{"ps", "-aq"}):                         {Output: "one\n"}, join([]string{"inspect", "one"}): {Output: `[{"Name":"/app","State":{"Running":true},"Config":{"Labels":{"com.docker.compose.service":"web"}},"NetworkSettings":{"Networks":{"app_net":{}}}}]`},
	}
	f := &fakeRunner{found: true, results: results}
	m := NewManagerForTest(t.TempDir(), f, true)
	_, networks, err := m.ListNetworks(context.Background())
	if err != nil || len(networks) != 1 {
		t.Fatalf("networks %#v %v", networks, err)
	}
	if n := networks[0]; n.ComposeProject != "app" || n.ComposeNetwork != "default" || !n.InUse || !n.RunningUse || len(n.UsedBy) != 1 {
		t.Fatalf("network %#v", n)
	}
	if err := m.DeleteNetwork(context.Background(), "bridge"); !errors.Is(err, ErrProtectedNetwork) {
		t.Fatalf("default delete %v", err)
	}
	if err := func() error { _, err := m.CreateNetwork(context.Background(), "app_net"); return err }(); !errors.Is(err, ErrNetworkExists) {
		t.Fatalf("duplicate create %v", err)
	}
	if _, err := m.CreateNetwork(context.Background(), "runpilot_net"); err != nil {
		t.Fatal(err)
	}
	if got := f.calls[len(f.calls)-1].args; join(got) != join([]string{"network", "create", "--driver", "bridge", "--label", "com.runpilot.managed=true", "runpilot_net"}) {
		t.Fatalf("create args %q", got)
	}
	if err := m.DeleteNetwork(context.Background(), "app_net"); !errors.Is(err, ErrComposeNetwork) {
		t.Fatalf("used Compose-managed delete %v", err)
	}
	results[join([]string{"ps", "-aq"})] = Result{}
	if err := m.DeleteNetwork(context.Background(), "app_net"); !errors.Is(err, ErrComposeNetwork) {
		t.Fatalf("Compose-managed delete %v", err)
	}
	if err := m.DeleteNetwork(context.Background(), "runpilot_net"); err != nil {
		t.Fatal(err)
	}
	if got := f.calls[len(f.calls)-1].args; join(got) != join([]string{"network", "rm", "runpilot_net"}) {
		t.Fatalf("delete args %q", got)
	}
	if ValidNetworkName("../escape") || !ValidNetworkName("app.net_1") {
		t.Fatal("network name validation")
	}
}
