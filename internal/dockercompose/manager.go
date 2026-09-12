// Package dockercompose provides the intentionally small Docker Compose
// integration used by RunPilot. It never accepts arbitrary Docker arguments.
package dockercompose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/szilab/RunPilot/internal/storage"
)

const MaxFileSize = 1 << 20

var (
	ErrUnsupported        = errors.New("Docker Compose is not supported on this platform")
	ErrInvalidName        = errors.New("invalid Compose project name")
	ErrProjectNotFound    = errors.New("unknown managed Compose project")
	ErrReadOnly           = errors.New("external Compose projects are read-only")
	ErrBusy               = errors.New("Compose project operation is already in progress")
	ErrProjectNotDown     = errors.New("project must be down before deletion")
	ErrRuntimeUnavailable = errors.New("Docker runtime is unavailable")
	ErrFileTooLarge       = errors.New("Compose file exceeds the 1 MiB limit")
	ErrVolumeInUse        = errors.New("Docker volume is referenced by a container")
	ErrVolumeStorage      = errors.New("remove this volume from RunPilot Storage before deleting it")
	ErrNetworkInUse       = errors.New("Docker network is referenced by a container")
	ErrProtectedNetwork   = errors.New("default Docker networks cannot be deleted")
	ErrNetworkExists      = errors.New("Docker network already exists")
	ErrContainerNotFound  = errors.New("Docker container was not found")
	ErrContainerReadOnly  = errors.New("container is not part of a managed Compose project")
	ErrContainerRunning   = errors.New("container must be stopped before deletion")
)

type Runtime struct {
	Supported bool   `json:"supported"`
	Available bool   `json:"available"`
	State     string `json:"state"`
	Message   string `json:"message"`
	Identity  string `json:"identity,omitempty"`
}

