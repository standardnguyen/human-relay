package containers_e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testToken = "e2e-test-token"

// testEnv holds the shared state for the e2e container test suite.
type testEnv struct {
	composeDir string
	keyFile    string
	nodes      [3]nodeInfo
}

type nodeInfo struct {
	name string
	ip   string
}

var env *testEnv

func TestMain(m *testing.M) {
	if os.Getenv("HUMAN_RELAY_BIN") == "" {
		fmt.Fprintln(os.Stderr, "HUMAN_RELAY_BIN not set, skipping container e2e tests")
		os.Exit(0)
	}

	var err error
	env, err = setup()
	if err != nil {
		fmt.Fprintf(os.Stderr, "setup failed: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	teardown(env)
	os.Exit(code)
}

func setup() (*testEnv, error) {
	e := &testEnv{}
	e.composeDir = filepath.Join(mustGetwd(), ".")

	// Generate ephemeral SSH key pair
	keyDir, err := os.MkdirTemp("", "hr-e2e-ssh-*")
	if err != nil {
		return nil, fmt.Errorf("create key dir: %w", err)
	}
	e.keyFile = filepath.Join(keyDir, "id_ed25519")
	if out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-f", e.keyFile, "-N", "", "-q").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ssh-keygen: %s: %w", out, err)
	}

	// Write public key as authorized_keys for the containers
	pubKey, err := os.ReadFile(e.keyFile + ".pub")
	if err != nil {
		return nil, fmt.Errorf("read pub key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(e.composeDir, "authorized_keys"), pubKey, 0644); err != nil {
		return nil, fmt.Errorf("write authorized_keys: %w", err)
	}

	// Build and start containers
	cmd := exec.Command("docker-compose", "up", "-d", "--build")
	cmd.Dir = e.composeDir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker-compose up: %w", err)
	}

	// Get container IPs
	for i, name := range []string{"node1", "node2", "node3"} {
		ip, err := getContainerIP(e.composeDir, name)
		if err != nil {
			return nil, fmt.Errorf("get IP for %s: %w", name, err)
		}
		e.nodes[i] = nodeInfo{name: name, ip: ip}
	}

	// Wait for sshd to be ready on all nodes
	for _, n := range e.nodes {
		if err := waitForSSH(e.keyFile, n.ip, 15*time.Second); err != nil {
			return nil, fmt.Errorf("ssh not ready on %s (%s): %w", n.name, n.ip, err)
		}
	}

	return e, nil
}

func teardown(e *testEnv) {
	cmd := exec.Command("docker-compose", "down", "-v", "--remove-orphans")
	cmd.Dir = e.composeDir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Run()

	os.Remove(filepath.Join(e.composeDir, "authorized_keys"))
	os.RemoveAll(filepath.Dir(e.keyFile))
}

