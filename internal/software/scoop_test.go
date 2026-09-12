package software

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/szilab/RunPilot/internal/model"
)

type fakeDownloader struct{ err error }

func (d fakeDownloader) Download(_ context.Context, _ string, destination string) error {
	if d.err != nil {
		return d.err
	}
	return os.WriteFile(destination, []byte("# installer"), 0o600)
}

type fakeRunner struct {
	commands []Command
	root     string
	list     string
	export   string
}

func (r *fakeRunner) Run(_ context.Context, c Command) (CommandResult, error) {
	r.commands = append(r.commands, c)
	if strings.Contains(strings.Join(c.Args, " "), "install.ps1") {
		entry := filepath.Join(r.root, "apps", "scoop", "current", "bin", "scoop.ps1")
		if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
			return CommandResult{}, err
		}
		return CommandResult{}, os.WriteFile(entry, []byte("# scoop"), 0o600)
	}
	if len(c.Args) >= 2 && c.Args[len(c.Args)-2] == "install" && c.Args[len(c.Args)-1] == "git" {
		return CommandResult{}, os.MkdirAll(filepath.Join(r.root, "apps", "git", "current"), 0o755)
	}
	if strings.HasSuffix(c.Args[len(c.Args)-1], "export") {
		if r.export != "" {
			return CommandResult{Stdout: r.export}, nil
		}
		return CommandResult{Stdout: `{"apps":[{"Name":"git","Version":"2.0","Source":"main"}]}`}, nil
	}
	if strings.HasSuffix(c.Args[len(c.Args)-1], "list") {
		return CommandResult{Stdout: r.list}, nil
	}
	return CommandResult{Stdout: "Scoop 0.5.0"}, nil
}

func scoopDefinition(root string) model.SoftwareProviderDefinition {
	return model.SoftwareProviderDefinition{ID: "scoop", Name: "RunPilot Scoop", Type: model.SoftwareProviderScoop, Scoop: &model.ScoopProviderSpec{Root: root}}
}