type Container struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Service string `json:"service,omitempty"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Health  string `json:"health,omitempty"`
	Tone    string `json:"tone"`
}

type Project struct {
	Name              string      `json:"name"`
	Managed           bool        `json:"managed"`
	ReadOnly          bool        `json:"readOnly"`
	ConfigPath        string      `json:"configPath,omitempty"`
	ComposeFile       string      `json:"composeFile,omitempty"`
	ComposeFileExists bool        `json:"composeFileExists"`
	State             string      `json:"state"`
	Containers        []Container `json:"containers"`
}

// Volume omits Docker's host mountpoint intentionally. It is an internal
// implementation detail only used by the Storage provider resolver below.
type Volume struct {
	Name              string            `json:"name"`
	Driver            string            `json:"driver"`
	Scope             string            `json:"scope"`
	Labels            map[string]string `json:"labels"`
	ComposeProject    string            `json:"composeProject,omitempty"`
	ComposeVolume     string            `json:"composeVolume,omitempty"`
	ManagedByRunPilot bool              `json:"managedByRunPilot"`
	InUse             bool              `json:"inUse"`
	RunningUse        bool              `json:"runningUse"`
	UsedBy            []string          `json:"usedBy,omitempty"`
	StorageProviderID string            `json:"storageProviderId,omitempty"`
}

type Network struct {
	Name              string            `json:"name"`
	Driver            string            `json:"driver"`
	Scope             string            `json:"scope"`
	Labels            map[string]string `json:"labels"`
	ComposeProject    string            `json:"composeProject,omitempty"`
	ComposeNetwork    string            `json:"composeNetwork,omitempty"`
	ManagedByRunPilot bool              `json:"managedByRunPilot"`
	InUse             bool              `json:"inUse"`
	RunningUse        bool              `json:"runningUse"`
	UsedBy            []string          `json:"usedBy,omitempty"`
}

type Result struct {
	Output string
	Err    error
}
type Runner interface {
	LookPath(string) (string, error)
	Run(context.Context, string, ...string) Result
}
type commandRunner struct{}

func (commandRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }
func (commandRunner) Run(ctx context.Context, name string, args ...string) Result {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	// Structured Docker responses (notably batched inspect output) must remain
	// complete until their JSON has been parsed. Error responses are bounded in
	// commandError before being returned through the API.
	return Result{Output: string(out), Err: err}
}

type Manager struct {
	root      string
	runner    Runner
	supported bool
	mu        sync.Mutex
	busy      map[string]bool
}

func NewManager(dataDir string) *Manager {
	root, _ := filepath.Abs(filepath.Join(dataDir, "compose"))
	return &Manager{root: root, runner: commandRunner{}, supported: runtime.GOOS == "linux", busy: map[string]bool{}}
}

// NewManagerForTest permits deterministic command tests without Docker.
func NewManagerForTest(dataDir string, runner Runner, supported bool) *Manager {
	m := NewManager(dataDir)
	m.runner, m.supported = runner, supported
	return m
}
func (m *Manager) Root() string { return m.root }

func (m *Manager) Runtime(ctx context.Context) Runtime {
	identity := currentIdentity()
	if !m.supported {
		return Runtime{State: "unsupported", Message: "Docker Compose is not supported on this platform.", Identity: identity}
	}
	if _, err := m.runner.LookPath("docker"); err != nil {
		return Runtime{Supported: true, State: "cli-missing", Message: "Docker CLI is not installed.", Identity: identity}
	}
	if r := m.runner.Run(ctx, "docker", "compose", "version"); r.Err != nil {
		return Runtime{Supported: true, State: "compose-missing", Message: "Docker Compose v2 is not available.", Identity: identity}
	}
	if r := m.runner.Run(ctx, "docker", "info"); r.Err != nil {
		if permissionText(r.Output) {
			return Runtime{Supported: true, State: "permission-denied", Message: "RunPilot cannot access the Docker daemon.", Identity: identity}
		}
		return Runtime{Supported: true, State: "daemon-unavailable", Message: "Docker daemon is unavailable.", Identity: identity}
	}
	return Runtime{Supported: true, Available: true, State: "ready", Message: "Docker Compose is ready.", Identity: identity}
}

func currentIdentity() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return fmt.Sprintf("uid-%d", os.Getuid())
}
func permissionText(v string) bool {
	v = strings.ToLower(v)
	return strings.Contains(v, "permission denied") || strings.Contains(v, "permissiondenied") || strings.Contains(v, "access is denied")
}
func bounded(v string) string {
	const max = 8192
	if len(v) > max {
		return v[:max] + "\n(output truncated)"
	}
	return v
}

func ValidName(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || (i > 0 && (c == '-' || c == '_'))) {
			return false
		}
	}
	return true
}
func (m *Manager) projectDir(name string) (string, error) {
	if !ValidName(name) {
		return "", ErrInvalidName
	}
	p := filepath.Join(m.root, name)
	rel, err := filepath.Rel(m.root, p)
	if err != nil || rel != name || filepath.IsAbs(rel) {
		return "", ErrInvalidName
	}
	return p, nil
}
func (m *Manager) managedDir(name string) (string, error) {
	p, err := m.projectDir(name)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(p)
	if os.IsNotExist(err) {
		return "", ErrProjectNotFound
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("managed project directory is invalid")
	}
	return p, nil
}

func (m *Manager) Create(name string) (Project, error) {
	p, err := m.projectDir(name)
	if err != nil {
		return Project{}, err
	}
	if err = os.MkdirAll(m.root, 0o755); err != nil {
		return Project{}, err
	}
	if err = os.Mkdir(p, 0o755); err != nil {
		if os.IsExist(err) {
			return Project{}, fmt.Errorf("project already exists")
		}
		return Project{}, err
	}
	if err = m.WriteFile(name, "compose", "services: {}\n"); err != nil {
		_ = os.Remove(p)
		return Project{}, err
	}
	return Project{Name: name, Managed: true, ComposeFile: filepath.Join(p, "compose.yaml"), ComposeFileExists: true, State: "unknown", Containers: []Container{}}, nil
}

func (m *Manager) List(ctx context.Context) (Runtime, []Project, error) {
	rt := m.Runtime(ctx)
	managed, err := m.managedProjects()
	if err != nil {
		return rt, nil, err
	}
	if !rt.Available {
		for i := range managed {
			managed[i].State = "unknown"
		}
		return rt, managed, nil
	}
	containers, err := m.containers(ctx)
	if err != nil {
		return rt, managed, err
	}
	byName := map[string]*Project{}
	for i := range managed {
		byName[managed[i].Name] = &managed[i]
	}
	for _, p := range byName {
		p.Containers = append(p.Containers, containers[p.Name]...)
		p.State = projectState(p.Containers)
	}
	external, err := m.external(ctx)
	if err != nil {
		return rt, managed, err
	}
	for _, e := range external {
		if _, ok := byName[e.Name]; ok {
			continue
		}
		e.Containers = containers[e.Name]
		e.State = projectState(e.Containers)
		managed = append(managed, e)
	}
	for name, cs := range containers {
		if _, ok := byName[name]; !ok && !hasProject(managed, name) {
			managed = append(managed, Project{Name: name, ReadOnly: true, State: projectState(cs), Containers: cs})
		}
	}
	return rt, managed, nil
}
func hasProject(projects []Project, name string) bool {
	for _, p := range projects {
		if p.Name == name {
			return true
		}
	}
	return false
}
func (m *Manager) managedProjects() ([]Project, error) {
	if err := os.MkdirAll(m.root, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, err
	}
	out := []Project{}
	for _, e := range entries {
		if !e.IsDir() || !ValidName(e.Name()) {
			continue
		}
		p, err := m.managedDir(e.Name())
		if err != nil {
			continue
		}
		file, exists := composeAt(p)
		out = append(out, Project{Name: e.Name(), Managed: true, ComposeFile: file, ConfigPath: file, ComposeFileExists: exists, Containers: []Container{}})
	}
	return out, nil
}
func composeAt(dir string) (string, bool) {
	for _, n := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		p := filepath.Join(dir, n)
		info, err := os.Lstat(p)
		if err == nil && info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular() {
			return p, true
		}
	}
	return filepath.Join(dir, "compose.yaml"), false
}

type composeLS struct {
	Name        string          `json:"Name"`
	Status      string          `json:"Status"`
	ConfigFiles json.RawMessage `json:"ConfigFiles"`
}

func (m *Manager) external(ctx context.Context) ([]Project, error) {
	r := m.runner.Run(ctx, "docker", "compose", "ls", "--all", "--format", "json")
	if r.Err != nil {
		return nil, commandError(r)
	}
	var items []composeLS
	if strings.TrimSpace(r.Output) == "" {
		return []Project{}, nil
	}
	if err := json.Unmarshal([]byte(r.Output), &items); err != nil {
		return nil, fmt.Errorf("read Docker Compose projects: %w", err)
	}
	out := make([]Project, 0, len(items))
	for _, v := range items {
		if v.Name == "" {
			continue
		}
		path := composeConfigPath(v.ConfigFiles)
		out = append(out, Project{Name: v.Name, ReadOnly: true, ConfigPath: path, Containers: []Container{}})
	}
	return out, nil
}

func composeConfigPath(raw json.RawMessage) string {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil && len(many) > 0 {
		return many[0]
	}
	return ""
}

type dockerPS struct {
	ID     string `json:"ID"`
	Names  string `json:"Names"`
	Image  string `json:"Image"`
	State  string `json:"State"`
	Status string `json:"Status"`
	Labels string `json:"Labels"`
}

func (m *Manager) containers(ctx context.Context) (map[string][]Container, error) {
	r := m.runner.Run(ctx, "docker", "ps", "-a", "--format", "{{json .}}")
	if r.Err != nil {
		return nil, commandError(r)
	}
	out := map[string][]Container{}
	for _, line := range strings.Split(r.Output, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row dockerPS
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("read Docker containers: %w", err)
		}
		labels := parseLabels(row.Labels)
		project := labels["com.docker.compose.project"]
		if project == "" {
			continue
		}
		health := ""
		low := strings.ToLower(row.Status)
		if strings.Contains(low, "(healthy)") {
			health = "healthy"
		}
		if strings.Contains(low, "(unhealthy)") {
			health = "unhealthy"
		}
		c := Container{ID: row.ID, Name: row.Names, Service: labels["com.docker.compose.service"], Image: row.Image, State: strings.ToLower(row.State), Status: row.Status, Health: health}
		c.Tone = containerTone(c)
		out[project] = append(out[project], c)
	}
	return out, nil
}
func parseLabels(v string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(v, ",") {
		k, val, ok := strings.Cut(part, "=")
		if ok {
			out[k] = val
		}
	}
	return out
}
func containerTone(c Container) string {
	s := strings.ToLower(c.State + " " + c.Health)
	if strings.Contains(s, "unhealthy") || strings.Contains(s, "dead") || strings.Contains(s, "exited") {
		return "red"
	}
	if strings.Contains(s, "running") {
		return "green"
	}
	if strings.Contains(s, "created") || strings.Contains(s, "restarting") || strings.Contains(s, "paused") {
		return "yellow"
	}
	return "gray"
}
func projectState(cs []Container) string {
	if len(cs) == 0 {
		return "down"
	}
	run, degraded, trans := 0, false, false
	for _, c := range cs {
		if c.Health == "unhealthy" || c.State == "dead" {
			degraded = true
		}
		if c.State == "running" && c.Tone == "green" {
			run++
		}
		if c.Tone == "yellow" {
			trans = true
		}
	}
	if degraded {
		return "degraded"
	}
	if run == len(cs) {
		return "running"
	}
	if run == 0 && !trans {
		return "stopped"
	}
	return "partial"
}

type volumeLS struct {
	Name   string `json:"Name"`
	Driver string `json:"Driver"`
	Scope  string `json:"Scope"`
	Labels string `json:"Labels"`
}
type volumeInspect struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Scope      string            `json:"Scope"`
	Mountpoint string            `json:"Mountpoint"`
	Labels     map[string]string `json:"Labels"`
}
type inspectedContainer struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	Mounts []struct {
		Type string `json:"Type"`
		Name string `json:"Name"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
}

