package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/szilab/RunPilot/internal/plugins"
)

const (
	workspaceDownloadLifetime = 5 * time.Minute
	workspaceUploadLifetime   = 2 * time.Minute
	// Image thumbnails use one download ticket each, so owners may hold many.
	maxWorkspaceTransfersOwner  = 256
	maxWorkspaceTransfersGlobal = 1024
	// WorkspaceTransferPath is the authenticated host route for transfers,
	// relative to the RunPilot base path.
	WorkspaceTransferPath = "api/v1/transfers/"
)

// ErrWorkspaceTransferUnknown reports an unknown, expired or used ticket.
var ErrWorkspaceTransferUnknown = errors.New("transfer ticket is unknown, expired or already used")

// inlineImageTypes may be shown in the browser instead of downloaded. Other
// content, including SVG and HTML, is always an attachment so user files
// never run as RunPilot pages.
var inlineImageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp",
}

// workspaceTransferManager issues single-purpose tickets for streaming file
// transfers through the authenticated HTTP route, so large files never pass
// through plugin memory or the application WebSocket.
type workspaceTransferManager struct {
	mu    sync.Mutex
	items map[string]*workspaceTransfer
	open  func(owner, root string) (*os.Root, string, error)
}

type workspaceTransfer struct {
	owner, kind string
	root, path  string
	name        string
	overwrite   bool
	inline      bool
	maxBytes    int64
	expires     time.Time
	used        bool
}

// WorkspaceDownload is an open file ready to be served.
type WorkspaceDownload struct {
	File        *os.File
	Info        os.FileInfo
	Name        string
	ContentType string
	Inline      bool
}

func newWorkspaceTransferManager(open func(owner, root string) (*os.Root, string, error)) *workspaceTransferManager {
	return &workspaceTransferManager{items: map[string]*workspaceTransfer{}, open: open}
}

func (m *workspaceTransferManager) issue(owner, kind string, raw json.RawMessage) (json.RawMessage, error) {
	if m == nil {
		return nil, &plugins.HostFailure{Code: "unavailable", Message: "workspace transfers are unavailable"}
	}
	transfer := &workspaceTransfer{owner: owner, kind: kind}
	var response map[string]any
	switch kind {
	case "download":
		var in struct {
			Root   string `json:"root"`
			Path   string `json:"path"`
			Inline bool   `json:"inline"`
		}
		if decodeNetworkParams(raw, &in) != nil || !validWorkspacePath(in.Path, false) {
			return nil, workspaceInvalid("download requires a valid file path")
		}
		root, _, err := m.open(owner, in.Root)
		if err != nil {
			return nil, workspaceFileError(err)
		}
		info, err := root.Lstat(in.Path)
		root.Close()
		if err != nil {
			return nil, workspaceFileError(err)
		}
		if !info.Mode().IsRegular() {
			return nil, workspaceInvalid("only regular files can be downloaded")
		}
		_, inlineType := inlineImageTypes[strings.ToLower(path.Ext(in.Path))]
		transfer.root, transfer.path, transfer.name = in.Root, in.Path, path.Base(in.Path)
		transfer.inline = in.Inline && inlineType
		transfer.expires = time.Now().Add(workspaceDownloadLifetime)
		response = map[string]any{"name": transfer.name, "size": info.Size(), "inline": transfer.inline}
	case "upload":
		var in struct {
			Root      string `json:"root"`
			Directory string `json:"directory"`
			Name      string `json:"name"`
			Overwrite bool   `json:"overwrite"`
			MaxBytes  int64  `json:"maxBytes"`
		}
		if decodeNetworkParams(raw, &in) != nil || !validWorkspacePath(in.Directory, true) || !validWorkspacePath(in.Name, false) || strings.Contains(in.Name, "/") || in.MaxBytes < 0 {
			return nil, workspaceInvalid("upload requires a valid directory and file name")
		}
		root, _, err := m.open(owner, in.Root)
		if err != nil {
			return nil, workspaceFileError(err)
		}
		directory := in.Directory
		if directory == "" {
			directory = "."
		}
		info, err := root.Stat(directory)
		root.Close()
		if err != nil {
			return nil, workspaceFileError(err)
		}
		if !info.IsDir() {
			return nil, workspaceInvalid("upload directory is not a directory")
		}
		transfer.root, transfer.path, transfer.name = in.Root, directory, in.Name
		transfer.overwrite, transfer.maxBytes = in.Overwrite, in.MaxBytes
		transfer.expires = time.Now().Add(workspaceUploadLifetime)
		response = map[string]any{"name": transfer.name}
	default:
		return nil, workspaceInvalid("unknown transfer kind")
	}
	var idBytes [24]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return nil, err
	}
	ticket := base64.RawURLEncoding.EncodeToString(idBytes[:])
	m.mu.Lock()
	m.pruneLocked(time.Now())
	owned := 0
	for _, current := range m.items {
		if current.owner == owner {
			owned++
		}
	}
	if owned >= maxWorkspaceTransfersOwner || len(m.items) >= maxWorkspaceTransfersGlobal {
		m.mu.Unlock()
		return nil, &plugins.HostFailure{Code: "resource_limit", Message: "too many pending transfers"}
	}
	m.items[ticket] = transfer
	m.mu.Unlock()
	response["url"] = WorkspaceTransferPath + ticket
	response["expiresAt"] = transfer.expires.UTC().Format(time.RFC3339)
	return json.Marshal(response)
}

