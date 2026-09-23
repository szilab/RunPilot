package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseCatalog(t *testing.T) {
	checksum := strings.Repeat("a", 64)
	catalog, err := ParseCatalog([]byte(`{"apiVersion":1,"plugins":[{"id":"remote.xpra","name":"Xpra","version":"1.0.0","runpilotApi":1,"asset":"remote-xpra-1.0.0.rpplugin","sha256":"` + checksum + `"}]}`))
	if err != nil || len(catalog.Plugins) != 1 {
		t.Fatalf("ParseCatalog() = %#v, %v", catalog, err)
	}
}

func TestDownloadAndInstallRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("x", MaxPackageSize+1)))
	}))
	defer server.Close()
	_, err := DownloadAndInstall(context.Background(), server.Client(), server.URL, t.TempDir(), CatalogEntry{SHA256: strings.Repeat("a", 64)})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("DownloadAndInstall() error = %v", err)
	}
}