func ValidContainerID(id string) bool {
	if len(id) < 12 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func (m *Manager) inspectComposeContainer(ctx context.Context, id string) (inspectedContainer, error) {
	if !ValidContainerID(id) {
		return inspectedContainer{}, ErrContainerNotFound
	}
	r := m.runner.Run(ctx, "docker", "inspect", id)
	if r.Err != nil {
		return inspectedContainer{}, ErrContainerNotFound
	}
	var containers []inspectedContainer
	if err := json.Unmarshal([]byte(r.Output), &containers); err != nil || len(containers) != 1 {
		return inspectedContainer{}, ErrContainerNotFound
	}
	project := containers[0].Config.Labels["com.docker.compose.project"]
	if project == "" {
		return inspectedContainer{}, ErrContainerReadOnly
	}
	if _, err := m.managedDir(project); err != nil {
		return inspectedContainer{}, ErrContainerReadOnly
	}
	return containers[0], nil
}
func (m *Manager) ContainerAction(ctx context.Context, id, action string) error {
	if !map[string]bool{"start": true, "stop": true, "delete": true}[action] {
		return fmt.Errorf("invalid container action")
	}
	rt := m.Runtime(ctx)
	if !rt.Supported {
		return ErrUnsupported
	}
	if !rt.Available {
		return fmt.Errorf("%w: %s", ErrRuntimeUnavailable, rt.Message)
	}
	container, err := m.inspectComposeContainer(ctx, id)
	if err != nil {
		return err
	}
	if action == "delete" && container.State.Running {
		return ErrContainerRunning
	}
	if action == "start" && container.State.Running {
		return nil
	}
	if action == "stop" && !container.State.Running {
		return nil
	}
	args := []string{"container", action, id}
	if action == "delete" {
		args = []string{"container", "rm", id}
	}
	r := m.runner.Run(ctx, "docker", args...)
	if r.Err != nil {
		return commandError(r)
	}
	return nil
}
func (m *Manager) ContainerLogs(ctx context.Context, id string) (string, error) {
	rt := m.Runtime(ctx)
	if !rt.Supported {
		return "", ErrUnsupported
	}
	if !rt.Available {
		return "", fmt.Errorf("%w: %s", ErrRuntimeUnavailable, rt.Message)
	}
	if _, err := m.inspectComposeContainer(ctx, id); err != nil {
		return "", err
	}
	r := m.runner.Run(ctx, "docker", "logs", "--tail", "800", id)
	if r.Err != nil {
		return "", commandError(r)
	}
	return bounded(r.Output), nil
}

// TerminalContainer confirms that a running managed Compose container may
// receive the fixed interactive-shell integration.
func (m *Manager) TerminalContainer(ctx context.Context, id string) error {
	rt := m.Runtime(ctx)
	if !rt.Supported {
		return ErrUnsupported
	}
	if !rt.Available {
		return fmt.Errorf("%w: %s", ErrRuntimeUnavailable, rt.Message)
	}
	container, err := m.inspectComposeContainer(ctx, id)
	if err != nil {
		return err
	}
	if !container.State.Running {
		return fmt.Errorf("container is not running")
	}
	return nil
}

type volumeUse struct {
	Running bool
	Names   []string
}

func ValidVolumeName(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || (i > 0 && (c == '.' || c == '_' || c == '-'))) {
			return false
		}
	}
	return true
}

