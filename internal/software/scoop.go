package software

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/model"
)

// ScoopInstallerURL is intentionally centralized. It is the official Scoop
// installer, downloaded to a provider-owned location before it is executed.
const ScoopInstallerURL = "https://raw.githubusercontent.com/ScoopInstaller/Install/master/install.ps1"

const scoopBootstrapWrapper = `param([string]$Installer, [string]$ScoopDir, [string]$CacheDir, [string]$GlobalDir)
$env:CI = '1'
$env:SCOOP_NOINSTALL = '1'
. $Installer -ScoopDir $ScoopDir -ScoopCacheDir $CacheDir -ScoopGlobalDir $GlobalDir -RunAsAdmin
# The official installer normally persists shims in PATH. RunPilot invokes the
# managed entrypoint directly, so suppress that host-level mutation.
function Add-ShimsDirToPath { }
Install-Scoop
`

var packageID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(?:/[A-Za-z0-9][A-Za-z0-9._-]*)?$`)

type Command struct {
	Path string
	Args []string
	Env  []string
	Dir  string
}
type CommandResult struct {
	Stdout, Stderr string
	ExitCode       int
}
type CommandRunner interface {
	Run(context.Context, Command) (CommandResult, error)
}
type Downloader interface {
	Download(context.Context, string, string) error
}

type ScoopOptions struct {
	Runner       CommandRunner
	Downloader   Downloader
	PowerShell   string
	OperationTTL time.Duration
}

type Scoop struct {
	definition   model.SoftwareProviderDefinition
	root         string
	bootstrapDir string
	defaultRoot  bool
	runner       CommandRunner
	downloader   Downloader
	powerShell   string
	timeout      time.Duration
	mu           sync.Mutex
	last         Operation
}

func NewScoop(d model.SoftwareProviderDefinition, dataDir string, options ScoopOptions) (*Scoop, error) {
	d, err := ValidateDefinition(dataDir, d)
	if err != nil {
		return nil, err
	}
	root, isDefault, err := EffectiveRoot(dataDir, d)
	if err != nil {
		return nil, err
	}
	bootstrapDir, err := filepath.Abs(filepath.Join(dataDir, "software", "bootstrap", d.ID))
	if err != nil {
		return nil, fmt.Errorf("resolve Scoop bootstrap directory: %w", err)
	}
	if options.Runner == nil {
		options.Runner = systemRunner{}
	}
	if options.Downloader == nil {
		options.Downloader = httpDownloader{}
	}
	if options.PowerShell == "" {
		options.PowerShell = powerShellPath()
	}
	if options.OperationTTL <= 0 {
		options.OperationTTL = 15 * time.Minute
	}
	return &Scoop{definition: d, root: root, bootstrapDir: bootstrapDir, defaultRoot: isDefault, runner: options.Runner, downloader: options.Downloader, powerShell: options.PowerShell, timeout: options.OperationTTL}, nil
}

func (s *Scoop) EntryPoint() string {
	return filepath.Join(s.root, "apps", "scoop", "current", "bin", "scoop.ps1")
}
func (s *Scoop) Root() string        { return s.root }
func (s *Scoop) ownerMarker() string { return filepath.Join(s.root, ".runpilot-scoop-owner") }
func (s *Scoop) managedGitRoot() string {
	return filepath.Join(s.root, "apps", "git", "current")
}

func (s *Scoop) Status(ctx context.Context) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	state := Status{ID: s.definition.ID, Name: s.definition.Name, Type: string(s.definition.Type), Root: s.root, UsingDefault: s.defaultRoot, LastOperation: s.last}
	if err := s.ensureReadyLocked(ctx); err != nil {
		state.State = StateUnavailable
		state.Message = err.Error()
		return state
	}
	state.State = StateReady
	if result, err := s.runLocked(ctx, "--version"); err == nil {
		state.Version = firstNonEmptyLine(result.Stdout)
	}
	return state
}

func (s *Scoop) Installed(ctx context.Context) ([]Package, error) {
	return s.withReady(ctx, "list installed", func(ctx context.Context) ([]Package, error) { return s.installedLocked(ctx) })
}
func (s *Scoop) Search(ctx context.Context, query string) ([]Package, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("search query is required")
	}
	if strings.ContainsAny(query, "\x00\r\n") {
		return nil, fmt.Errorf("invalid search query")
	}
	return s.withReady(ctx, "search", func(ctx context.Context) ([]Package, error) {
		r, e := s.runLocked(ctx, "search", query)
		if e != nil {
			return nil, e
		}
		results := parseSearchOutput(s.definition.ID, r.Stdout)
		installed, e := s.installedLocked(ctx)
		if e != nil {
			return nil, e
		}
		for i := range results {
			for _, current := range installed {
				if samePackage(results[i].ID, current.ID) {
					results[i].Installed = true
					results[i].Version = current.Version
					break
				}
			}
			results[i].Protected = isScoopDependency(results[i].ID)
		}
		return results, nil
	})
}
func (s *Scoop) Updates(ctx context.Context) ([]Package, error) {
	return s.withReady(ctx, "list updates", func(ctx context.Context) ([]Package, error) {
		r, e := s.runLocked(ctx, "status")
		if e != nil {
			return nil, e
		}
		items := parseStatusOutput(s.definition.ID, r.Stdout)
		for i := range items {
			items[i].Protected = isScoopDependency(items[i].ID)
		}
		return items, nil
	})
}

func (s *Scoop) Buckets(ctx context.Context) ([]Bucket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	if err := s.ensureReadyLocked(ctx); err != nil {
		return nil, err
	}
	r, err := s.runLocked(ctx, "bucket", "list")
	if err != nil {
		return nil, err
	}
	return parseBucketList(r.Stdout), nil
}

func (s *Scoop) AddBucket(ctx context.Context, name, source string) error {
	if !ValidPackageID(name) || strings.Contains(name, "/") {
		return fmt.Errorf("invalid bucket name %q", name)
	}
	if strings.EqualFold(name, "main") {
		return fmt.Errorf("the main bucket is managed by Scoop and cannot be added manually")
	}
	if source != "" {
		u, err := url.ParseRequestURI(source)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("bucket source must be a valid HTTPS URL")
		}
	}
	return s.mutate(ctx, "add bucket", name, func(ctx context.Context) error {
		args := []string{"bucket", "add", name}
		if source != "" {
			args = append(args, source)
		}
		if _, err := s.runLocked(ctx, args...); err != nil {
			return err
		}
		buckets, err := s.bucketsLocked(ctx)
		if err != nil {
			return err
		}
		for _, bucket := range buckets {
			if strings.EqualFold(bucket.Name, name) {
				return nil
			}
		}
		return fmt.Errorf("Scoop added bucket %q but it is not listed in the managed root", name)
	})
}

func (s *Scoop) RemoveBucket(ctx context.Context, name string) error {
	if !ValidPackageID(name) || strings.Contains(name, "/") {
		return fmt.Errorf("invalid bucket name %q", name)
	}
	if strings.EqualFold(name, "main") {
		return fmt.Errorf("the main bucket is required by the managed Scoop provider")
	}
	return s.mutate(ctx, "remove bucket", name, func(ctx context.Context) error {
		if _, err := s.runLocked(ctx, "bucket", "rm", name); err != nil {
			return err
		}
		buckets, err := s.bucketsLocked(ctx)
		if err != nil {
			return err
		}
		for _, bucket := range buckets {
			if strings.EqualFold(bucket.Name, name) {
				return fmt.Errorf("Scoop removed bucket %q but it is still listed", name)
			}
		}
		return nil
	})
}

func (s *Scoop) Install(ctx context.Context, id string) error {
	if isScoopDependency(id) {
		return fmt.Errorf("%q is managed as a Scoop dependency and cannot be changed manually", id)
	}
	return s.mutate(ctx, "install", id, func(ctx context.Context) error {
		_, e := s.runLocked(ctx, "install", id)
		if e != nil {
			return e
		}
		packages, e := s.installedLocked(ctx)
		if e != nil {
			return e
		}
		if !containsPackage(packages, id) {
			return fmt.Errorf("Scoop install completed but %q is not installed in managed root %s", id, s.root)
		}
		return nil
	})
}
func (s *Scoop) Upgrade(ctx context.Context, id string) error {
	if isScoopDependency(id) {
		return fmt.Errorf("%q is managed as a Scoop dependency and cannot be changed manually", id)
	}
	return s.mutate(ctx, "upgrade", id, func(ctx context.Context) error {
		_, e := s.runLocked(ctx, "update", id)
		if e != nil {
			return e
		}
		packages, e := s.installedLocked(ctx)
		if e != nil {
			return e
		}
		if !containsPackage(packages, id) {
			return fmt.Errorf("Scoop upgrade completed but %q is not installed in managed root %s", id, s.root)
		}
		return nil
	})
}
func (s *Scoop) UpgradeAll(ctx context.Context) error {
	return s.mutate(ctx, "upgrade all", "", func(ctx context.Context) error { _, e := s.runLocked(ctx, "update", "*"); return e })
}
func (s *Scoop) Uninstall(ctx context.Context, id string) error {
	if isScoopDependency(id) {
		return fmt.Errorf("%q is managed as a Scoop dependency and cannot be changed manually", id)
	}
	return s.mutate(ctx, "uninstall", id, func(ctx context.Context) error {
		_, e := s.runLocked(ctx, "uninstall", id)
		if e != nil {
			return e
		}
		packages, e := s.installedLocked(ctx)
		if e != nil {
			return e
		}
		if containsPackage(packages, id) {
			return fmt.Errorf("Scoop uninstall completed but %q is still installed in managed root %s", id, s.root)
		}
		return nil
	})
}
func (s *Scoop) Refresh(ctx context.Context) error {
	return s.mutate(ctx, "refresh", "", func(ctx context.Context) error { _, e := s.runLocked(ctx, "update"); return e })
}

func (s *Scoop) withReady(ctx context.Context, action string, fn func(context.Context) ([]Package, error)) ([]Package, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	if err := s.ensureReadyLocked(ctx); err != nil {
		return nil, err
	}
	return fn(ctx)
}
func (s *Scoop) mutate(ctx context.Context, action, id string, fn func(context.Context) error) error {
	if id != "" && !ValidPackageID(id) {
		return fmt.Errorf("invalid package ID %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = Operation{Action: action, State: "running"}
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	if err := s.ensureReadyLocked(ctx); err != nil {
		s.last = Operation{Action: action, State: "failure", Message: err.Error()}
		return err
	}
	err := fn(ctx)
	if err != nil {
		s.last = Operation{Action: action, State: "failure", Message: err.Error()}
		return err
	}
	s.last.State = "success"
	s.last.Message = "completed"
	return nil
}
func (s *Scoop) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, s.timeout)
}

func (s *Scoop) ensureReadyLocked(ctx context.Context) error {
	if _, err := os.Stat(s.EntryPoint()); err == nil {
		if _, markerErr := os.Stat(s.ownerMarker()); markerErr == nil {
			return s.ensureManagedGitLocked(ctx)
		}
		return fmt.Errorf("Scoop runtime at %s is not RunPilot-managed; choose an empty Scoop root instead", s.root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot access managed Scoop root %s: %w", s.root, err)
	}
	brokenMarker := filepath.Join(s.root, "apps", "scoop")
	if _, err := os.Stat(brokenMarker); err == nil {
		return fmt.Errorf("managed Scoop installation at %s is incomplete; expected %s", s.root, s.EntryPoint())
	}
	installer := filepath.Join(s.bootstrapDir, "install.ps1")
	wrapper := filepath.Join(s.bootstrapDir, "bootstrap.ps1")
	if err := os.MkdirAll(s.bootstrapDir, 0o700); err != nil {
		return fmt.Errorf("cannot create Scoop bootstrap directory: %w", err)
	}
	if err := s.downloader.Download(ctx, ScoopInstallerURL, installer); err != nil {
		return fmt.Errorf("Scoop bootstrap failed: unable to download the Scoop installer from GitHub: %w", err)
	}
	if err := os.WriteFile(wrapper, []byte(scoopBootstrapWrapper), 0o600); err != nil {
		return fmt.Errorf("cannot create Scoop bootstrap wrapper: %w", err)
	}
	result, err := s.runner.Run(ctx, Command{Path: s.powerShell, Args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", wrapper, installer, s.root, filepath.Join(s.root, "cache"), filepath.Join(s.root, "global")}, Env: s.environment(true), Dir: s.bootstrapDir})
	s.captureOutput(result)
	if err != nil {
		return s.commandError("Scoop bootstrap failed", result, err)
	}
	if _, err := os.Stat(s.EntryPoint()); err != nil {
		return fmt.Errorf("Scoop bootstrap completed but managed entrypoint %s was not created", s.EntryPoint())
	}
	if err := os.WriteFile(s.ownerMarker(), []byte("RunPilot-managed Scoop root\n"), 0o600); err != nil {
		return fmt.Errorf("Scoop bootstrap completed but RunPilot cannot mark managed root %s: %w", s.root, err)
	}
	return s.ensureManagedGitLocked(ctx)
}

// Scoop uses Git to maintain its own repository and buckets. Keep this
// prerequisite inside the managed root instead of relying on a user Scoop or
// an arbitrary Git discovered from the host PATH.
func (s *Scoop) ensureManagedGitLocked(ctx context.Context) error {
	if _, err := os.Stat(s.managedGitRoot()); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot access managed Scoop Git at %s: %w", s.managedGitRoot(), err)
	}
	result, err := s.runLocked(ctx, "install", "git")
	if err != nil {
		return s.commandError("Scoop bootstrap failed while installing managed Git", result, err)
	}
	if _, err := os.Stat(s.managedGitRoot()); err != nil {
		return fmt.Errorf("Scoop bootstrap installed Git but managed Git was not found at %s", s.managedGitRoot())
	}
	return nil
}
func (s *Scoop) runLocked(ctx context.Context, args ...string) (CommandResult, error) {
	r, err := s.runner.Run(ctx, Command{Path: s.powerShell, Args: append([]string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", s.EntryPoint()}, args...), Env: s.environment(false), Dir: s.root})
	s.captureOutput(r)
	if err != nil {
		return r, s.commandError("managed Scoop command failed", r, err)
	}
	return r, nil
}
func (s *Scoop) captureOutput(r CommandResult) {
	if s.last.State != "running" {
		return
	}
	output := strings.TrimSpace(strings.TrimSpace(r.Stdout) + "\n" + strings.TrimSpace(r.Stderr))
	if len(output) > 4000 {
		output = output[:4000]
	}
	if output != "" {
		s.last.Output = output
	}
}
func (s *Scoop) commandError(prefix string, r CommandResult, err error) error {
	detail := strings.TrimSpace(strings.TrimSpace(r.Stderr) + "\n" + strings.TrimSpace(r.Stdout))
	if len(detail) > 2000 {
		detail = detail[:2000]
	}
	if detail != "" {
		return fmt.Errorf("%s: %s", prefix, detail)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}
func (s *Scoop) environment(bootstrap bool) []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+4)
	for _, v := range base {
		upper := strings.ToUpper(v)
		if strings.HasPrefix(upper, "SCOOP=") || strings.HasPrefix(upper, "SCOOP_GLOBAL=") || strings.HasPrefix(upper, "SCOOP_CACHE=") || strings.HasPrefix(upper, "XDG_CONFIG_HOME=") || strings.HasPrefix(upper, "PATH=") {
			continue
		}
		out = append(out, v)
	}
	systemRoot := os.Getenv("SystemRoot")
	safePath := filepath.Join(systemRoot, "System32") + ";" + filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0")
	out = append(out, "PATH="+safePath, "XDG_CONFIG_HOME="+filepath.Join(s.root, ".config"))
	if !bootstrap {
		out = append(out, "SCOOP="+s.root, "SCOOP_CACHE="+filepath.Join(s.root, "cache"))
	}
	return out
}
func (s *Scoop) installedLocked(ctx context.Context) ([]Package, error) {
	// Scoop export is structured JSON on supported Scoop versions. Fall back to
	// the normal list command only when that output is not available/parseable.
	if r, err := s.runLocked(ctx, "export"); err == nil {
		if packages, decodeErr := decodeExport(s.definition.ID, r.Stdout); decodeErr == nil {
			for i := range packages {
				packages[i].Protected = isScoopDependency(packages[i].ID)
			}
			return packages, nil
		}
	}
	r, e := s.runLocked(ctx, "list")
	if e != nil {
		return nil, e
	}
	packages := parseListOutput(s.definition.ID, r.Stdout)
	for i := range packages {
		packages[i].Protected = isScoopDependency(packages[i].ID)
	}
	return packages, nil
}
func (s *Scoop) bucketsLocked(ctx context.Context) ([]Bucket, error) {
	r, err := s.runLocked(ctx, "bucket", "list")
	if err != nil {
		return nil, err
	}
	return parseBucketList(r.Stdout), nil
}

func ValidPackageID(id string) bool { return packageID.MatchString(id) && !strings.HasPrefix(id, "-") }

func packageLeaf(id string) string {
	parts := strings.Split(id, "/")
	return parts[len(parts)-1]
}

func samePackage(a, b string) bool {
	return strings.EqualFold(packageLeaf(a), packageLeaf(b))
}

func isScoopDependency(id string) bool {
	switch strings.ToLower(packageLeaf(id)) {
	case "git", "7zip":
		return true
	default:
		return false
	}
}
func containsPackage(items []Package, id string) bool {
	name := strings.ToLower(strings.TrimPrefix(id, strings.Split(id, "/")[0]+"/"))
	for _, p := range items {
		if strings.EqualFold(p.ID, id) || strings.EqualFold(p.Name, id) || strings.EqualFold(p.Name, name) {
			return true
		}
	}
	return false
}
func firstNonEmptyLine(v string) string {
	for _, line := range strings.Split(v, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func parseListOutput(provider, output string) []Package {
	out := make([]Package, 0)
	for _, line := range strings.Split(output, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || strings.EqualFold(f[0], "name") || strings.HasPrefix(f[0], "-") || strings.Contains(strings.ToLower(line), "installed apps") {
			continue
		}
		if !ValidPackageID(f[0]) {
			continue
		}
		out = append(out, Package{Provider: provider, ID: f[0], Name: f[0], Version: f[1], Bucket: field(f, 2), Installed: true})
	}
	return out
}
func parseSearchOutput(provider, output string) []Package {
	out := make([]Package, 0)
	for _, line := range strings.Split(output, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.EqualFold(f[0], "name") || strings.HasPrefix(f[0], "-") || strings.Contains(strings.ToLower(line), "results") {
			continue
		}
		if !ValidPackageID(f[0]) {
			continue
		}
		p := Package{Provider: provider, ID: f[0], Name: f[0], Version: field(f, 1), Bucket: field(f, 2)}
		if len(f) > 3 {
			p.Description = strings.Join(f[3:], " ")
		}
		out = append(out, p)
	}
	return out
}
func parseStatusOutput(provider, output string) []Package {
	out := make([]Package, 0)
	for _, line := range strings.Split(output, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || strings.EqualFold(f[0], "name") || strings.HasPrefix(f[0], "-") {
			continue
		}
		announcement := strings.ToLower(strings.Join(f, " "))
		if strings.HasPrefix(announcement, "everything is ") || strings.HasPrefix(announcement, "no apps ") {
			continue
		}
		if !ValidPackageID(f[0]) {
			continue
		}
		out = append(out, Package{Provider: provider, ID: f[0], Name: f[0], Version: f[1], Available: f[2], Installed: true, UpdateAvailable: f[1] != f[2]})
	}
	return out
}
func parseBucketList(output string) []Bucket {
	out := make([]Bucket, 0)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.EqualFold(fields[0], "name") || strings.HasPrefix(fields[0], "-") {
			continue
		}
		if !ValidPackageID(fields[0]) || strings.Contains(fields[0], "/") {
			continue
		}
		out = append(out, Bucket{Name: fields[0], Source: field(fields, 1), Protected: strings.EqualFold(fields[0], "main")})
	}
	return out
}
func field(v []string, n int) string {
	if len(v) > n {
		return v[n]
	}
	return ""
}

// Retain JSON support for future Scoop structured output without exposing it to callers.
func decodeExport(provider, value string) ([]Package, error) {
	var payload struct {
		Apps []struct{ Name, Version, Source string } `json:"apps"`
	}
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return nil, err
	}
	out := make([]Package, 0, len(payload.Apps))
	for _, a := range payload.Apps {
		out = append(out, Package{Provider: provider, ID: a.Name, Name: a.Name, Version: a.Version, Bucket: a.Source, Installed: true})
	}
	return out, nil
}
