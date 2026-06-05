package integration

import (
	"testing"
)

// Mistyped args must hard-error, not silently no-op. The motivating failure
// (2026-06-05): a cc session with a stale cached MCP schema sent form_file as
// a JSON string and source_ctid as a string — both were silently ignored,
// producing a bodyless Trello POST (400) and a source that defaulted to the
// Proxmox host. Wrong-type args are always caller bugs; fail loudly.

func TestHTTPRequestFormFileWrongType(t *testing.T) {
	_, c := initClient(t)

	tests := []struct {
		name string
		args map[string]interface{}
	}{
		{"form_file as string", map[string]interface{}{
			"method":    "POST",
			"url":       "https://example.com/upload",
			"form_file": `{"source_ctid": 115, "source_path": "/tmp/x"}`,
			"reason":    "stale schema sends objects as strings",
		}},
		{"form_fields as string", map[string]interface{}{
			"method": "POST",
			"url":    "https://example.com/upload",
			"form_file": map[string]interface{}{
				"source_host": "10.0.0.1",
				"source_path": "/tmp/x",
			},
			"form_fields": `{"name": "x"}`,
			"reason":      "stale schema sends objects as strings",
		}},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := c.Call(t, i+2, "tools/call", map[string]interface{}{
				"name":      "http_request",
				"arguments": tt.args,
			})
			if !isErrorResponse(resp) {
				t.Fatal("expected hard error for mistyped arg")
			}
		})
	}
}

func TestWriteFileSourceCtidWrongType(t *testing.T) {
	_, c := initClient(t)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "write_file",
		"arguments": map[string]interface{}{
			"path":        "/tmp/dest.bin",
			"source_ctid": "115", // string, not number
			"source_path": "/tmp/src.bin",
			"reason":      "stale schema sends ints as strings",
		},
	})
	if !isErrorResponse(resp) {
		t.Fatal("expected hard error for string source_ctid")
	}
}

func TestWriteFileCtidWrongType(t *testing.T) {
	_, c := initClient(t)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "write_file",
		"arguments": map[string]interface{}{
			"path":    "/tmp/dest.bin",
			"content": "x",
			"ctid":    "131", // string, not number
			"reason":  "stale schema sends ints as strings",
		},
	})
	if !isErrorResponse(resp) {
		t.Fatal("expected hard error for string ctid")
	}
}