func (m *Manager) ListVolumes(ctx context.Context) (Runtime, []Volume, error) {
	rt := m.Runtime(ctx)
	if !rt.Available {
		return rt, []Volume{}, nil
	}
	r := m.runner.Run(ctx, "docker", "volume", "ls", "--format", "json")
	if r.Err != nil {
		return rt, nil, commandError(r)
	}
	rows, err := parseVolumeRows(r.Output)
	if err != nil {
		return rt, nil, err
	}
	usage, err := m.volumeUsageMap(ctx)
	if err != nil {
		return rt, nil, err
	}
	out := make([]Volume, 0, len(rows))
	for _, row := range rows {
		labels := parseLabels(row.Labels)
		use := usage[row.Name]
		out = append(out, Volume{Name: row.Name, Driver: row.Driver, Scope: row.Scope, Labels: labels, ComposeProject: labels["com.docker.compose.project"], ComposeVolume: labels["com.docker.compose.volume"], ManagedByRunPilot: labels["com.runpilot.managed"] == "true", InUse: len(use.Names) > 0, RunningUse: use.Running, UsedBy: use.Names})
	}
	return rt, out, nil
}
func parseVolumeRows(output string) ([]volumeLS, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return []volumeLS{}, nil
	}
	var list []volumeLS
	if json.Unmarshal([]byte(output), &list) == nil {
		return list, nil
	}
	list = []volumeLS{}
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row volumeLS
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("read Docker volumes: %w", err)
		}
		list = append(list, row)
	}
	return list, nil
}
func (m *Manager) volumeUsageMap(ctx context.Context) (map[string]volumeUse, error) {
	r := m.runner.Run(ctx, "docker", "ps", "-aq")
	if r.Err != nil {
		return nil, commandError(r)
	}
	ids := strings.Fields(r.Output)
	out := map[string]volumeUse{}
	if len(ids) == 0 {
		return out, nil
	}
	args := append([]string{"inspect"}, ids...)
	r = m.runner.Run(ctx, "docker", args...)
	if r.Err != nil {
		return nil, commandError(r)
	}
	var containers []inspectedContainer
	if err := json.Unmarshal([]byte(r.Output), &containers); err != nil {
		return nil, fmt.Errorf("read Docker container mounts: %w", err)
	}
	for _, container := range containers {
		name := strings.TrimPrefix(container.Name, "/")
		if service := container.Config.Labels["com.docker.compose.service"]; service != "" {
			name = service
		}
		for _, mount := range container.Mounts {
			if mount.Type != "volume" || mount.Name == "" {
				continue
			}
			u := out[mount.Name]
			u.Running = u.Running || container.State.Running
			u.Names = append(u.Names, name)
			out[mount.Name] = u
		}
	}
	return out, nil
}
func (m *Manager) inspectVolume(ctx context.Context, name string) (volumeInspect, error) {
	if !ValidVolumeName(name) {
		return volumeInspect{}, ErrInvalidName
	}
	r := m.runner.Run(ctx, "docker", "volume", "inspect", name)
	if r.Err != nil {
		return volumeInspect{}, commandError(r)
	}
	var values []volumeInspect
	if err := json.Unmarshal([]byte(r.Output), &values); err != nil || len(values) != 1 {
		if err != nil {
			return volumeInspect{}, fmt.Errorf("read Docker volume: %w", err)
		}
		return volumeInspect{}, fmt.Errorf("Docker volume inspect returned no volume")
	}
	return values[0], nil
}

