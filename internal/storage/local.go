package storage

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/szilab/RunPilot/internal/model"
)

type Local struct {
	spec model.LocalStorageSpec
	root string
}

func NewLocal(spec model.LocalStorageSpec) (*Local, error) {
	if spec.Scope != model.LocalStorageScopeRoot && spec.Scope != model.LocalStorageScopeHost {
		return nil, fmt.Errorf("local storage scope must be root or host")
	}
	if spec.Scope == model.LocalStorageScopeHost && strings.TrimSpace(spec.Root) != "" {
		return nil, fmt.Errorf("host-scoped local storage must not specify a root")
	}
	l := &Local{spec: spec}
	if spec.Scope == model.LocalStorageScopeRoot {
		if strings.TrimSpace(spec.Root) == "" || !filepath.IsAbs(spec.Root) {
			return nil, fmt.Errorf("root-scoped local storage requires an absolute root")
		}
		root, err := filepath.EvalSymlinks(filepath.Clean(spec.Root))
		if err != nil {
			return l, nil
		} // unavailable is reported on use
		l.root = root
	}
	return l, nil
}
func (l *Local) Capabilities() Capabilities {
	return Capabilities{Browse: true, Download: true, Upload: true, CreateDirectory: true, Rename: true, Move: true, Copy: true, Delete: true, TextEdit: true}
}
func (l *Local) State() State {
	if l.spec.Scope == model.LocalStorageScopeHost {
		return State{Status: "ready"}
	}
	info, err := os.Stat(l.spec.Root)
	if errors.Is(err, os.ErrNotExist) {
		return State{Status: "unavailable", Reason: "Root directory does not exist"}
	}
	if errors.Is(err, os.ErrPermission) {
		return State{Status: "unavailable", Reason: "Permission denied"}
	}
	if err != nil {
		return State{Status: "unavailable", Reason: "Root directory is unavailable"}
	}
	if !info.IsDir() {
		return State{Status: "unavailable", Reason: "Root path is not a directory"}
	}
	return State{Status: "ready"}
}

func (l *Local) ResolveBackupSource(p string) (BackupSource, error) {
	abs, err := l.resolve(p, false)
	if err != nil {
		return BackupSource{}, err
	}
	return BackupSource{Path: abs}, nil
}

func validNamespace(p string) ([]string, error) {
	if p == "" {
		return nil, nil
	}
	if strings.Contains(p, "\\") || strings.HasPrefix(p, "/") {
		return nil, fmt.Errorf("invalid provider path")
	}
	parts := strings.Split(p, "/")
	for i, x := range parts {
		if x == "" || x == "." || x == ".." || (strings.Contains(x, ":") && !(i == 0 && len(x) == 2 && x[1] == ':')) {
			return nil, fmt.Errorf("invalid provider path")
		}
	}
	return parts, nil
}
func childName(name string) error {
	if strings.TrimSpace(name) == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\:") {
		return fmt.Errorf("name must be a single filesystem child")
	}
	if runtime.GOOS == "windows" && strings.ContainsAny(name, "<>\"|?*") {
		return fmt.Errorf("invalid Windows filename")
	}
	return nil
}
func (l *Local) resolve(p string, allowMissing bool) (string, error) {
	parts, err := validNamespace(p)
	if err != nil {
		return "", err
	}
	if l.spec.Scope == model.LocalStorageScopeRoot && len(parts) > 0 && len(parts[0]) == 2 && parts[0][1] == ':' {
		return "", fmt.Errorf("invalid provider path")
	}
	if l.spec.Scope == model.LocalStorageScopeHost {
		if len(parts) == 0 {
			return "", nil
		}
		roots := filesystemRoots()
		drive := strings.TrimSuffix(parts[0], ":")
		found := ""
		for _, r := range roots {
			if strings.EqualFold(strings.TrimSuffix(filepath.ToSlash(r), "/"), drive+":") || (r == "/" && parts[0] == "root") {
				found = r
				break
			}
		}
		if found == "" {
			return "", fmt.Errorf("unknown filesystem root")
		}
		return filepath.Join(append([]string{found}, parts[1:]...)...), nil
	}
	if l.root == "" {
		root, err := filepath.EvalSymlinks(filepath.Clean(l.spec.Root))
		if err != nil {
			return "", fmt.Errorf("storage root unavailable: %w", err)
		}
		l.root = root
	}
	candidate := filepath.Join(append([]string{l.root}, parts...)...)
	check := candidate
	if allowMissing {
		check = filepath.Dir(candidate)
	}
	real, err := filepath.EvalSymlinks(check)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(l.root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes configured root")
	}
	return candidate, nil
}
func pathForHost(abs string) string {
	if abs == "/" {
		return "root"
	}
	v := filepath.ToSlash(abs)
	if len(v) >= 2 && v[1] == ':' {
		return strings.ToUpper(v[:1]) + ":/" + strings.TrimPrefix(v[2:], "/")
	}
	return v
}
func (l *Local) List(p string) (Listing, error) {
	return l.ListWithOptions(p, ListOptions{})
}

