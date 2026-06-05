package integration

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// Tests for write_file source streaming (source_host / source_ctid / source_path):
// instead of inline content, the relay pulls bytes over SSH from a source host
// and pipes them into the existing destination write. The file never transits
// the agent context.

func TestWriteFileSourceCtidToCtidDirectSSH(t *testing.T) {
	s, c := initClient(t)

	registerContainer(t, c, 2, 115, "192.168.10.66", "claude-personal", true)
	registerContainer(t, c, 3, 131, "192.168.10.90", "human-relay", true)

	resp := c.Call(t, 4, "tools/call", map[string]interface{}{
		"name": "write_file",
		"arguments": map[string]interface{}{
			"path":        "/opt/human-relay/scripts/data.xlsx",
			"ctid":        float64(131),
			"source_ctid": float64(115),
			"source_path": "/shared/data.xlsx",
			"reason":      "stream xlsx from 115 to relay",
		},
	})
	if isErrorResponse(resp) {
		t.Fatal("unexpected error")
	}

	wfr := extractWriteFileResponse(t, resp)
	if wfr.Status != "pending" {
		t.Errorf("expected status pending, got %s", wfr.Status)
	}
	if !strings.Contains(wfr.Target, "131") {
		t.Errorf("expected target to contain dest CTID, got %s", wfr.Target)
	}
	if !strings.Contains(wfr.Source, "115") || !strings.Contains(wfr.Source, "/shared/data.xlsx") {
		t.Errorf("expected source descriptor with CTID and path, got %s", wfr.Source)
	}

	found := findRequestByID(t, c, 5, wfr.RequestID)

	// Source mode runs as a shell pipeline on the relay:
	//   ssh root@<src> -- cat '<srcpath>' | ssh root@<dst> -- "cat > '<dstpath>' && chmod ..."
	if !found.Shell {
		t.Error("expected shell mode for source pipeline")
	}
	full := found.Command
	if len(found.Args) > 0 {
		full += " " + strings.Join(found.Args, " ")
	}
	if !strings.Contains(full, "root@192.168.10.66") {
		t.Errorf("expected source ssh to 192.168.10.66, got %s", full)
	}
	if !strings.Contains(full, "cat '/shared/data.xlsx'") {
		t.Errorf("expected source cat of quoted path, got %s", full)
	}
	if !strings.Contains(full, " | ") {
		t.Errorf("expected pipeline, got %s", full)
	}
	if !strings.Contains(full, "root@192.168.10.90") {
		t.Errorf("expected dest ssh to 192.168.10.90, got %s", full)
	}
	if !strings.Contains(full, "cat > '/opt/human-relay/scripts/data.xlsx'") {
		t.Errorf("expected dest cat redirect, got %s", full)
	}
	if !strings.Contains(full, "chmod 0644") {
		t.Errorf("expected chmod of default mode, got %s", full)
	}

	// No bytes through the agent: stdin must be empty.
	if found.StdinLen != 0 {
		t.Errorf("expected stdin_len 0, got %d", found.StdinLen)
	}

	// Approval reason names both ends instead of a content preview.
	if !strings.Contains(found.Reason, "[FILE from") {
		t.Errorf("expected reason to contain [FILE from prefix, got %q", found.Reason)
	}
	if !strings.Contains(found.Reason, "/shared/data.xlsx") {
		t.Errorf("expected reason to contain source path, got %q", found.Reason)
	}

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), wfr.RequestID),
		s.token, map[string]string{"reason": "test only"})
}

func TestWriteFileSourceHostToHost(t *testing.T) {
	s, c := initClient(t)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "write_file",
		"arguments": map[string]interface{}{
			"path":        "/tmp/dest.bin",
			"host":        "10.0.0.2",
			"source_host": "10.0.0.1",
			"source_path": "/tmp/src.bin",
			"reason":      "host to host stream",
		},
	})
	if isErrorResponse(resp) {
		t.Fatal("unexpected error")
	}

	wfr := extractWriteFileResponse(t, resp)
	found := findRequestByID(t, c, 3, wfr.RequestID)
	full := found.Command
	if len(found.Args) > 0 {
		full += " " + strings.Join(found.Args, " ")
	}
	if !strings.Contains(full, "root@10.0.0.1") {
		t.Errorf("expected source ssh to 10.0.0.1, got %s", full)
	}
	if !strings.Contains(full, "root@10.0.0.2") {
		t.Errorf("expected dest ssh to 10.0.0.2, got %s", full)
	}

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), wfr.RequestID),
		s.token, map[string]string{"reason": "test only"})
}

func TestWriteFileSourceCtidPctExecFallback(t *testing.T) {
	s, c := initClient(t)

	// Source container without relay SSH: pull via pct exec cat on the Proxmox host.
	registerContainer(t, c, 2, 153, "192.168.10.113", "habit-isekai", false)

	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "write_file",
		"arguments": map[string]interface{}{
			"path":        "/tmp/dest.bin",
			"host":        "10.0.0.2",
			"source_ctid": float64(153),
			"source_path": "/root/data.json",
			"reason":      "pull from container without relay ssh",
		},
	})
	if isErrorResponse(resp) {
		t.Fatal("unexpected error")
	}

	wfr := extractWriteFileResponse(t, resp)
	found := findRequestByID(t, c, 4, wfr.RequestID)
	full := found.Command
	if len(found.Args) > 0 {
		full += " " + strings.Join(found.Args, " ")
	}
	if !strings.Contains(full, "root@192.168.10.50") {
		t.Errorf("expected source pull via Proxmox host, got %s", full)
	}
	if !strings.Contains(full, "pct exec 153") {
		t.Errorf("expected pct exec fallback for source, got %s", full)
	}
	if !strings.Contains(full, "cat '/root/data.json'") {
		t.Errorf("expected source cat of quoted path, got %s", full)
	}

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), wfr.RequestID),
		s.token, map[string]string{"reason": "test only"})
}