// VolumeDetails implements storage.DockerVolumeResolver. Its mountpoint is
// deliberately returned only to internal Storage code, never to web models.
func (m *Manager) VolumeDetails(ctx context.Context, name string) (storage.DockerVolumeDetails, error) {
	rt := m.Runtime(ctx)
	if !rt.Supported {
		return storage.DockerVolumeDetails{}, ErrUnsupported
	}
	if !rt.Available {
		return storage.DockerVolumeDetails{}, fmt.Errorf("%w: %s", ErrRuntimeUnavailable, rt.Message)
	}
	v, e := m.inspectVolume(ctx, name)
	return storage.DockerVolumeDetails{Driver: v.Driver, Mountpoint: v.Mountpoint}, e
}
func (m *Manager) VolumeUsage(ctx context.Context, name string) (storage.DockerVolumeUsage, error) {
	rt := m.Runtime(ctx)
	if !rt.Supported {
		return storage.DockerVolumeUsage{}, ErrUnsupported
	}
	if !rt.Available {
		return storage.DockerVolumeUsage{}, fmt.Errorf("%w: %s", ErrRuntimeUnavailable, rt.Message)
	}
	u, e := m.volumeUsageMap(ctx)
	if e != nil {
		return storage.DockerVolumeUsage{}, e
	}
	return storage.DockerVolumeUsage{Running: u[name].Running}, nil
}
func (m *Manager) CreateVolume(ctx context.Context, name string) (Volume, error) {
	if !ValidVolumeName(name) {
		return Volume{}, ErrInvalidName
	}
	rt := m.Runtime(ctx)
	if !rt.Supported {
		return Volume{}, ErrUnsupported
	}
	if !rt.Available {
		return Volume{}, fmt.Errorf("%w: %s", ErrRuntimeUnavailable, rt.Message)
	}
	r := m.runner.Run(ctx, "docker", "volume", "create", "--driver", "local", "--label", "com.runpilot.managed=true", name)
	if r.Err != nil {
		return Volume{}, commandError(r)
	}
	return Volume{Name: name, Driver: "local", Scope: "local", Labels: map[string]string{"com.runpilot.managed": "true"}, ManagedByRunPilot: true}, nil
}
func (m *Manager) DeleteVolume(ctx context.Context, name string, exposed bool) error {
	if !ValidVolumeName(name) {
		return ErrInvalidName
	}
	if exposed {
		return ErrVolumeStorage
	}
	rt := m.Runtime(ctx)
	if !rt.Supported {
		return ErrUnsupported
	}
	if !rt.Available {
		return fmt.Errorf("%w: %s", ErrRuntimeUnavailable, rt.Message)
	}
	u, e := m.volumeUsageMap(ctx)
	if e != nil {
		return e
	}
	if len(u[name].Names) > 0 {
		return ErrVolumeInUse
	}
	r := m.runner.Run(ctx, "docker", "volume", "rm", name)
	if r.Err != nil {
		return commandError(r)
	}
	return nil
}

