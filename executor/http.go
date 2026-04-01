package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"git.ekaterina.net/administrator/human-relay/store"
)

// envVarRe matches ${VAR_NAME} patterns for server-side env var expansion.
var envVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnvVars replaces ${VAR} placeholders with values from the relay's
// own environment. Unset variables expand to empty string.
func expandEnvVars(s string) string {
	return envVarRe.ReplaceAllStringFunc(s, func(match string) string {
		name := envVarRe.FindStringSubmatch(match)[1]
		return os.Getenv(name)
	})
}

func (e *Executor) ExecuteHTTP(r *store.Request) *store.Result {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = e.config.DefaultTimeout
	}
	if timeout > e.config.MaxTimeout {
		timeout = e.config.MaxTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	// Expand ${VAR} placeholders from the relay's environment.
	// The store retains the unexpanded versions (what the human reviewed).
	expandedURL := expandEnvVars(r.HTTPURL)
	expandedBody := expandEnvVars(r.HTTPBody)

	var bodyReader io.Reader
	if expandedBody != "" {
		bodyReader = strings.NewReader(expandedBody)
	}

	req, err := http.NewRequestWithContext(ctx, r.HTTPMethod, expandedURL, bodyReader)
	if err != nil {
		return &store.Result{
			ExitCode: -1,
			Stderr:   fmt.Sprintf("failed to create request: %v", err),
		}
	}

	for k, v := range r.HTTPHeaders {
		req.Header.Set(k, expandEnvVars(v))
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return &store.Result{
				ExitCode: -1,
				Stderr:   fmt.Sprintf("HTTP request timed out after %ds", timeout),
			}
		}
		// Scrub expanded credentials from error messages — Go's HTTP client
		// includes the full URL in errors (e.g. DNS failures), which would
		// leak expanded ${VAR} values into the audit log and agent context.
		errMsg := err.Error()
		errMsg = strings.ReplaceAll(errMsg, expandedURL, r.HTTPURL)
		for _, v := range r.HTTPHeaders {
			expanded := expandEnvVars(v)
			if expanded != v && expanded != "" {
				errMsg = strings.ReplaceAll(errMsg, expanded, "${***}")
			}
		}
		return &store.Result{
			ExitCode: -1,
			Stderr:   fmt.Sprintf("HTTP request failed: %s", errMsg),
		}
	}
	defer resp.Body.Close()

	// Read response body with the same 1MB limit as command output
	body := &limitedWriter{max: maxOutputBytes}
	io.Copy(body, resp.Body)

	stderr := ""
	if body.hit {
		stderr = "[human-relay: response body truncated at 1MB]"
	}

	// Collect response headers
	respHeaders := make(map[string]string)
	for k := range resp.Header {
		respHeaders[k] = resp.Header.Get(k)
	}

	// Map HTTP status to exit code: 2xx = 0, everything else = 1
	exitCode := 0
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		exitCode = 1
	}

	return &store.Result{
		ExitCode:    exitCode,
		Stdout:      body.buf.String(),
		Stderr:      stderr,
		StatusCode:  resp.StatusCode,
		RespHeaders: respHeaders,
	}
}