func getContainerIP(composeDir, service string) (string, error) {
	cmd := exec.Command("docker-compose", "exec", "-T", service, "hostname", "-i")
	cmd.Dir = composeDir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func waitForSSH(keyFile, ip string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command("ssh",
			"-i", keyFile,
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "ConnectTimeout=2",
			"-o", "LogLevel=ERROR",
			fmt.Sprintf("root@%s", ip),
			"echo", "ready",
		)
		if out, err := cmd.Output(); err == nil && strings.TrimSpace(string(out)) == "ready" {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout after %s", timeout)
}

func mustGetwd() string {
	d, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return d
}

// --- Test server helpers (adapted from integration/) ---

type testServer struct {
	cmd     *exec.Cmd
	mcpPort int
	webPort int
	token   string
	dataDir string
}

func startServer(t *testing.T, extraEnv ...string) *testServer {
	t.Helper()
	bin := os.Getenv("HUMAN_RELAY_BIN")

	mcpPort := 28080 + os.Getpid()%1000
	webPort := 29090 + os.Getpid()%1000
	dataDir := t.TempDir()

	s := &testServer{
		mcpPort: mcpPort,
		webPort: webPort,
		token:   testToken,
		dataDir: dataDir,
	}

	// Write an SSH config that uses our ephemeral key
	sshConfig := fmt.Sprintf(`Host *
  IdentityFile %s
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
`, env.keyFile)
	sshConfigPath := filepath.Join(dataDir, "ssh_config")
	os.WriteFile(sshConfigPath, []byte(sshConfig), 0644)

	// Set up HOME/.ssh so the relay's ssh commands use our ephemeral key
	sshDir := filepath.Join(dataDir, ".ssh")
	os.MkdirAll(sshDir, 0700)
	os.WriteFile(filepath.Join(sshDir, "config"), []byte(sshConfig), 0600)
	// Copy the key (symlinks can have permission issues with ssh)
	keyData, _ := os.ReadFile(env.keyFile)
	os.WriteFile(filepath.Join(sshDir, "id_ed25519"), keyData, 0600)
	pubData, _ := os.ReadFile(env.keyFile + ".pub")
	os.WriteFile(filepath.Join(sshDir, "id_ed25519.pub"), pubData, 0644)

	s.cmd = exec.Command(bin)
	s.cmd.Env = append(os.Environ(),
		fmt.Sprintf("MHR_MCP_PORT=%d", mcpPort),
		fmt.Sprintf("MHR_WEB_PORT=%d", webPort),
		fmt.Sprintf("MHR_AUTH_TOKEN=%s", testToken),
		fmt.Sprintf("MHR_DATA_DIR=%s", dataDir),
		"MHR_HOST_IP=192.168.10.50",
		"MHR_DEFAULT_TIMEOUT=10",
		"MHR_MAX_TIMEOUT=30",
		"MHR_APPROVAL_COOLDOWN=0",
		fmt.Sprintf("HOME=%s", dataDir),
	)
	s.cmd.Env = append(s.cmd.Env, extraEnv...)

	var stderr bytes.Buffer
	s.cmd.Stderr = &stderr

	if err := s.cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}

	t.Cleanup(func() {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", webPort))
		if err == nil {
			resp.Body.Close()
			return s
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server didn't start within 5s, stderr: %s", stderr.String())
	return nil
}

func (s *testServer) mcpURL() string  { return fmt.Sprintf("http://127.0.0.1:%d", s.mcpPort) }
func (s *testServer) webURL() string  { return fmt.Sprintf("http://127.0.0.1:%d", s.webPort) }

// --- MCP client (minimal, same as integration/) ---

type mcpClient struct {
	mcpURL    string
	sessionID string
	eventCh   chan sseEvent
	client    *http.Client
}

type sseEvent struct {
	Event string
	Data  string
}

func newMCPClient(t *testing.T, mcpURL string) *mcpClient {
	t.Helper()
	c := &mcpClient{
		mcpURL:  mcpURL,
		eventCh: make(chan sseEvent, 100),
		client:  &http.Client{Timeout: 30 * time.Second},
	}

	resp, err := http.Get(mcpURL + "/sse")
	if err != nil {
		t.Fatalf("SSE connect: %v", err)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 2*1024*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			c.sessionID = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	if c.sessionID == "" {
		resp.Body.Close()
		t.Fatal("no endpoint event")
	}

	go func() {
		defer resp.Body.Close()
		var currentEvent string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				currentEvent = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				c.eventCh <- sseEvent{Event: currentEvent, Data: data}
			}
		}
	}()

	t.Cleanup(func() { resp.Body.Close() })
	return c
}

type jsonrpcReq struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type jsonrpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *mcpClient) call(t *testing.T, id int, method string, params interface{}) *jsonrpcResp {
	t.Helper()
	body, _ := json.Marshal(jsonrpcReq{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	resp, err := c.client.Post(c.mcpURL+c.sessionID, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("MCP call: %v", err)
	}
	resp.Body.Close()

	select {
	case ev := <-c.eventCh:
		var r jsonrpcResp
		json.Unmarshal([]byte(ev.Data), &r)
		return &r
	case <-time.After(15 * time.Second):
		t.Fatal("MCP response timeout")
		return nil
	}
}

func (c *mcpClient) notify(t *testing.T, method string, params interface{}) {
	t.Helper()
	body, _ := json.Marshal(jsonrpcReq{JSONRPC: "2.0", Method: method, Params: params})
	resp, err := c.client.Post(c.mcpURL+c.sessionID, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("MCP notify: %v", err)
	}
	resp.Body.Close()
}

func (c *mcpClient) init(t *testing.T) {
	t.Helper()
	c.call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "e2e", "version": "1.0"},
	})
	c.notify(t, "notifications/initialized", nil)
}

// --- Web helpers ---

func webPost(t *testing.T, url, token string, payload interface{}) (int, []byte) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, _ := json.Marshal(payload)
		body = bytes.NewReader(data)
	}
	req, _ := http.NewRequest("POST", url, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("web POST: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// --- Result parsing helpers ---

type requestResult struct {
	ID      string      `json:"id"`
	Command string      `json:"command"`
	Args    []string    `json:"args"`
	Status  string      `json:"status"`
	Result  *execResult `json:"result"`
}

type execResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

func extractRequestID(t *testing.T, resp *jsonrpcResp) string {
	t.Helper()
	var result struct {
		Content []struct{ Text string `json:"text"` } `json:"content"`
	}
	json.Unmarshal(resp.Result, &result)
	if len(result.Content) == 0 {
		t.Fatal("no content")
	}
	var parsed map[string]interface{}
	json.Unmarshal([]byte(result.Content[0].Text), &parsed)
	id, _ := parsed["request_id"].(string)
	if id == "" {
		t.Fatalf("empty request_id in: %s", result.Content[0].Text)
	}
	return id
}

func extractToolResult(t *testing.T, resp *jsonrpcResp) *requestResult {
	t.Helper()
	var result struct {
		Content []struct{ Text string `json:"text"` } `json:"content"`
	}
	json.Unmarshal(resp.Result, &result)
	if len(result.Content) == 0 {
		t.Fatal("no content")
	}
	var parsed requestResult
	json.Unmarshal([]byte(result.Content[0].Text), &parsed)
	return &parsed
}

func approveAndWait(t *testing.T, c *mcpClient, s *testServer, requestID string, callID int) *requestResult {
	t.Helper()
	code, _ := webPost(t, fmt.Sprintf("%s/api/requests/%s/approve", s.webURL(), requestID), s.token, nil)
	if code != 200 {
		t.Fatalf("approve returned %d", code)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp := c.call(t, callID, "tools/call", map[string]interface{}{
			"name":      "get_result",
			"arguments": map[string]interface{}{"request_id": requestID},
		})
		r := extractToolResult(t, resp)
		if r.Status == "complete" || r.Status == "error" {
			return r
		}
		time.Sleep(300 * time.Millisecond)
		callID++
	}
	t.Fatal("command did not complete within 10s")
	return nil
}

// registerNode registers a test container with the relay.
func registerNode(t *testing.T, c *mcpClient, ctid int, ip, hostname string, callID int) {
	t.Helper()
	resp := c.call(t, callID, "tools/call", map[string]interface{}{
		"name": "register_container",
		"arguments": map[string]interface{}{
			"ctid":          ctid,
			"ip":            ip,
			"hostname":      hostname,
			"has_relay_ssh": true,
		},
	})
	if resp.Error != nil {
		t.Fatalf("register_container error: %s", resp.Error.Message)
	}
}