type networkLS struct {
	Name   string `json:"Name"`
	Driver string `json:"Driver"`
	Scope  string `json:"Scope"`
	Labels string `json:"Labels"`
}

func ValidNetworkName(name string) bool { return ValidVolumeName(name) }
func DefaultNetwork(name string) bool   { return name == "bridge" || name == "host" || name == "none" }
func (m *Manager) ListNetworks(ctx context.Context) (Runtime, []Network, error) {
	rt := m.Runtime(ctx)
	if !rt.Available {
		return rt, []Network{}, nil
	}
	r := m.runner.Run(ctx, "docker", "network", "ls", "--format", "json")
	if r.Err != nil {
		return rt, nil, commandError(r)
	}
	rows, err := parseNetworkRows(r.Output)
	if err != nil {
		return rt, nil, err
	}
	usage, err := m.networkUsageMap(ctx)
	if err != nil {
		return rt, nil, err
	}
	out := make([]Network, 0, len(rows))
	for _, row := range rows {
		labels := parseLabels(row.Labels)
		use := usage[row.Name]
		out = append(out, Network{Name: row.Name, Driver: row.Driver, Scope: row.Scope, Labels: labels, ComposeProject: labels["com.docker.compose.project"], ComposeNetwork: labels["com.docker.compose.network"], ManagedByRunPilot: labels["com.runpilot.managed"] == "true", InUse: len(use.Names) > 0, RunningUse: use.Running, UsedBy: use.Names})
	}
	return rt, out, nil
}
func parseNetworkRows(output string) ([]networkLS, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return []networkLS{}, nil
	}
	var list []networkLS
	if json.Unmarshal([]byte(output), &list) == nil {
		return list, nil
	}
	list = []networkLS{}
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row networkLS
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("read Docker networks: %w", err)
		}
		list = append(list, row)
	}
	return list, nil
}
func (m *Manager) networkUsageMap(ctx context.Context) (map[string]volumeUse, error) {
	r := m.runner.Run(ctx, "docker", "ps", "-aq")
	if r.Err != nil {
		return nil, commandError(r)
	}
	ids := strings.Fields(r.Output)
	out := map[string]volumeUse{}
	if len(ids) == 0 {
		return out, nil
	}
	r = m.runner.Run(ctx, "docker", append([]string{"inspect"}, ids...)...)
	if r.Err != nil {
		return nil, commandError(r)
	}
	var containers []inspectedContainer
	if err := json.Unmarshal([]byte(r.Output), &containers); err != nil {
		return nil, fmt.Errorf("read Docker container networks: %w", err)
	}
	for _, container := range containers {
		name := strings.TrimPrefix(container.Name, "/")
		if service := container.Config.Labels["com.docker.compose.service"]; service != "" {
			name = service
		}
		for network := range container.NetworkSettings.Networks {
			u := out[network]
			u.Running = u.Running || container.State.Running
			u.Names = append(u.Names, name)
			out[network] = u
		}
	}
	return out, nil
}
func (m *Manager) CreateNetwork(ctx context.Context, name string) (Network, error) {
	if !ValidNetworkName(name) {
		return Network{}, ErrInvalidName
	}
	rt := m.Runtime(ctx)
	if !rt.Supported {
		return Network{}, ErrUnsupported
	}
	if !rt.Available {
		return Network{}, fmt.Errorf("%w: %s", ErrRuntimeUnavailable, rt.Message)
	}
	if exists, err := m.networkExists(ctx, name); err != nil {
		return Network{}, err
	} else if exists {
		return Network{}, ErrNetworkExists
	}
	r := m.runner.Run(ctx, "docker", "network", "create", "--driver", "bridge", "--label", "com.runpilot.managed=true", name)
	if r.Err != nil {
		return Network{}, commandError(r)
	}
	return Network{Name: name, Driver: "bridge", Scope: "local", Labels: map[string]string{"com.runpilot.managed": "true"}, ManagedByRunPilot: true}, nil
}
func (m *Manager) DeleteNetwork(ctx context.Context, name string) error {
	if !ValidNetworkName(name) {
		return ErrInvalidName
	}
	if DefaultNetwork(name) {
		return ErrProtectedNetwork
	}
	rt := m.Runtime(ctx)
	if !rt.Supported {
		return ErrUnsupported
	}
	if !rt.Available {
		return fmt.Errorf("%w: %s", ErrRuntimeUnavailable, rt.Message)
	}
	usage, err := m.networkUsageMap(ctx)
	if err != nil {
		return err
	}
	if len(usage[name].Names) > 0 {
		return ErrNetworkInUse
	}
	r := m.runner.Run(ctx, "docker", "network", "rm", name)
	if r.Err != nil {
		return commandError(r)
	}
	return nil
}

