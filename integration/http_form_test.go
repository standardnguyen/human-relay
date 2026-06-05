package integration

import (
	"fmt"
	"strings"
	"testing"
)

// Tests for http_request multipart support (form_file / form_fields): the
// relay fetches the file bytes over SSH at execution time and builds a
// multipart/form-data body — first-class file uploads (e.g. Trello
// attachments) with zero bytes through the agent context.

func TestHTTPRequestFormFileFromCtid(t *testing.T) {
	s, c := initClient(t)

	registerContainer(t, c, 2, 115, "192.168.10.66", "claude-personal", true)

	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "http_request",
		"arguments": map[string]interface{}{
			"method": "POST",
			"url":    "https://api.trello.com/1/cards/abc/attachments?key=${TRELLO_API_KEY}&token=${TRELLO_TOKEN}",
			"form_file": map[string]interface{}{
				"source_ctid": float64(115),
				"source_path": "/shared/data.xlsx",
			},
			"form_fields": map[string]interface{}{
				"name": "data.xlsx",
			},
			"reason": "attach xlsx to card",
		},
	})
	if isErrorResponse(resp) {
		t.Fatal("unexpected error")
	}

	id := extractRequestID(t, resp)
	found := findRequestByID(t, c, 4, id)

	if found.HTTPFormFile == nil {
		t.Fatal("expected http_form_file on request")
	}
	// Field defaults to "file", filename defaults to the source basename.
	if found.HTTPFormFile.Field != "file" {
		t.Errorf("expected default field 'file', got %q", found.HTTPFormFile.Field)
	}
	if found.HTTPFormFile.Filename != "data.xlsx" {
		t.Errorf("expected default filename from basename, got %q", found.HTTPFormFile.Filename)
	}
	// Fetch command pulls over SSH from the registered container's IP.
	fetch := strings.Join(found.HTTPFormFile.FetchCmd, " ")
	if !strings.Contains(fetch, "root@192.168.10.66") {
		t.Errorf("expected fetch over ssh to 192.168.10.66, got %q", fetch)
	}
	if !strings.Contains(fetch, "/shared/data.xlsx") {
		t.Errorf("expected fetch of source path, got %q", fetch)
	}
	if found.HTTPFormFields["name"] != "data.xlsx" {
		t.Errorf("expected form field name=data.xlsx, got %v", found.HTTPFormFields)
	}

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), id),
		s.token, map[string]string{"reason": "test only"})
}

func TestHTTPRequestFormFileExplicitFieldAndFilename(t *testing.T) {
	s, c := initClient(t)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "http_request",
		"arguments": map[string]interface{}{
			"method": "POST",
			"url":    "https://example.com/upload",
			"form_file": map[string]interface{}{
				"source_host": "10.0.0.1",
				"source_path": "/tmp/blob.bin",
				"field":       "attachment",
				"filename":    "renamed.bin",
			},
			"reason": "explicit field and filename",
		},
	})
	if isErrorResponse(resp) {
		t.Fatal("unexpected error")
	}

	id := extractRequestID(t, resp)
	found := findRequestByID(t, c, 3, id)
	if found.HTTPFormFile == nil {
		t.Fatal("expected http_form_file on request")
	}
	if found.HTTPFormFile.Field != "attachment" {
		t.Errorf("expected field 'attachment', got %q", found.HTTPFormFile.Field)
	}
	if found.HTTPFormFile.Filename != "renamed.bin" {
		t.Errorf("expected filename 'renamed.bin', got %q", found.HTTPFormFile.Filename)
	}

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), id),
		s.token, map[string]string{"reason": "test only"})
}

func TestHTTPRequestFormFileValidation(t *testing.T) {
	_, c := initClient(t)

	tests := []struct {
		name string
		args map[string]interface{}
	}{
		{"form_file + body conflict", map[string]interface{}{
			"method": "POST",
			"url":    "https://example.com/upload",
			"body":   `{"a":1}`,
			"form_file": map[string]interface{}{
				"source_host": "10.0.0.1",
				"source_path": "/tmp/blob.bin",
			},
			"reason": "conflict",
		}},
		{"form_file on GET", map[string]interface{}{
			"method": "GET",
			"url":    "https://example.com/upload",
			"form_file": map[string]interface{}{
				"source_host": "10.0.0.1",
				"source_path": "/tmp/blob.bin",
			},
			"reason": "wrong method",
		}},
		{"form_file missing source_path", map[string]interface{}{
			"method": "POST",
			"url":    "https://example.com/upload",
			"form_file": map[string]interface{}{
				"source_host": "10.0.0.1",
			},
			"reason": "no source path",
		}},
		{"form_file invalid source_path", map[string]interface{}{
			"method": "POST",
			"url":    "https://example.com/upload",
			"form_file": map[string]interface{}{
				"source_host": "10.0.0.1",
				"source_path": "/tmp/blob;id.bin",
			},
			"reason": "shell chars",
		}},
		{"form_file unregistered source_ctid", map[string]interface{}{
			"method": "POST",
			"url":    "https://example.com/upload",
			"form_file": map[string]interface{}{
				"source_ctid": float64(999),
				"source_path": "/tmp/blob.bin",
			},
			"reason": "unregistered",
		}},
		{"form_fields without form_file", map[string]interface{}{
			"method": "POST",
			"url":    "https://example.com/upload",
			"form_fields": map[string]interface{}{
				"a": "b",
			},
			"reason": "fields alone",
		}},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := c.Call(t, i+2, "tools/call", map[string]interface{}{
				"name":      "http_request",
				"arguments": tt.args,
			})
			if !isErrorResponse(resp) {
				t.Fatal("expected error")
			}
		})
	}
}
