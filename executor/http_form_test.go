package executor

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardnguyen/human-relay/store"
)

// ExecuteHTTP with a FormFile fetches the file bytes by running FetchCmd and
// sends a multipart/form-data body. Tests use a local `cat` as the fetch
// command so no SSH is involved.

func TestExecuteHTTPMultipartFormFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "blob.bin")
	payload := []byte("xlsx-bytes-\x00\x01\x02-end")
	if err := os.WriteFile(src, payload, 0644); err != nil {
		t.Fatal(err)
	}

	var gotField, gotFilename string
	var gotBytes []byte
	var gotName string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("not multipart: %v", err)
		}
		gotName = r.FormValue("name")
		for field, files := range r.MultipartForm.File {
			gotField = field
			gotFilename = files[0].Filename
			f, _ := files[0].Open()
			gotBytes, _ = io.ReadAll(f)
			f.Close()
		}
		w.WriteHeader(200)
		io.WriteString(w, `{"ok":true}`)
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "POST",
		HTTPURL:    ts.URL + "/upload",
		HTTPFormFile: &store.FormFile{
			Field:    "file",
			Filename: "data.xlsx",
			FetchCmd: []string{"cat", src},
			Source:   "test:" + src,
		},
		HTTPFormFields: map[string]string{"name": "data.xlsx"},
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", result.StatusCode)
	}
	if gotField != "file" {
		t.Errorf("expected field 'file', got %q", gotField)
	}
	if gotFilename != "data.xlsx" {
		t.Errorf("expected filename 'data.xlsx', got %q", gotFilename)
	}
	if string(gotBytes) != string(payload) {
		t.Errorf("payload mismatch: expected %q, got %q", payload, gotBytes)
	}
	if gotName != "data.xlsx" {
		t.Errorf("expected form field name=data.xlsx, got %q", gotName)
	}
}

func TestExecuteHTTPMultipartFetchFails(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be reached when fetch fails")
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "POST",
		HTTPURL:    ts.URL + "/upload",
		HTTPFormFile: &store.FormFile{
			Field:    "file",
			Filename: "missing.bin",
			FetchCmd: []string{"cat", "/nonexistent/missing.bin"},
			Source:   "test:/nonexistent/missing.bin",
		},
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode == 0 {
		t.Fatal("expected nonzero exit when fetch fails")
	}
	if !strings.Contains(result.Stderr, "fetch") {
		t.Errorf("expected stderr to mention fetch failure, got %q", result.Stderr)
	}
}
