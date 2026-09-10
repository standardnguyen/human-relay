package integration

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for write_file source streaming (source_host / source_ctid / source_path):
// instead of inline content, the relay pulls bytes over SSH from a source host
// and pipes them into the existing destination write. The file never transits
// the agent context.

func TestWriteFileSourceCtidToCtidDirectSSH(t *testing.T) {
	s, c := initClient(t)

	registerContainer(t, s, c, 2, 115, "192.168.10.66", "claude-personal", true)
	registerContainer(t, s, c, 3, 131, "192.168.10.90", "human-relay", true)

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
	registerContainer(t, s, c, 2, 153, "192.168.10.113", "habit-isekai", false)

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

	registerContainer(t, s, c, 2, 115, "192.168.10.66", "claude-personal", true)
	registerContainer(t, s, c, 3, 108, "192.168.10.59", "wikijs", false)

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

// --- finding #6: ssh_user shell injection into the source-streaming pipeline ---
//
// write_file's source-streaming mode builds `srcCmd | dstCmd` as a shell string
// and stores it with shell=true, which executor.Execute hands to `sh -c`. The
// registry's ssh_user is only screened for a leading '-' (sshUserInjectable,
// which guards argv sinks like exec_container) — nothing there rejects shell
// metacharacters. Unquoted, an ssh_user of "foo; touch /tmp/pwned" would append
// a second command to the pipeline the relay itself runs.

// pipelineOf reassembles the shell string the executor would run for a request.
func pipelineOf(r *RequestResult) string {
	full := r.Command
	if len(r.Args) > 0 {
		full += " " + strings.Join(r.Args, " ")
	}
	return full
}

// runPipelineSandboxed executes a constructed pipeline exactly the way
// executor.Execute does (`sh -c <full>`), with a stub `ssh` on PATH so nothing
// leaves the machine. Any injected command would still run — which is the point.
// Proven against an unquoted control pipeline: both payload shapes used below
// do create their marker when the ssh_user is interpolated raw, so a clean Stat
// here is evidence of the quoting and not of a blind probe.
func runPipelineSandboxed(t *testing.T, full string) {
	t.Helper()
	binDir := t.TempDir()
	stub := filepath.Join(binDir, "ssh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat >/dev/null 2>&1\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write ssh stub: %v", err)
	}
	cmd := exec.Command("sh", "-c", full)
	cmd.Env = append(os.Environ(), "PATH="+binDir+":/usr/bin:/bin")
	// Exit status is irrelevant: the stub makes the ssh legs no-ops. Only the
	// side effects on disk matter.
	_ = cmd.Run()
}

func TestWriteFileSourceSSHUserShellInjectionSourceSide(t *testing.T) {
	s, c := initClient(t)

	marker := filepath.Join(t.TempDir(), "mhr-pwned-marker")
	// No leading '-', so registration's sshUserInjectable check lets it through.
	// The trailing ';' is load-bearing: unquoted, the pipeline reads
	// "ssh foo; touch <marker>@<ip> -- ...", so without it the injected touch
	// creates "<marker>@192.168.10.104" and a Stat on <marker> reads clean in
	// BOTH the vulnerable and fixed cases — i.e. no test at all. Verified
	// against an unquoted control before trusting this assertion.
	payload := "foo; touch " + marker + " ;"

	registerContainerArgs(t, s, c, 2, map[string]interface{}{
		"ctid":          float64(9999),
		"ip":            "192.168.10.104",
		"hostname":      "corsair",
		"has_relay_ssh": true,
		"ssh_user":      payload,
	})

	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "write_file",
		"arguments": map[string]interface{}{
			"path":        "/tmp/dest.bin",
			"host":        "10.0.0.2",
			"source_ctid": float64(9999),
			"source_path": "/tmp/src.bin",
			"reason":      "source-side ssh_user injection",
		},
	})
	if isErrorResponse(resp) {
		t.Fatal("unexpected error")
	}

	wfr := extractWriteFileResponse(t, resp)
	found := findRequestByID(t, c, 4, wfr.RequestID)
	full := pipelineOf(found)

	// The whole user@ip token must be one single-quoted shell word.
	want := "'" + payload + "@192.168.10.104'"
	if !strings.Contains(full, want) {
		t.Errorf("expected ssh target quoted as %q, got pipeline %q", want, full)
	}

	runPipelineSandboxed(t, full)
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("SHELL INJECTION: ssh_user payload executed, %s was created", marker)
	}

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), wfr.RequestID),
		s.token, map[string]string{"reason": "test only"})
}