func (m *workspaceTransferManager) pruneLocked(now time.Time) {
	for ticket, transfer := range m.items {
		if now.After(transfer.expires) || transfer.used {
			delete(m.items, ticket)
		}
	}
}

// claim returns a live ticket of the given kind. Upload tickets are single
// use; download tickets stay valid until expiry so ranged resumes work.
func (m *workspaceTransferManager) claim(ticket, kind string) (*workspaceTransfer, error) {
	if m == nil {
		return nil, ErrWorkspaceTransferUnknown
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	transfer := m.items[ticket]
	if transfer == nil || transfer.kind != kind || transfer.used || time.Now().After(transfer.expires) {
		return nil, ErrWorkspaceTransferUnknown
	}
	if kind == "upload" {
		transfer.used = true
	}
	copy := *transfer
	return &copy, nil
}

// OpenWorkspaceDownload opens the file behind a download ticket. Errors are
// ErrWorkspaceTransferUnknown or a workspace capability failure.
func (c *Controller) OpenWorkspaceDownload(ticket string) (*WorkspaceDownload, error) {
	download, err := c.openWorkspaceDownload(ticket)
	if err != nil && !errors.Is(err, ErrWorkspaceTransferUnknown) {
		err = workspaceFileError(err)
	}
	return download, err
}

func (c *Controller) openWorkspaceDownload(ticket string) (*WorkspaceDownload, error) {
	transfer, err := c.workspaceTransfers.claim(ticket, "download")
	if err != nil {
		return nil, err
	}
	root, _, err := c.openWorkspaceRoot(transfer.owner, transfer.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(transfer.path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrWorkspaceTransferUnknown
	}
	file, err := root.Open(transfer.path)
	if err != nil {
		return nil, err
	}
	download := &WorkspaceDownload{File: file, Info: info, Name: transfer.name, ContentType: "application/octet-stream"}
	if transfer.inline {
		// Serve an image inline only when its content matches its extension.
		header := make([]byte, 512)
		n, _ := io.ReadFull(file, header)
		expected := inlineImageTypes[strings.ToLower(path.Ext(transfer.name))]
		if http.DetectContentType(header[:n]) == expected {
			download.ContentType, download.Inline = expected, true
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			file.Close()
			return nil, err
		}
	}
	return download, nil
}

// ContentDisposition formats the download header with an RFC 6266 file name.
func (d *WorkspaceDownload) ContentDisposition() string {
	kind := "attachment"
	if d.Inline {
		kind = "inline"
	}
	return mime.FormatMediaType(kind, map[string]string{"filename": d.Name})
}

// AcceptWorkspaceUpload streams body into a temporary file next to the
// target and renames it into place, so readers never see a partial file.
// Errors are ErrWorkspaceTransferUnknown or a workspace capability failure.
func (c *Controller) AcceptWorkspaceUpload(ctx context.Context, ticket string, body io.Reader) (int64, error) {
	size, err := c.acceptWorkspaceUpload(ctx, ticket, body)
	if err != nil && !errors.Is(err, ErrWorkspaceTransferUnknown) {
		err = workspaceFileError(err)
	}
	return size, err
}

func (c *Controller) acceptWorkspaceUpload(ctx context.Context, ticket string, body io.Reader) (int64, error) {
	transfer, err := c.workspaceTransfers.claim(ticket, "upload")
	if err != nil {
		return 0, err
	}
	root, rootPath, err := c.openWorkspaceRoot(transfer.owner, transfer.root)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	var suffix [9]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return 0, err
	}
	temporary := path.Join(transfer.path, ".runpilot-upload-"+base64.RawURLEncoding.EncodeToString(suffix[:]))
	if transfer.path == "." {
		temporary = path.Base(temporary)
	}
	target := path.Join(transfer.path, transfer.name)
	if transfer.path == "." {
		target = transfer.name
	}
	out, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return 0, err
	}
	reader := body
	if transfer.maxBytes > 0 {
		reader = io.LimitReader(body, transfer.maxBytes+1)
	}
	written, copyErr := io.Copy(out, contextReader{ctx, reader})
	closeErr := out.Close()
	switch {
	case copyErr != nil:
		err = copyErr
	case closeErr != nil:
		err = closeErr
	case transfer.maxBytes > 0 && written > transfer.maxBytes:
		err = &plugins.HostFailure{Code: "too_large", Message: "upload exceeds its size limit"}
	default:
		err = renameInRoot(root, rootPath, temporary, root, rootPath, target, transfer.overwrite)
	}
	if err != nil {
		_ = root.Remove(temporary)
		return 0, err
	}
	return written, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func (m *workspaceTransferManager) stopOwner(owner string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	for ticket, transfer := range m.items {
		if transfer.owner == owner {
			delete(m.items, ticket)
		}
	}
	m.mu.Unlock()
}

// IssueWorkspaceTransfer issues a transfer ticket on behalf of owner, exactly
// like the workspace.transfer.* capability.
func (c *Controller) IssueWorkspaceTransfer(owner, kind string, params json.RawMessage) (json.RawMessage, error) {
	return c.workspaceTransfers.issue(owner, kind, params)
}