func (l *Local) ListWithOptions(p string, options ListOptions) (Listing, error) {
	if l.spec.Scope == model.LocalStorageScopeHost && p == "" {
		entries := []Entry{}
		for _, r := range filesystemRoots() {
			n := strings.TrimSuffix(filepath.ToSlash(r), "/")
			if n == "" {
				n = "root"
			}
			entries = append(entries, Entry{Name: n, Path: strings.TrimSuffix(n, "/"), Type: "filesystem-root"})
		}
		return Listing{Path: "", Entries: entries}, nil
	}
	abs, err := l.resolve(p, false)
	if err != nil {
		return Listing{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Listing{}, err
	}
	if !info.IsDir() {
		return Listing{}, fmt.Errorf("path is not a directory")
	}
	list, err := os.ReadDir(abs)
	if err != nil {
		return Listing{}, err
	}
	entries := make([]Entry, 0, len(list))
	for _, de := range list {
		if !options.ShowHidden && hiddenEntry(abs, de) {
			continue
		}
		i, e := de.Info()
		if e != nil {
			return Listing{}, e
		}
		typ := "file"
		if i.IsDir() {
			typ = "directory"
		}
		size := i.Size()
		mod := i.ModTime()
		cp := de.Name()
		if p != "" {
			cp = p + "/" + cp
		}
		entries = append(entries, Entry{Name: de.Name(), Path: cp, Type: typ, Size: &size, ModifiedAt: &mod})
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		ad := a.Type != "file"
		bd := b.Type != "file"
		if ad != bd {
			return ad
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	var parent *string
	if p != "" {
		x := strings.Join(strings.Split(p, "/")[:len(strings.Split(p, "/"))-1], "/")
		parent = &x
	}
	return Listing{Path: p, ParentPath: parent, Entries: entries}, nil
}
func (l *Local) Open(p string) (*ObjectReader, error) {
	abs, e := l.resolve(p, false)
	if e != nil {
		return nil, e
	}
	i, e := os.Stat(abs)
	if e != nil {
		return nil, e
	}
	if i.IsDir() {
		return nil, fmt.Errorf("directories cannot be downloaded")
	}
	f, e := os.Open(abs)
	if e != nil {
		return nil, e
	}
	size := i.Size()
	mod := i.ModTime()
	return &ObjectReader{Reader: f, Name: filepath.Base(abs), Size: &size, ModifiedAt: &mod}, nil
}
func (l *Local) Upload(parent, name string, r io.Reader) error {
	if e := childName(name); e != nil {
		return e
	}
	dir, e := l.resolve(parent, false)
	if e != nil {
		return e
	}
	i, e := os.Stat(dir)
	if e != nil {
		return e
	}
	if !i.IsDir() {
		return fmt.Errorf("destination is not a directory")
	}
	target, e := l.resolve(join(parent, name), true)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	_, e = io.Copy(f, r)
	return e
}
func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "/" + b
}
func (l *Local) CreateDirectory(parent, name string) error {
	if e := childName(name); e != nil {
		return e
	}
	dir, e := l.resolve(parent, false)
	if e != nil {
		return e
	}
	i, e := os.Stat(dir)
	if e != nil {
		return e
	}
	if !i.IsDir() {
		return fmt.Errorf("parent is not a directory")
	}
	target, e := l.resolve(join(parent, name), true)
	if e != nil {
		return e
	}
	return os.Mkdir(target, 0755)
}
func (l *Local) Rename(p, name string) error {
	if e := childName(name); e != nil {
		return e
	}
	if p == "" || l.isRoot(p) {
		return fmt.Errorf("filesystem roots cannot be renamed")
	}
	src, e := l.resolve(p, false)
	if e != nil {
		return e
	}
	dst, e := l.resolve(join(parentPath(p), name), true)
	if e != nil {
		return e
	}
	if _, e = os.Lstat(dst); !errors.Is(e, os.ErrNotExist) {
		return fmt.Errorf("destination already exists")
	}
	return os.Rename(src, dst)
}
func parentPath(p string) string {
	n := strings.LastIndex(p, "/")
	if n < 0 {
		return ""
	}
	return p[:n]
}
func (l *Local) isRoot(p string) bool {
	return l.spec.Scope == model.LocalStorageScopeHost && !strings.Contains(p, "/")
}
func (l *Local) Move(src, dest string) error {
	if src == "" || l.isRoot(src) {
		return fmt.Errorf("filesystem roots cannot be moved")
	}
	s, e := l.resolve(src, false)
	if e != nil {
		return e
	}
	d, e := l.resolve(dest, false)
	if e != nil {
		return e
	}
	i, e := os.Stat(d)
	if e != nil {
		return e
	}
	if !i.IsDir() {
		return fmt.Errorf("destination is not a directory")
	}
	target, e := l.resolve(join(dest, filepath.Base(s)), true)
	if e != nil {
		return e
	}
	if _, e = os.Lstat(target); !errors.Is(e, os.ErrNotExist) {
		return fmt.Errorf("destination already exists")
	}
	rel, e := filepath.Rel(s, d)
	if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("cannot move a directory into itself")
	}
	return os.Rename(s, target)
}

func (l *Local) Copy(src, dest string) error {
	if src == "" || l.isRoot(src) {
		return fmt.Errorf("filesystem roots cannot be copied")
	}
	source, err := l.resolve(src, false)
	if err != nil {
		return err
	}
	destination, err := l.resolve(dest, false)
	if err != nil {
		return err
	}
	if info, err := os.Stat(destination); err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("destination is not a directory")
	}
	target, err := l.resolve(join(dest, filepath.Base(source)), true)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("destination already exists")
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if rel, err := filepath.Rel(source, destination); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("cannot copy a directory into itself")
		}
	}
	return copyObject(source, target, info)
}