func (m *Manager) networkExists(ctx context.Context, name string) (bool, error) {
	r := m.runner.Run(ctx, "docker", "network", "ls", "--format", "json")
	if r.Err != nil {
		return false, commandError(r)
	}
	rows, err := parseNetworkRows(r.Output)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.Name == name {
			return true, nil
		}
	}
	return false, nil
}

func (m *Manager) Action(ctx context.Context, name, action string) error {
	if !map[string]bool{"up": true, "start": true, "stop": true, "restart": true, "down": true}[action] {
		return fmt.Errorf("invalid Compose action")
	}
	dir, err := m.managedDir(name)
	if err != nil {
		return err
	}
	file, exists := composeAt(dir)
	if !exists {
		return fmt.Errorf("compose.yaml is missing")
	}
	rt := m.Runtime(ctx)
	if !rt.Supported {
		return ErrUnsupported
	}
	if !rt.Available {
		return fmt.Errorf("%w: %s", ErrRuntimeUnavailable, rt.Message)
	}
	if !m.acquire(name) {
		return ErrBusy
	}
	defer m.release(name)
	args := []string{"compose", "--project-name", name, "--project-directory", dir, "-f", file, action}
	if action == "up" {
		args = append(args, "-d")
	}
	r := m.runner.Run(ctx, "docker", args...)
	if r.Err != nil {
		return commandError(r)
	}
	return nil
}
func (m *Manager) acquire(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy[name] {
		return false
	}
	m.busy[name] = true
	return true
}
func (m *Manager) release(name string) { m.mu.Lock(); delete(m.busy, name); m.mu.Unlock() }
func commandError(r Result) error {
	detail := strings.TrimSpace(r.Output)
	if detail == "" {
		return r.Err
	}
	return fmt.Errorf("%v: %s", r.Err, bounded(detail))
}

