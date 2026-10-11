package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const packageName = "runpilot"

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:(?:-[0-9A-Za-z.-]+)|(?:\.r[0-9]+))?(?:\+[0-9A-Za-z.-]+)?$`)

func main() {
	version := flag.String("version", "", "ASUSTOR App Central package version")
	binary := flag.String("binary", "", "Linux amd64 RunPilot executable")
	source := flag.String("source", "packaging/asustor", "ASUSTOR package source directory")
	outDir := flag.String("out", "dist", "Artifact output directory")
	flag.Parse()
	if *version == "" || *binary == "" {
		fail("-version and -binary are required")
	}
	if !validPackageVersion(*version) {
		fail("invalid package version %q", *version)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fail("create output directory: %v", err)
	}
	artifact := filepath.Join(*outDir, packageName+"_"+*version+"_x86-64.apk")
	if err := buildAPK(*source, *binary, *version, artifact); err != nil {
		fail("build APKG: %v", err)
	}
	if err := validateAPK(artifact, *version); err != nil {
		_ = os.Remove(artifact)
		fail("validate APKG: %v", err)
	}
	hash, err := fileSHA256(artifact)
	if err != nil {
		fail("checksum APK: %v", err)
	}
	if err := os.WriteFile(artifact+".sha256", []byte(hash+"  "+filepath.Base(artifact)+"\n"), 0o644); err != nil {
		fail("write checksum: %v", err)
	}
	fmt.Println(artifact)
}

func validPackageVersion(version string) bool {
	return versionPattern.MatchString(version)
}

func buildAPK(source, binary, version, artifact string) error {
	info, err := os.Stat(binary)
	if err != nil {
		return fmt.Errorf("read binary: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("binary is not a regular file")
	}
	controlDir := filepath.Join(source, "CONTROL")
	if _, err := os.Stat(filepath.Join(controlDir, "icon.png")); err != nil {
		return fmt.Errorf("read package icon: %w", err)
	}
	var config map[string]any
	configBytes, err := os.ReadFile(filepath.Join(controlDir, "config.json"))
	if err != nil {
		return fmt.Errorf("read App Central config: %w", err)
	}
	if err := json.Unmarshal(configBytes, &config); err != nil {
		return fmt.Errorf("parse App Central config: %w", err)
	}
	general, ok := config["general"].(map[string]any)
	if !ok || general["package"] != packageName || general["architecture"] != "x86-64" {
		return errors.New("App Central config must identify runpilot for x86-64")
	}
	general["version"] = version
	configBytes, err = json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode App Central config: %w", err)
	}
	configBytes = append(configBytes, '\n')

	temp, err := os.MkdirTemp("", "runpilot-asustor-package-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	controlRoot := filepath.Join(temp, "CONTROL")
	dataRoot := filepath.Join(temp, "data")
	if err := copyTree(controlDir, controlRoot); err != nil {
		return fmt.Errorf("stage CONTROL: %w", err)
	}
	if err := os.WriteFile(filepath.Join(controlRoot, "config.json"), configBytes, 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dataRoot, "bin"), 0o755); err != nil {
		return err
	}
	if err := copyFile(binary, filepath.Join(dataRoot, "bin", "runpilot"), 0o755); err != nil {
		return fmt.Errorf("stage executable: %w", err)
	}
	controlArchive, err := tarGzip(controlRoot)
	if err != nil {
		return fmt.Errorf("archive CONTROL: %w", err)
	}
	dataArchive, err := tarGzip(dataRoot)
	if err != nil {
		return fmt.Errorf("archive application data: %w", err)
	}
	if err := writeZip(artifact, map[string][]byte{
		"apkg-version":   []byte("2.0\n"),
		"control.tar.gz": controlArchive,
		"data.tar.gz":    dataArchive,
	}); err != nil {
		return err
	}
	return nil
}

func validateAPK(path, wantVersion string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("APK is not a ZIP package: %w", err)
	}
	defer zr.Close()
	members := map[string]*zip.File{}
	for _, member := range zr.File {
		if _, exists := members[member.Name]; exists {
			return fmt.Errorf("duplicate outer member %q", member.Name)
		}
		members[member.Name] = member
	}
	if len(members) != 3 {
		return fmt.Errorf("expected three outer members, got %d", len(members))
	}
	versionText, err := readZipMember(members["apkg-version"])
	if err != nil || string(versionText) != "2.0\n" {
		return errors.New("missing APKG format version 2.0")
	}
	control, err := readZipMember(members["control.tar.gz"])
	if err != nil {
		return fmt.Errorf("read control archive: %w", err)
	}
	data, err := readZipMember(members["data.tar.gz"])
	if err != nil {
		return fmt.Errorf("read data archive: %w", err)
	}
	controlFiles, err := readTarGzip(control)
	if err != nil {
		return fmt.Errorf("read CONTROL tarball: %w", err)
	}
	dataFiles, err := readTarGzip(data)
	if err != nil {
		return fmt.Errorf("read data tarball: %w", err)
	}
	for _, name := range []string{"config.json", "icon.png", "description.txt", "changelog.txt", "start-stop.sh"} {
		if _, ok := controlFiles[name]; !ok {
			return fmt.Errorf("CONTROL archive is missing %s", name)
		}
	}
	config := struct {
		General struct {
			Package      string `json:"package"`
			Version      string `json:"version"`
			Architecture string `json:"architecture"`
			Firmware     string `json:"firmware"`
		} `json:"general"`
		Desktop struct {
			App struct {
				Type string `json:"type"`
				Port int    `json:"port"`
			} `json:"app"`
		} `json:"adm-desktop"`
	}{}
	if err := json.Unmarshal(controlFiles["config.json"].data, &config); err != nil {
		return fmt.Errorf("parse packaged config: %w", err)
	}
	if config.General.Package != packageName || config.General.Version != wantVersion || config.General.Architecture != "x86-64" || config.General.Firmware != "5.0.0" {
		return errors.New("packaged App Central metadata does not match version/platform requirements")
	}
	if config.Desktop.App.Type != "custom" || config.Desktop.App.Port != 9070 {
		return errors.New("App Central launcher is not configured for RunPilot on port 9070")
	}
	if _, ok := dataFiles["bin/runpilot"]; !ok {
		return errors.New("application archive is missing bin/runpilot")
	}
	if !dataFiles["bin/runpilot"].mode.IsRegular() || dataFiles["bin/runpilot"].mode.Perm()&0o111 == 0 {
		return errors.New("packaged RunPilot binary is not executable")
	}
	if !pngIs90(controlFiles["icon.png"].data) {
		return errors.New("App Central icon must be a 90x90 PNG")
	}
	if controlFiles["start-stop.sh"].mode.Perm()&0o111 == 0 || !strings.Contains(string(controlFiles["start-stop.sh"].data), "start-stop-daemon") {
		return errors.New("start-stop hook does not use the ADM service manager")
	}
	if !strings.Contains(string(controlFiles["start-stop.sh"].data), "--bind-address 0.0.0.0") || !strings.Contains(string(controlFiles["start-stop.sh"].data), "--port 9070") {
		return errors.New("start-stop hook does not configure the App Central launcher address")
	}
	lifecycle := string(controlFiles["start-stop.sh"].data)
	for _, requirement := range []string{
		"DATA_DIR=/volume1/.@plugins/AppCentral/runpilot/data",
		"-c admin:administrators",
		"-s TERM",
		"-s KILL",
		"Refusing to use unmarked data directory",
		"status)",
	} {
		if !strings.Contains(lifecycle, requirement) {
			return fmt.Errorf("lifecycle hook is missing %q", requirement)
		}
	}
	return nil
}

type archivedFile struct {
	data []byte
	mode fs.FileMode
}

func readTarGzip(data []byte) (map[string]archivedFile, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := map[string]archivedFile{}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		clean := filepath.ToSlash(filepath.Clean(header.Name))
		if clean == "." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			continue
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("unexpected tar entry type for %s", clean)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		files[clean] = archivedFile{data: content, mode: fs.FileMode(header.Mode)}
	}
	return files, nil
}

func tarGzip(root string) ([]byte, error) {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	gz.Header.ModTime = time.Unix(0, 0)
	gz.Header.OS = 255
	tw := tar.NewWriter(gz)
	rootHeader := &tar.Header{Name: ".", Mode: 0o755, Typeflag: tar.TypeDir, ModTime: time.Unix(0, 0), Uid: 0, Gid: 0, Uname: "root", Gname: "root", Format: tar.FormatUSTAR}
	if err := tw.WriteHeader(rootHeader); err != nil {
		return nil, err
	}
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	for _, rel := range paths {
		path := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil, fmt.Errorf("unsupported package entry %s", rel)
		}
		name := rel
		mode := int64(info.Mode().Perm())
		typeflag := byte(tar.TypeReg)
		size := info.Size()
		if info.IsDir() {
			name += "/"
			mode = 0o755
			typeflag = tar.TypeDir
			size = 0
		}
		header := &tar.Header{Name: name, Mode: mode, Size: size, Typeflag: typeflag, ModTime: time.Unix(0, 0), Uid: 0, Gid: 0, Uname: "root", Gname: "root", Format: tar.FormatUSTAR}
		if err := tw.WriteHeader(header); err != nil {
			return nil, err
		}
		if typeflag == tar.TypeReg {
			file, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			_, copyErr := io.Copy(tw, file)
			closeErr := file.Close()
			if copyErr != nil {
				return nil, copyErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeZip(path string, members map[string][]byte) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(file)
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.ModifiedDate = 0x21 // 1980-01-01, encoded directly to avoid local timezone differences.
		header.ModifiedTime = 0
		header.SetMode(0o644)
		entry, err := zw.CreateHeader(header)
		if err != nil {
			_ = zw.Close()
			_ = file.Close()
			return err
		}
		if _, err := entry.Write(members[name]); err != nil {
			_ = zw.Close()
			_ = file.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func pngIs90(data []byte) bool {
	return len(data) >= 24 && string(data[:8]) == "\x89PNG\r\n\x1a\n" &&
		int(data[16])<<24|int(data[17])<<16|int(data[18])<<8|int(data[19]) == 90 &&
		int(data[20])<<24|int(data[21])<<16|int(data[22])<<8|int(data[23]) == 90
}

func copyTree(source, dest string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported package source entry %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(source, dest string, mode fs.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func readZipMember(file *zip.File) ([]byte, error) {
	if file == nil {
		return nil, errors.New("required APK member is missing")
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
