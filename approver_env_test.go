package main

import (
	"strings"
	"testing"
)

func TestReadApproverEnv(t *testing.T) {
	cases := []struct {
		name       string
		env        []string
		wantToken  string
		wantDigest string
		wantErr    string
	}{
		{"absent", []string{"PATH=/bin"}, "", "", ""},
		{"both-empty", []string{"MHR_APPROVER_TOKEN=", "MHR_APPROVER_TOKEN_SHA256="}, "", "", ""},
		{"token-with-empty-digest", []string{"MHR_APPROVER_TOKEN=tok", "MHR_APPROVER_TOKEN_SHA256="}, "tok", "", ""},
		{"digest-with-empty-token", []string{"MHR_APPROVER_TOKEN=", "MHR_APPROVER_TOKEN_SHA256=ab"}, "", "ab", ""},
		{"prefix-lookalike-ignored", []string{"MHR_APPROVER_TOKEN_SHA256X=zz", "MHR_APPROVER_TOKENS=yy"}, "", "", ""},
		{"digest-twice-empty-first", []string{"MHR_APPROVER_TOKEN_SHA256=", "MHR_APPROVER_TOKEN_SHA256=ab"}, "", "", "MHR_APPROVER_TOKEN_SHA256 appears more than once"},
		{"token-twice", []string{"MHR_APPROVER_TOKEN=", "MHR_APPROVER_TOKEN=tok"}, "", "", "MHR_APPROVER_TOKEN appears more than once"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readApproverEnv(tc.env)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("readApproverEnv(%q) error = %v, want one containing %q", tc.env, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("readApproverEnv(%q): unexpected error %v", tc.env, err)
			}
			if got.token != tc.wantToken || got.digestHex != tc.wantDigest {
				t.Fatalf("readApproverEnv(%q) = token %q digest %q, want %q %q", tc.env, got.token, got.digestHex, tc.wantToken, tc.wantDigest)
			}
		})
	}
}

// TestScrubbedApproverEnv: the re-exec environment keeps no approver entry but
// the one digest it appends, so no empty entry can come first and win.
func TestScrubbedApproverEnv(t *testing.T) {
	in := []string{"A=1", "MHR_APPROVER_TOKEN_SHA256=", "MHR_APPROVER_TOKEN=tok", "B=2", "MHR_APPROVER_TOKEN_SHA256="}
	out := scrubbedApproverEnv(in, "ab")
	var approver []string
	for _, kv := range out {
		if strings.HasPrefix(kv, "MHR_APPROVER_TOKEN") {
			approver = append(approver, kv)
		}
	}
	if len(approver) != 1 || approver[0] != "MHR_APPROVER_TOKEN_SHA256=ab" {
		t.Fatalf("approver entries after scrub = %q, want exactly [MHR_APPROVER_TOKEN_SHA256=ab]", approver)
	}
	if e, err := readApproverEnv(out); err != nil || e.digestHex != "ab" || e.token != "" {
		t.Fatalf("scrubbed env reads back as %+v (err %v), want digest ab only", e, err)
	}
	if strings.Join(out[:2], ",") != "A=1,B=2" {
		t.Fatalf("scrub dropped or reordered unrelated entries: %q", out)
	}
}

func TestApproverValuePresent(t *testing.T) {
	if approverValuePresent([]string{"MHR_APPROVER_TOKEN=", "MHR_APPROVER_TOKEN_SHA256="}) {
		t.Fatal("empty approver entries reported as a configured approver")
	}
	if !approverValuePresent([]string{"MHR_APPROVER_TOKEN_SHA256=", "MHR_APPROVER_TOKEN_SHA256=ab"}) {
		t.Fatal("a non-empty digest behind an empty one was not seen")
	}
}