func (m *Manager) ReadFile(name, kind string) (string, error) {
	p, err := m.managedFile(name, kind, false)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if len(b) > MaxFileSize {
		return "", ErrFileTooLarge
	}
	return string(b), nil
}
func (m *Manager) WriteFile(name, kind, content string) error {
	if len(content) > MaxFileSize {
		return ErrFileTooLarge
	}
	p, err := m.managedFile(name, kind, true)
	if err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if kind == "env" {
		perm = 0o600
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".runpilot-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(perm); err == nil {
		_, err = tmp.WriteString(content)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpName, p)
}
func (m *Manager) managedFile(name, kind string, create bool) (string, error) {
	dir, err := m.managedDir(name)
	if err != nil {
		return "", err
	}
	file := ""
	switch kind {
	case "compose":
		file = "compose.yaml"
	case "env":
		file = ".env"
	default:
		return "", fmt.Errorf("invalid managed file")
	}
	p := filepath.Join(dir, file)
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel != file {
		return "", ErrInvalidName
	}
	info, err := os.Lstat(p)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return "", fmt.Errorf("managed file is invalid")
	}
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return p, nil
}
func (m *Manager) Delete(ctx context.Context, name string) error {
	dir, err := m.managedDir(name)
	if err != nil {
		return err
	}
	rt := m.Runtime(ctx)
	if !rt.Supported {
		return ErrUnsupported
	}
	if !rt.Available {
		return fmt.Errorf("%w: %s", ErrRuntimeUnavailable, rt.Message)
	}
	if !m.acquire(name) {
		return ErrBusy
	}
	defer m.release(name)
	cs, err := m.containers(ctx)
	if err != nil {
		return err
	}
	if len(cs[name]) > 0 {
		return ErrProjectNotDown
	} // Revalidate containment and reject a race-created symlink before removal.
	dir, err = m.managedDir(name)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