func copyObject(source, target string, info os.FileInfo) error {
	if !info.IsDir() {
		in, err := os.Open(source)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode())
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	if err := os.Mkdir(target, info.Mode()); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		childInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if err := copyObject(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name()), childInfo); err != nil {
			return err
		}
	}
	return nil
}
func (l *Local) Delete(p string) error {
	if p == "" || l.isRoot(p) {
		return fmt.Errorf("filesystem roots cannot be deleted")
	}
	abs, e := l.resolve(p, false)
	if e != nil {
		return e
	}
	return os.RemoveAll(abs)
}

func (l *Local) ReadText(p string) (string, error) {
	abs, err := l.resolve(p, false)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("directories cannot be edited")
	}
	if info.Size() > 1<<20 {
		return "", fmt.Errorf("text files larger than 1 MiB cannot be edited")
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return "", err
	}
	if len(b) > 1<<20 {
		return "", fmt.Errorf("text files larger than 1 MiB cannot be edited")
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return "", fmt.Errorf("binary files cannot be edited")
	}
	return string(b), nil
}
func (l *Local) WriteText(p, content string) error {
	if len(content) > 1<<20 {
		return fmt.Errorf("text files larger than 1 MiB cannot be edited")
	}
	abs, err := l.resolve(p, true)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if errors.Is(err, os.ErrNotExist) {
		f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = io.WriteString(f, content)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symbolic links cannot be edited")
	}
	if info.IsDir() {
		return fmt.Errorf("directories cannot be edited")
	}
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, err = io.WriteString(f, content)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

var _ = time.Now
