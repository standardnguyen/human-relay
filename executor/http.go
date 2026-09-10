package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/standardnguyen/human-relay/store"
)

// envVarRe matches ${VAR_NAME} patterns for server-side env var expansion.
var envVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// protectedEnvVars are relay-security-critical variables that must never be
// substituted into an outbound request. The reviewer approves the unexpanded
// string, so a request referencing ${MHR_AUTH_TOKEN} looks harmless at review
// time but would otherwise resolve to the relay's own MCP bearer secret and
// ship it to whatever host the request points at.
//
// Deliberately narrow: the rest of the MHR_* surface is non-secret config
// (ports, paths, timeouts), and expansion of other real secrets (a vaulted API
// key set as an env var on the relay container) is the feature's whole point.
var protectedEnvVars = map[string]bool{
	"MHR_AUTH_TOKEN": true,
}

// expandEnvVars replaces ${VAR} placeholders with values from the relay's
// own environment. Unset variables expand to empty string. Placeholders naming
// a protected variable are left literal, so they can never resolve to the
// relay's own auth secret.
func expandEnvVars(s string) string {
	return envVarRe.ReplaceAllStringFunc(s, func(match string) string {
		name := envVarRe.FindStringSubmatch(match)[1]
		if protectedEnvVars[name] {
			return match
		}
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
	multipartContentType := ""
	if r.HTTPFormFile != nil {
		// Fetch the file bytes by running the fetch command (built and shown
		// to the reviewer at request time), then build a multipart body.
		ff := r.HTTPFormFile
		fileBytes, err := exec.CommandContext(ctx, ff.FetchCmd[0], ff.FetchCmd[1:]...).Output()
		if err != nil {
			stderr := ""
			if exitErr, ok := err.(*exec.ExitError); ok {
				stderr = strings.TrimSpace(string(exitErr.Stderr))
			}
			return &store.Result{
				ExitCode: -1,
				Stderr:   fmt.Sprintf("form_file fetch failed (%s): %v %s", ff.Source, err, stderr),
			}
		}
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for k, v := range r.HTTPFormFields {
			w.WriteField(k, expandEnvVars(v))
		}
		fw, err := w.CreateFormFile(ff.Field, ff.Filename)
		if err != nil {
			return &store.Result{
				ExitCode: -1,
				Stderr:   fmt.Sprintf("failed to build multipart body: %v", err),
			}
		}
		fw.Write(fileBytes)
		w.Close()
		bodyReader = &buf
		multipartContentType = w.FormDataContentType()
	} else if expandedBody != "" {
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
	if multipartContentType != "" {
		// The boundary is generated at execution time; override any
		// caller-supplied Content-Type.
		req.Header.Set("Content-Type", multipartContentType)
	}

	// Never follow redirects: Go's default policy only strips Authorization,
	// Cookie and WWW-Authenticate on a cross-host hop, so any other custom
	// header — including one holding an expanded ${VAR} secret — would be
	// forwarded to whatever host a 3xx points at. The reviewer approved the
	// original URL, not the redirect target. Return the 3xx itself instead;
	// it maps to ExitCode 1 and RespHeaders carries Location for inspection.
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
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
