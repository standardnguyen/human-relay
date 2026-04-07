package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"git.ekaterina.net/administrator/human-relay/store"
)

const defaultScriptsDir = "/scripts"

// ExecuteScript runs a named script from the default scripts directory.
func (e *Executor) ExecuteScript(r *store.Request) *store.Result {
	return e.ExecuteScriptIn(r, defaultScriptsDir)
}

// ExecuteScriptIn detects the script type (.sh, .py, or .json) and routes to
// the appropriate executor. Lookup order: .sh, .py, .json.
func (e *Executor) ExecuteScriptIn(r *store.Request, dir string) *store.Result {
	shPath := fmt.Sprintf("%s/%s.sh", dir, r.ScriptName)
	if _, err := os.Stat(shPath); err == nil {
		return e.executeShell(r, shPath)
	}

	pyPath := fmt.Sprintf("%s/%s.py", dir, r.ScriptName)
	if _, err := os.Stat(pyPath); err == nil {
		return e.executePython(r, pyPath)
	}

	jsonPath := fmt.Sprintf("%s/%s.json", dir, r.ScriptName)
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return &store.Result{
			ExitCode: -1,
			Stderr:   fmt.Sprintf("script not found: tried %s.sh, %s.py, and %s.json in %s", r.ScriptName, r.ScriptName, r.ScriptName, dir),
		}
	}

	var p Pipeline
	if err := json.Unmarshal(data, &p); err != nil {
		return &store.Result{
			ExitCode: -1,
			Stderr:   fmt.Sprintf("failed to parse pipeline: %v", err),
		}
	}

	return e.ExecutePipeline(&p, r.Timeout)
}

// executeShell runs a shell script with the relay's environment.
func (e *Executor) executeShell(r *store.Request, path string) *store.Result {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = e.config.DefaultTimeout
	}
	if timeout > e.config.MaxTimeout {
		timeout = e.config.MaxTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", path)

	stdout := &limitedWriter{max: maxOutputBytes}
	stderr := &limitedWriter{max: maxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	result := &store.Result{
		Stdout: stdout.buf.String(),
		Stderr: stderr.buf.String(),
	}
	if stdout.hit {
		result.Stderr += "\n[human-relay: stdout truncated at 1MB]"
	}
	if stderr.hit {
		result.Stderr += "\n[human-relay: stderr truncated at 1MB]"
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result.ExitCode = -1
			result.Stderr = fmt.Sprintf("script timed out after %ds\n%s", timeout, result.Stderr)
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
			result.Stderr = err.Error()
		}
	}

	return result
}

// executePython runs a Python script with the relay's environment.
func (e *Executor) executePython(r *store.Request, path string) *store.Result {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = e.config.DefaultTimeout
	}
	if timeout > e.config.MaxTimeout {
		timeout = e.config.MaxTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "python3", path)

	stdout := &limitedWriter{max: maxOutputBytes}
	stderr := &limitedWriter{max: maxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	result := &store.Result{
		Stdout: stdout.buf.String(),
		Stderr: stderr.buf.String(),
	}
	if stdout.hit {
		result.Stderr += "\n[human-relay: stdout truncated at 1MB]"
	}
	if stderr.hit {
		result.Stderr += "\n[human-relay: stderr truncated at 1MB]"
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result.ExitCode = -1
			result.Stderr = fmt.Sprintf("script timed out after %ds\n%s", timeout, result.Stderr)
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
			result.Stderr = err.Error()
		}
	}

	return result
}

// ExecuteScriptCreate writes a script file to the given directory.
// Detects type from content: JSON objects get .json extension, everything else gets .py.
func (e *Executor) ExecuteScriptCreate(r *store.Request, dir string) *store.Result {
	ext := detectScriptType(r.Stdin)
	scriptPath := fmt.Sprintf("%s/%s%s", dir, r.ScriptName, ext)

	mode := os.FileMode(0644)
	if ext == ".py" || ext == ".sh" {
		mode = 0755
	}

	if err := os.WriteFile(scriptPath, r.Stdin, mode); err != nil {
		return &store.Result{
			ExitCode: -1,
			Stderr:   fmt.Sprintf("failed to write script: %v", err),
		}
	}

	return &store.Result{
		ExitCode: 0,
		Stdout:   fmt.Sprintf("Created %s (%d bytes)", scriptPath, len(r.Stdin)),
	}
}

// detectScriptType returns ".json" for JSON objects, ".sh" for shell scripts
// (lines starting with #!/bin/bash, #!/bin/sh, or #!/usr/bin/env bash),
// and ".py" for everything else.
func detectScriptType(content []byte) string {
	var obj map[string]interface{}
	if json.Unmarshal(content, &obj) == nil {
		return ".json"
	}
	s := string(content)
	if strings.HasPrefix(s, "#!/bin/bash") ||
		strings.HasPrefix(s, "#!/bin/sh") ||
		strings.HasPrefix(s, "#!/usr/bin/env bash") ||
		strings.HasPrefix(s, "#!/usr/bin/env sh") {
		return ".sh"
	}
	return ".py"
}
