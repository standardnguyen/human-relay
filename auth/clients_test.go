package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryAddVerifyRevoke(t *testing.T) {
	reg, err := NewRegistry(filepath.Join(t.TempDir(), "clients.json"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	token, err := reg.Add("cc-115")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if token == "" {
		t.Fatal("Add returned an empty token")
	}

	if name, ok := reg.Verify(token); !ok || name != "cc-115" {
		t.Fatalf("Verify(valid) = (%q, %v), want (cc-115, true)", name, ok)
	}
	if _, ok := reg.Verify("not-a-real-token"); ok {
		t.Fatal("Verify(unknown) succeeded")
	}
	if _, ok := reg.Verify(""); ok {
		t.Fatal("Verify(empty) succeeded")
	}

	if err := reg.Revoke("cc-115"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if name, ok := reg.Verify(token); ok {
		t.Fatalf("Verify(revoked) = (%q, true), want unauthorized", name)
	}
}

func TestRegistryRefusesDuplicateName(t *testing.T) {
	reg, err := NewRegistry(filepath.Join(t.TempDir(), "clients.json"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if _, err := reg.Add("kapsrh"); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if _, err := reg.Add("kapsrh"); err == nil {
		t.Fatal("second Add with duplicate name succeeded")
	}
	// Revoking then re-adding is likewise refused: the name stays taken so a
	// stale token can never be resurrected by minting a fresh one.
	if err := reg.Revoke("kapsrh"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := reg.Add("kapsrh"); err == nil {
		t.Fatal("Add after revoke succeeded, want refusal")
	}
}

func TestRegistryStoresOnlyHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clients.json")
	reg, err := NewRegistry(path)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	token, err := reg.Add("cc-115")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if strings.Contains(string(data), token) {
		t.Fatal("registry file contains the raw token")
	}
	if !strings.Contains(string(data), hashToken(token)) {
		t.Fatal("registry file does not contain the token hash")
	}
}

func TestRegistryPersistsAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clients.json")
	reg, err := NewRegistry(path)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	token, err := reg.Add("cc-115")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	reloaded, err := NewRegistry(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if name, ok := reloaded.Verify(token); !ok || name != "cc-115" {
		t.Fatalf("Verify after reload = (%q, %v), want (cc-115, true)", name, ok)
	}
	list := reloaded.List()
	if len(list) != 1 || list[0].Name != "cc-115" {
		t.Fatalf("List after reload = %+v, want one cc-115", list)
	}
}

func TestVerifierMasterAndClients(t *testing.T) {
	reg, err := NewRegistry(filepath.Join(t.TempDir(), "clients.json"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	clientToken, err := reg.Add("cc-115")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	v := NewVerifier(reg, "master-secret")

	if name, ok := v.Verify("master-secret"); !ok || name != masterClientName {
		t.Fatalf("Verify(master) = (%q, %v), want (%s, true)", name, ok, masterClientName)
	}
	if name, ok := v.Verify(clientToken); !ok || name != "cc-115" {
		t.Fatalf("Verify(client) = (%q, %v), want (cc-115, true)", name, ok)
	}
	if _, ok := v.Verify("neither"); ok {
		t.Fatal("Verify(unknown) succeeded")
	}
	if _, ok := v.Verify(""); ok {
		t.Fatal("Verify(empty) succeeded")
	}
}

func TestVerifierRevokedClientRejectedMasterStillWorks(t *testing.T) {
	reg, err := NewRegistry(filepath.Join(t.TempDir(), "clients.json"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	token, err := reg.Add("kapsrh")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	v := NewVerifier(reg, "master-secret")

	if err := reg.Revoke("kapsrh"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok := v.Verify(token); ok {
		t.Fatal("revoked client token was accepted")
	}
	if _, ok := v.Verify("master-secret"); !ok {
		t.Fatal("master token stopped working after a client was revoked")
	}
}

// TestVerifierApproverRole: WithApprover adds one credential that verifies as
// the approver and carries the approver role; no other credential does, and
// the verifier it was derived from (the MCP port's) never learns it.
func TestVerifierApproverRole(t *testing.T) {
	reg, err := NewRegistry(filepath.Join(t.TempDir(), "clients.json"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	clientToken, err := reg.Add("agent")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	agentPort := NewVerifier(reg, "master-token")
	webPort := agentPort.WithApprover("approver-token")

	cases := []struct {
		name         string
		v            *Verifier
		token        string
		wantClient   string
		wantApprover bool
		wantOK       bool
	}{
		{"web/approver", webPort, "approver-token", ApproverClientName, true, true},
		{"web/master", webPort, "master-token", "master", false, true},
		{"web/client", webPort, clientToken, "agent", false, true},
		{"web/unknown", webPort, "nope", "", false, false},
		{"mcp/approver", agentPort, "approver-token", "", false, false},
		{"mcp/master", agentPort, "master-token", "master", false, true},
	}
	for _, tc := range cases {
		client, approver, ok := tc.v.VerifyRole(tc.token)
		if client != tc.wantClient || approver != tc.wantApprover || ok != tc.wantOK {
			t.Errorf("%s: VerifyRole = (%q, %v, %v), want (%q, %v, %v)",
				tc.name, client, approver, ok, tc.wantClient, tc.wantApprover, tc.wantOK)
		}
	}

	// A registry client that happens to be named "approver" is still not one.
	named, err := reg.Add(ApproverClientName)
	if err != nil {
		t.Fatalf("Add(approver-named client): %v", err)
	}
	if client, approver, ok := webPort.VerifyRole(named); !ok || approver || client != ApproverClientName {
		t.Fatalf("approver-named registry client: VerifyRole = (%q, %v, %v), want (approver, false, true)", client, approver, ok)
	}
}

// TestVerifierWithEmptyApproverIsLegacy: an empty approver token adds nothing.
func TestVerifierWithEmptyApproverIsLegacy(t *testing.T) {
	v := NewVerifier(nil, "master-token").WithApprover("")
	if _, approver, ok := v.VerifyRole(""); ok || approver {
		t.Fatal("empty token verified against an empty approver token")
	}
	if _, approver, ok := v.VerifyRole("master-token"); !ok || approver {
		t.Fatalf("master: ok=%v approver=%v, want ok and not approver", ok, approver)
	}
}