func TestWriteFileSourceSSHUserShellInjectionDestSide(t *testing.T) {
	s, c := initClient(t)

	marker := filepath.Join(t.TempDir(), "mhr-pwned-marker")
	// Command-substitution form rather than ';' — same sink, different metachar.
	// $(...) closes itself, so the "@<ip>" the pipeline appends lands outside
	// the injected command and the marker path stays exact (see the source-side
	// test's note on why that matters).
	payload := "foo$(touch " + marker + ")"

	registerContainerArgs(t, s, c, 2, map[string]interface{}{
		"ctid":          float64(9998),
		"ip":            "192.168.10.105",
		"hostname":      "victim",
		"has_relay_ssh": true,
		"ssh_user":      payload,
	})

	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "write_file",
		"arguments": map[string]interface{}{
			"path":        "/tmp/dest.bin",
			"ctid":        float64(9998),
			"source_host": "10.0.0.1",
			"source_path": "/tmp/src.bin",
			"reason":      "dest-side ssh_user injection",
		},
	})
	if isErrorResponse(resp) {
		t.Fatal("unexpected error")
	}

	wfr := extractWriteFileResponse(t, resp)
	found := findRequestByID(t, c, 4, wfr.RequestID)
	full := pipelineOf(found)

	want := "'" + payload + "@192.168.10.105'"
	if !strings.Contains(full, want) {
		t.Errorf("expected ssh target quoted as %q, got pipeline %q", want, full)
	}

	runPipelineSandboxed(t, full)
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("SHELL INJECTION: ssh_user payload executed, %s was created", marker)
	}

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), wfr.RequestID),
		s.token, map[string]string{"reason": "test only"})
}

// The reviewer must be able to see the pipeline they are approving: display_command
// REPLACES the raw command in the dashboard, so a summary-only rendering hid a
// hostile ssh_user entirely.
func TestWriteFileSourceDisplayCommandShowsPipeline(t *testing.T) {
	s, c := initClient(t)

	payload := "foo; touch /tmp/mhr-pwned-marker"
	registerContainerArgs(t, s, c, 2, map[string]interface{}{
		"ctid":          float64(9999),
		"ip":            "192.168.10.104",
		"hostname":      "corsair",
		"has_relay_ssh": true,
		"ssh_user":      payload,
	})

	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "write_file",
		"arguments": map[string]interface{}{
			"path":        "/tmp/dest.bin",
			"host":        "10.0.0.2",
			"source_ctid": float64(9999),
			"source_path": "/tmp/src.bin",
			"reason":      "display command must expose the pipeline",
		},
	})
	if isErrorResponse(resp) {
		t.Fatal("unexpected error")
	}

	wfr := extractWriteFileResponse(t, resp)
	found := findRequestByID(t, c, 4, wfr.RequestID)

	// Friendly summary still present...
	if !strings.Contains(found.DisplayCommand, "stream") ||
		!strings.Contains(found.DisplayCommand, "/tmp/src.bin") {
		t.Errorf("expected display_command to keep the stream summary, got %q", found.DisplayCommand)
	}
	// ...and the real pipeline alongside it, payload included.
	if !strings.Contains(found.DisplayCommand, " | ") {
		t.Errorf("expected display_command to show the pipeline, got %q", found.DisplayCommand)
	}
	if !strings.Contains(found.DisplayCommand, payload) {
		t.Errorf("expected display_command to expose the hostile ssh_user, got %q", found.DisplayCommand)
	}
	if !strings.Contains(found.DisplayCommand, pipelineOf(found)) {
		t.Errorf("expected display_command to contain the exact command to be run\n  display: %q\n  command: %q",
			found.DisplayCommand, pipelineOf(found))
	}

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), wfr.RequestID),
		s.token, map[string]string{"reason": "test only"})
}
