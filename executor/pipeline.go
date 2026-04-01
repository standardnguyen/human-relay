package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"git.ekaterina.net/administrator/human-relay/store"
)

// Pipeline defines a sequence of HTTP steps with variable extraction between them.
type Pipeline struct {
	Steps  []Step `json:"steps"`
	Output string `json:"output"`
}

// Step is a single HTTP call in a pipeline.
type Step struct {
	Method            string            `json:"method"`
	URL               string            `json:"url"`
	Headers           map[string]string `json:"headers,omitempty"`
	Body              string            `json:"body,omitempty"`
	Extract           map[string]string `json:"extract,omitempty"`
	EmptyArrayMessage string            `json:"empty_array_message,omitempty"`
}

// varRe matches ${VAR_NAME} patterns.
var varRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandVarsMap replaces ${VAR} with values from vars map, falling back to os.Getenv.
func expandVarsMap(s string, vars map[string]string) string {
	return varRe.ReplaceAllStringFunc(s, func(match string) string {
		name := varRe.FindStringSubmatch(match)[1]
		if v, ok := vars[name]; ok {
			return v
		}
		return os.Getenv(name)
	})
}

// jsonPath extracts a value from JSON data using a simple dot-separated path.
//
// Supported syntax:
//   - "0.id"           → array index 0, then field "id"
//   - "user.name"      → object field "user", then "name"
//   - "#"              → array length (as string)
//   - "0.tags.#"       → length of nested array
func jsonPath(data []byte, path string) (string, error) {
	var root interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return "", fmt.Errorf("invalid JSON: %v", err)
	}

	parts := strings.Split(path, ".")
	current := root

	for _, part := range parts {
		if part == "#" {
			arr, ok := current.([]interface{})
			if !ok {
				return "", fmt.Errorf("# applied to non-array")
			}
			return strconv.Itoa(len(arr)), nil
		}

		// Try as array index
		if idx, err := strconv.Atoi(part); err == nil {
			arr, ok := current.([]interface{})
			if !ok {
				return "", fmt.Errorf("index %d applied to non-array", idx)
			}
			if idx < 0 || idx >= len(arr) {
				return "", fmt.Errorf("index %d out of bounds (len=%d)", idx, len(arr))
			}
			current = arr[idx]
			continue
		}

		// Object field access
		obj, ok := current.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("field %q applied to non-object", part)
		}
		val, exists := obj[part]
		if !exists {
			return "", fmt.Errorf("field %q not found", part)
		}
		current = val
	}

	// Convert final value to string
	switch v := current.(type) {
	case string:
		return v, nil
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10), nil
		}
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(v), nil
	case nil:
		return "", nil
	default:
		// Nested object/array — return as JSON
		b, _ := json.Marshal(v)
		return string(b), nil
	}
}

// ExecutePipeline runs a pipeline definition.
func (e *Executor) ExecutePipeline(p *Pipeline, timeout int) *store.Result {
	if timeout <= 0 {
		timeout = e.config.DefaultTimeout
	}
	if timeout > e.config.MaxTimeout {
		timeout = e.config.MaxTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	vars := make(map[string]string)
	client := &http.Client{}

	for i, step := range p.Steps {
		// Expand variables in all fields
		method := step.Method
		url := expandVarsMap(step.URL, vars)
		body := expandVarsMap(step.Body, vars)

		var bodyReader io.Reader
		if body != "" {
			bodyReader = strings.NewReader(body)
		}

		req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
		if err != nil {
			return &store.Result{
				ExitCode: -1,
				Stderr:   fmt.Sprintf("step %d: failed to create request: %v", i+1, err),
			}
		}

		for k, v := range step.Headers {
			req.Header.Set(k, expandVarsMap(v, vars))
		}

		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return &store.Result{
					ExitCode: -1,
					Stderr:   fmt.Sprintf("step %d: timed out after %ds", i+1, timeout),
				}
			}
			return &store.Result{
				ExitCode: -1,
				Stderr:   fmt.Sprintf("step %d: request failed: %v", i+1, err),
			}
		}

		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		// Check for HTTP errors
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return &store.Result{
				ExitCode: 1,
				Stderr:   fmt.Sprintf("step %d: HTTP %d\n%s", i+1, resp.StatusCode, string(respBody)),
			}
		}

		// Empty array guard
		if step.EmptyArrayMessage != "" {
			var arr []interface{}
			if err := json.Unmarshal(respBody, &arr); err == nil && len(arr) == 0 {
				return &store.Result{
					ExitCode: 0,
					Stdout:   step.EmptyArrayMessage,
				}
			}
		}

		// Extract variables
		for varName, path := range step.Extract {
			val, err := jsonPath(respBody, path)
			if err != nil {
				return &store.Result{
					ExitCode: -1,
					Stderr:   fmt.Sprintf("step %d: extract %q (%s): %v", i+1, varName, path, err),
				}
			}
			vars[varName] = val
		}
	}

	output := expandVarsMap(p.Output, vars)
	return &store.Result{
		ExitCode: 0,
		Stdout:   output,
	}
}