func TestWriteFileSourceDefaultsToProxmoxHost(t *testing.T) {
	s, c := initClient(t)

	// source_path with neither source_host nor source_ctid: source defaults to
	// the Proxmox host, mirroring the destination's host default.
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "write_file",
		"arguments": map[string]interface{}{
			"path":        "/tmp/dest.bin",
			"host":        "10.0.0.2",
			"source_path": "/var/lib/vz/template/foo.tar.zst",
			"reason":      "default source host",
		},
	})
	if isErrorResponse(resp) {
		t.Fatal("unexpected error")
	}

	wfr := extractWriteFileResponse(t, resp)
	found := findRequestByID(t, c, 3, wfr.RequestID)
	full := found.Command
	if len(found.Args) > 0 {
		full += " " + strings.Join(found.Args, " ")
	}
	if !strings.Contains(full, "root@192.168.10.50") {
		t.Errorf("expected source ssh to default Proxmox host, got %s", full)
	}

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), wfr.RequestID),
		s.token, map[string]string{"reason": "test only"})
}

func TestWriteFileSourceConflictsWithInlineContent(t *testing.T) {
	_, c := initClient(t)

	tests := []struct {
		name string
		args map[string]interface{}
	}{
		{"content + source_path", map[string]interface{}{
			"path":        "/tmp/dest.txt",
			"content":     "inline",
			"source_path": "/tmp/src.txt",
			"reason":      "conflict",
		}},
		{"content_base64 + source_path", map[string]interface{}{
			"path":           "/tmp/dest.txt",
			"content_base64": base64.StdEncoding.EncodeToString([]byte("inline")),
			"source_path":    "/tmp/src.txt",
			"reason":         "conflict",
		}},
		{"source_host without source_path", map[string]interface{}{
			"path":        "/tmp/dest.txt",
			"source_host": "10.0.0.1",
			"reason":      "source_host alone is not a content method",
		}},
		{"source_host + source_ctid both set", map[string]interface{}{
			"path":        "/tmp/dest.txt",
			"source_host": "10.0.0.1",
			"source_ctid": float64(115),
			"source_path": "/tmp/src.txt",
			"reason":      "ambiguous source",
		}},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := c.Call(t, i+2, "tools/call", map[string]interface{}{
				"name":      "write_file",
				"arguments": tt.args,
			})
			if !isErrorResponse(resp) {
				t.Fatal("expected error")
			}
		})
	}
}

func TestWriteFileSourceInvalidSourcePath(t *testing.T) {
	_, c := initClient(t)

	tests := []struct {
		name string
		path string
	}{
		{"relative", "tmp/src.txt"},
		{"shell chars", "/tmp/src;id.txt"},
		{"spaces", "/tmp/src file.txt"},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := c.Call(t, i+2, "tools/call", map[string]interface{}{
				"name": "write_file",
				"arguments": map[string]interface{}{
					"path":        "/tmp/dest.txt",
					"source_host": "10.0.0.1",
					"source_path": tt.path,
					"reason":      "invalid source path",
				},
			})
			if !isErrorResponse(resp) {
				t.Fatalf("expected error for source_path %q", tt.path)
			}
		})
	}
}

func TestWriteFileSourceUnregisteredCtid(t *testing.T) {
	_, c := initClient(t)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "write_file",
		"arguments": map[string]interface{}{
			"path":        "/tmp/dest.txt",
			"source_ctid": float64(999),
			"source_path": "/tmp/src.txt",
			"reason":      "unregistered source",
		},
	})
	if !isErrorResponse(resp) {
		t.Fatal("expected error for unregistered source container")
	}
}

func TestWriteFileSourcePctPushDest(t *testing.T) {
	s, c := initClient(t)

	registerContainer(t, c, 2, 115, "192.168.10.66", "claude-personal", true)
	registerContainer(t, c, 3, 108, "192.168.10.59", "wikijs", false)

	resp := c.Call(t, 4, "tools/call", map[string]interface{}{
		"name": "write_file",
		"arguments": map[string]interface{}{
			"path":        "/opt/wiki/data.xlsx",
			"ctid":        float64(108),
			"source_ctid": float64(115),
			"source_path": "/shared/data.xlsx",
			"reason":      "stream into pct-push dest",
		},
	})
	if isErrorResponse(resp) {
		t.Fatal("unexpected error")
	}

	wfr := extractWriteFileResponse(t, resp)
	if wfr.Route != "pct_push" {
		t.Errorf("expected route pct_push, got %s", wfr.Route)
	}

	found := findRequestByID(t, c, 5, wfr.RequestID)
	full := found.Command
	if len(found.Args) > 0 {
		full += " " + strings.Join(found.Args, " ")
	}
	if !strings.Contains(full, "root@192.168.10.66") {
		t.Errorf("expected source ssh to 115's IP, got %s", full)
	}
	if !strings.Contains(full, "pct push 108") {
		t.Errorf("expected pct push to dest, got %s", full)
	}

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), wfr.RequestID),
		s.token, map[string]string{"reason": "test only"})
}
