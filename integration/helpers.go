package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const testToken = "test-token-for-ci"

type TestServer struct {
	cmd     *exec.Cmd
	mcpPort int
	webPort int
	token   string
	env     []string
}

type ServerOption func(*TestServer)

func WithAllowedDirs(dirs string) ServerOption {
	return func(s *TestServer) {
		s.env = append(s.env, "MHR_ALLOWED_DIRS="+dirs)
	}
}

func WithDataDir(dir string) ServerOption {
	return func(s *TestServer) {
		s.env = append(s.env, "MHR_DATA_DIR="+dir)
	}
}

func WithPorts(mcp, web int) ServerOption {
	return func(s *TestServer) {
		s.mcpPort = mcp
		s.webPort = web
	}
}

func WithCooldown(seconds int) ServerOption {
	return func(s *TestServer) {
		s.env = append(s.env, fmt.Sprintf("MHR_APPROVAL_COOLDOWN=%d", seconds))
	}
}

func StartServer(t *testing.T, opts ...ServerOption) *TestServer {
	t.Helper()
	bin := os.Getenv("HUMAN_RELAY_BIN")
	if bin == "" {
		t.Fatal("HUMAN_RELAY_BIN not set")
	}

	// Find free ports
	mcpPort := 18080 + os.Getpid()%1000
	webPort := 19090 + os.Getpid()%1000

	s := &TestServer{
		mcpPort: mcpPort,
		webPort: webPort,
		token:   testToken,
	}
	for _, opt := range opts {
		opt(s)
	}

	s.cmd = exec.Command(bin)
	s.cmd.Env = append(os.Environ(),
		fmt.Sprintf("MHR_MCP_PORT=%d", s.mcpPort),
		fmt.Sprintf("MHR_WEB_PORT=%d", s.webPort),
		fmt.Sprintf("MHR_AUTH_TOKEN=%s", testToken),
		fmt.Sprintf("MHR_DATA_DIR=%s", t.TempDir()),
		"MHR_HOST_IP=192.168.10.50",
		"MHR_DEFAULT_TIMEOUT=5",
		"MHR_MAX_TIMEOUT=10",
		"MHR_APPROVAL_COOLDOWN=0",
	)
	s.cmd.Env = append(s.cmd.Env, s.env...)

	var stderr bytes.Buffer
	s.cmd.Stderr = &stderr

	if err := s.cmd.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}

	t.Cleanup(func() {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	})

	// Wait for server to be ready
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", s.webPort))
		if err == nil {
			resp.Body.Close()
			return s
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server didn't start within 5s, stderr: %s", stderr.String())
	return nil
}

func (s *TestServer) MCPURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.mcpPort)
}

func (s *TestServer) WebURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.webPort)
}

// MCP SSE client

type MCPClient struct {
	mcpURL    string
	sessionID string
	eventCh   chan SSEEvent
	client    *http.Client
	cancel    func()
}

type SSEEvent struct {
	Event string
	Data  string
}

func NewMCPClient(t *testing.T, mcpURL string) *MCPClient {
	t.Helper()
	c := &MCPClient{
		mcpURL:  mcpURL,
		eventCh: make(chan SSEEvent, 100),
		client:  &http.Client{Timeout: 30 * time.Second},
	}

	// Connect to SSE endpoint
	resp, err := http.Get(mcpURL + "/sse")
	if err != nil {
		t.Fatalf("failed to connect to SSE: %v", err)
	}

	// Read the endpoint event to get session ID
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 2*1024*1024), 2*1024*1024) // 2MB buffer for large responses
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			endpoint := strings.TrimPrefix(line, "data: ")
			c.sessionID = endpoint
			break
		}
	}
	if c.sessionID == "" {
		resp.Body.Close()
		t.Fatal("did not receive endpoint event")
	}

	// Start reading events in background
	go func() {
		defer resp.Body.Close()
		var currentEvent string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				currentEvent = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				c.eventCh <- SSEEvent{Event: currentEvent, Data: data}
			}
		}
	}()

	t.Cleanup(func() {
		resp.Body.Close()
	})

	return c
}

type JSONRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *MCPClient) Call(t *testing.T, id int, method string, params interface{}) *JSONRPCResponse {
	t.Helper()
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	body, _ := json.Marshal(req)

	url := c.mcpURL + c.sessionID
	resp, err := c.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("MCP call failed: %v", err)
	}
	resp.Body.Close()

	// Read response from SSE
	select {
	case ev := <-c.eventCh:
		var rpcResp JSONRPCResponse
		if err := json.Unmarshal([]byte(ev.Data), &rpcResp); err != nil {
			t.Fatalf("failed to parse response: %v\nraw: %s", err, ev.Data)
		}
		return &rpcResp
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for MCP response")
		return nil
	}
}

func (c *MCPClient) Notify(t *testing.T, method string, params interface{}) {
	t.Helper()
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	body, _ := json.Marshal(req)

	url := c.mcpURL + c.sessionID
	resp, err := c.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("MCP notify failed: %v", err)
	}
	resp.Body.Close()
}

// Web API helpers

func WebGet(t *testing.T, url, token string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("web GET failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func WebGetResp(t *testing.T, url, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("web GET failed: %v", err)
	}
	return resp
}

func WebPost(t *testing.T, url, token string, payload interface{}) (int, []byte) {
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
		t.Fatalf("web POST failed: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody
}
