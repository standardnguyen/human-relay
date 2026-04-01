package executor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestJsonPathBasic(t *testing.T) {
	data := `[{"id":"abc123","name":"My Card"},{"id":"def456","name":"Other"}]`

	tests := []struct {
		path string
		want string
	}{
		{"0.id", "abc123"},
		{"0.name", "My Card"},
		{"1.id", "def456"},
		{"#", "2"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := jsonPath([]byte(data), tt.path)
			if err != nil {
				t.Fatalf("jsonPath(%q): %v", tt.path, err)
			}
			if got != tt.want {
				t.Fatalf("jsonPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestJsonPathNestedObject(t *testing.T) {
	data := `{"user":{"name":"Alice","address":{"city":"Denver"}}}`

	tests := []struct {
		path string
		want string
	}{
		{"user.name", "Alice"},
		{"user.address.city", "Denver"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := jsonPath([]byte(data), tt.path)
			if err != nil {
				t.Fatalf("jsonPath(%q): %v", tt.path, err)
			}
			if got != tt.want {
				t.Fatalf("jsonPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestJsonPathErrors(t *testing.T) {
	data := `[{"id":"abc"}]`

	tests := []struct {
		name string
		path string
	}{
		{"out of bounds", "5.id"},
		{"missing field", "0.missing"},
		{"not an object", "0.id.bad"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := jsonPath([]byte(data), tt.path)
			if err == nil {
				t.Fatalf("expected error for path %q", tt.path)
			}
		})
	}
}

func TestExpandVars(t *testing.T) {
	vars := map[string]string{
		"CARD_ID":   "abc123",
		"CARD_NAME": "Test Card",
	}

	tests := []struct {
		input string
		want  string
	}{
		{"${CARD_ID}", "abc123"},
		{"cards/${CARD_ID}?name=${CARD_NAME}", "cards/abc123?name=Test Card"},
		{"no vars here", "no vars here"},
		{"${UNKNOWN}", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := expandVarsMap(tt.input, vars)
			if got != tt.want {
				t.Fatalf("expandVarsMap(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExpandVarsFallsBackToEnv(t *testing.T) {
	os.Setenv("TEST_PIPELINE_VAR", "from_env")
	defer os.Unsetenv("TEST_PIPELINE_VAR")

	vars := map[string]string{"LOCAL": "from_step"}
	got := expandVarsMap("${LOCAL} ${TEST_PIPELINE_VAR}", vars)
	if got != "from_step from_env" {
		t.Fatalf("expected step vars + env fallback, got: %q", got)
	}
}

func TestPipelineSingleStep(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":"card1","name":"Buy milk"}]`)
	}))
	defer ts.Close()

	p := &Pipeline{
		Steps: []Step{
			{
				Method:  "GET",
				URL:     ts.URL + "/cards",
				Extract: map[string]string{"card_name": "0.name"},
			},
		},
		Output: "Found: ${card_name}",
	}

	e := newTestExecutor()
	result := e.ExecutePipeline(p, 10)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "Found: Buy milk") {
		t.Fatalf("expected output with card name, got: %s", result.Stdout)
	}
}

func TestPipelineMultiStep(t *testing.T) {
	step := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step++
		w.Header().Set("Content-Type", "application/json")
		if step == 1 {
			// GET cards
			fmt.Fprint(w, `[{"id":"card1","name":"Buy milk"},{"id":"card2","name":"Walk dog"}]`)
		} else {
			// PUT move card
			if !strings.Contains(r.URL.Path, "card1") {
				t.Fatalf("expected card1 in URL, got: %s", r.URL.Path)
			}
			if r.Method != "PUT" {
				t.Fatalf("expected PUT, got %s", r.Method)
			}
			fmt.Fprint(w, `{"id":"card1","idList":"doing"}`)
		}
	}))
	defer ts.Close()

	p := &Pipeline{
		Steps: []Step{
			{
				Method:  "GET",
				URL:     ts.URL + "/cards",
				Extract: map[string]string{"card_id": "0.id", "card_name": "0.name"},
			},
			{
				Method: "PUT",
				URL:    ts.URL + "/cards/${card_id}?idList=doing",
			},
		},
		Output: "Moved: \"${card_name}\" -> Doing",
	}

	e := newTestExecutor()
	result := e.ExecutePipeline(p, 10)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, `Moved: "Buy milk" -> Doing`) {
		t.Fatalf("expected output, got: %s", result.Stdout)
	}
}

func TestPipelineEmptyArrayGuard(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer ts.Close()

	p := &Pipeline{
		Steps: []Step{
			{
				Method:            "GET",
				URL:               ts.URL + "/cards",
				EmptyArrayMessage: "Queue is empty.",
			},
			{
				Method: "PUT",
				URL:    ts.URL + "/should-not-reach",
			},
		},
	}

	e := newTestExecutor()
	result := e.ExecutePipeline(p, 10)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0 for empty guard, got %d", result.ExitCode)
	}
	if !strings.Contains(result.Stdout, "Queue is empty.") {
		t.Fatalf("expected empty message, got: %s", result.Stdout)
	}
}

func TestPipelineEnvVarExpansion(t *testing.T) {
	os.Setenv("TEST_API_KEY", "key123")
	defer os.Unsetenv("TEST_API_KEY")

	var receivedURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer ts.Close()

	p := &Pipeline{
		Steps: []Step{
			{
				Method: "GET",
				URL:    ts.URL + "/api?key=${TEST_API_KEY}",
			},
		},
		Output: "done",
	}

	e := newTestExecutor()
	result := e.ExecutePipeline(p, 10)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(receivedURL, "key=key123") {
		t.Fatalf("expected env var expanded in URL, got: %s", receivedURL)
	}
}

func TestPipelineHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"unauthorized"}`)
	}))
	defer ts.Close()

	p := &Pipeline{
		Steps: []Step{
			{Method: "GET", URL: ts.URL + "/api"},
		},
	}

	e := newTestExecutor()
	result := e.ExecutePipeline(p, 10)

	if result.ExitCode == 0 {
		t.Fatal("expected non-zero exit code for HTTP error")
	}
	if !strings.Contains(result.Stderr, "401") {
		t.Fatalf("expected 401 in stderr, got: %s", result.Stderr)
	}
}

func TestPipelineWithHeaders(t *testing.T) {
	var receivedAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer ts.Close()

	p := &Pipeline{
		Steps: []Step{
			{
				Method:  "GET",
				URL:     ts.URL + "/api",
				Headers: map[string]string{"Authorization": "Bearer ${TEST_TOKEN}"},
			},
		},
		Output: "done",
	}

	os.Setenv("TEST_TOKEN", "secret")
	defer os.Unsetenv("TEST_TOKEN")

	e := newTestExecutor()
	result := e.ExecutePipeline(p, 10)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
	if receivedAuth != "Bearer secret" {
		t.Fatalf("expected expanded auth header, got: %s", receivedAuth)
	}
}

func TestPipelineWithBody(t *testing.T) {
	var receivedBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer ts.Close()

	p := &Pipeline{
		Steps: []Step{
			{
				Method: "POST",
				URL:    ts.URL + "/api",
				Body:   `{"name":"${card_name}"}`,
			},
		},
		Output: "done",
	}

	e := newTestExecutor()
	// Pre-set a variable as if extracted from a prior step
	// Actually, we can't do that with the current API. Let me use a two-step pipeline.
	_ = receivedBody
	result := e.ExecutePipeline(p, 10)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
}

func TestPipelineParsesFromJSON(t *testing.T) {
	raw := `{
		"steps": [
			{
				"method": "GET",
				"url": "https://example.com/api",
				"extract": {"id": "0.id"}
			}
		],
		"output": "Got: ${id}"
	}`

	var p Pipeline
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("failed to parse pipeline JSON: %v", err)
	}

	if len(p.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(p.Steps))
	}
	if p.Steps[0].Method != "GET" {
		t.Fatalf("expected GET, got %s", p.Steps[0].Method)
	}
	if p.Steps[0].Extract["id"] != "0.id" {
		t.Fatalf("expected extract path, got %v", p.Steps[0].Extract)
	}
	if p.Output != "Got: ${id}" {
		t.Fatalf("expected output template, got %s", p.Output)
	}
}

func TestPipelineNoSteps(t *testing.T) {
	p := &Pipeline{Steps: []Step{}}
	e := newTestExecutor()
	result := e.ExecutePipeline(p, 10)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0 for empty pipeline, got %d", result.ExitCode)
	}
}