func TestScoopBootstrapsOnlyManagedEntrypoint(t *testing.T) {
	root := filepath.Join(t.TempDir(), "managed-scoop")
	runner := &fakeRunner{root: root, list: "Name Version Source\n---- ------- ------\ngit 2.0 main\n"}
	p, err := NewScoop(scoopDefinition(root), t.TempDir(), ScoopOptions{Runner: runner, Downloader: fakeDownloader{}, PowerShell: "C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := p.Installed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "git" {
		t.Fatalf("installed = %#v", items)
	}
	if len(runner.commands) != 3 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	bootstrap, gitInstall, list := runner.commands[0], runner.commands[1], runner.commands[2]
	if !strings.Contains(strings.Join(bootstrap.Args, "|"), "|"+root+"|") {
		t.Fatalf("bootstrap did not set managed root: %#v", bootstrap.Args)
	}
	if !strings.Contains(strings.Join(list.Args, "|"), "-File|"+p.EntryPoint()+"|export") {
		t.Fatalf("export did not invoke managed entrypoint: %#v", list.Args)
	}
	if !strings.Contains(strings.Join(gitInstall.Args, "|"), "-File|"+p.EntryPoint()+"|install|git") {
		t.Fatalf("Git was not installed through the managed entrypoint: %#v", gitInstall.Args)
	}
	if _, err := os.Stat(filepath.Join(root, "apps", "git", "current")); err != nil {
		t.Fatalf("managed Git was not installed: %v", err)
	}
	if list.Path == "scoop" || strings.Contains(strings.ToLower(list.Path), "scoop.exe") {
		t.Fatalf("provider PATH-discovered Scoop: %q", list.Path)
	}
	if strings.Contains(strings.Join(bootstrap.Env, "\n"), "SCOOP=") || strings.Contains(strings.Join(bootstrap.Env, "\n"), "SCOOP_CACHE=") {
		t.Fatalf("bootstrap environment must not let the installer persist Scoop variables: %#v", bootstrap.Env)
	}
	joined := strings.Join(list.Env, "\n")
	if !strings.Contains(joined, "SCOOP="+root) || !strings.Contains(joined, "SCOOP_CACHE="+filepath.Join(root, "cache")) {
		t.Fatalf("managed environment missing: %q", joined)
	}
}

func TestScoopRejectsInjectionSensitivePackageID(t *testing.T) {
	p, err := NewScoop(scoopDefinition(t.TempDir()), t.TempDir(), ScoopOptions{Runner: &fakeRunner{}, Downloader: fakeDownloader{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"git; whoami", "-global", "git`whoami", "git\nupdate"} {
		if err := p.Install(context.Background(), id); err == nil {
			t.Fatalf("Install(%q) accepted unsafe value", id)
		}
	}
}

func TestScoopBootstrapFailureIsActionable(t *testing.T) {
	p, err := NewScoop(scoopDefinition(t.TempDir()), t.TempDir(), ScoopOptions{Runner: &fakeRunner{}, Downloader: fakeDownloader{err: errors.New("offline")}})
	if err != nil {
		t.Fatal(err)
	}
	status := p.Status(context.Background())
	if status.State != StateUnavailable || !strings.Contains(status.Message, "unable to download") {
		t.Fatalf("status = %#v", status)
	}
}

func TestScoopNeverAdoptsAnUnmarkedInstallation(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "apps", "scoop", "current", "bin", "scoop.ps1")
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("# user scoop"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := NewScoop(scoopDefinition(root), root, ScoopOptions{Runner: &fakeRunner{}, Downloader: fakeDownloader{}})
	if err != nil {
		t.Fatal(err)
	}
	status := p.Status(context.Background())
	if status.State != StateUnavailable || !strings.Contains(status.Message, "not RunPilot-managed") {
		t.Fatalf("status = %#v", status)
	}
}

func TestUninstallVerifiesPackageDisappeared(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "apps", "scoop", "current", "bin", "scoop.ps1")
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("# managed scoop"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".runpilot-scoop-owner"), []byte("managed"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{root: root, export: `{"apps":[{"Name":"caddy","Version":"2.0","Source":"main"}]}`}
	p, err := NewScoop(scoopDefinition(root), root, ScoopOptions{Runner: runner, Downloader: fakeDownloader{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Install(context.Background(), "caddy"); err != nil {
		t.Fatalf("install should verify present package: %v", err)
	}
	if err := p.Uninstall(context.Background(), "caddy"); err == nil || !strings.Contains(err.Error(), "still installed") {
		t.Fatalf("uninstall did not verify state: %v", err)
	}
}

func TestParsersTolerateHeadingsAndWhitespace(t *testing.T) {
	items := parseListOutput("scoop", "Installed apps:\n\nName    Version  Source\n----    -------  ------\n git    2.47.1   main\n")
	if len(items) != 1 || items[0].Version != "2.47.1" || items[0].Bucket != "main" {
		t.Fatalf("items = %#v", items)
	}
	updates := parseStatusOutput("scoop", "Name Version Available\n---- ------- ---------\ngit 2.46 2.47\n")
	if len(updates) != 1 || !updates[0].UpdateAvailable {
		t.Fatalf("updates = %#v", updates)
	}
	if updates := parseStatusOutput("scoop", "Everything is ok!\n"); len(updates) != 0 {
		t.Fatalf("status summary was parsed as package update: %#v", updates)
	} else if updates == nil {
		t.Fatal("empty update results must serialize as an array, not null")
	}
}

func TestScoopDependenciesRemainVisibleButProtected(t *testing.T) {
	items := parseListOutput("scoop", "Name Version Source\n---- ------- ------\ngit 2.47 main\n7zip 24.09 main\ncaddy 2.8 main\n")
	for i := range items {
		items[i].Protected = isScoopDependency(items[i].ID)
	}
	if len(items) != 3 || !items[0].Protected || !items[1].Protected || items[2].Protected {
		t.Fatalf("protected package visibility = %#v", items)
	}
	if !samePackage("main/git", "git") || samePackage("git", "7zip") {
		t.Fatal("package matching was not normalized")
	}
}

func TestBucketParserAndValidation(t *testing.T) {
	buckets := parseBucketList("Name Source\n---- ------\nmain https://github.com/ScoopInstaller/Main\nextras https://github.com/ScoopInstaller/Extras\n")
	if len(buckets) != 2 || !buckets[0].Protected || buckets[1].Protected || buckets[1].Source == "" {
		t.Fatalf("buckets = %#v", buckets)
	}
	p, err := NewScoop(scoopDefinition(t.TempDir()), t.TempDir(), ScoopOptions{Runner: &fakeRunner{}, Downloader: fakeDownloader{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AddBucket(context.Background(), "extras", "http://example.com/extras"); err == nil {
		t.Fatal("non-HTTPS bucket source was accepted")
	}
	if err := p.RemoveBucket(context.Background(), "main"); err == nil {
		t.Fatal("main bucket removal was accepted")
	}
	if err := p.Uninstall(context.Background(), "git"); err == nil {
		t.Fatal("manual Git removal was accepted")
	}
}

func TestScoopBootstrapWorkspaceIsAbsoluteForRelativeDataDir(t *testing.T) {
	p, err := NewScoop(scoopDefinition(""), "relative-runpilot-data", ScoopOptions{Runner: &fakeRunner{}, Downloader: fakeDownloader{}})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(p.bootstrapDir) {
		t.Fatalf("bootstrap directory must be absolute: %q", p.bootstrapDir)
	}
}
