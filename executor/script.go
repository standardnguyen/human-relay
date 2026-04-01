package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"git.ekaterina.net/administrator/human-relay/store"
)

const defaultScriptsDir = "/scripts"

// ExecuteScript runs a named script from the default scripts directory.
func (e *Executor) ExecuteScript(r *store.Request) *store.Result {
	return e.ExecuteScriptIn(r, defaultScriptsDir)
}

// ExecuteScriptIn runs a named script from the given directory.
func (e *Executor) ExecuteScriptIn(r *store.Request, dir string) *store.Result {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = e.config.DefaultTimeout
	}
	if timeout > e.config.MaxTimeout {
		timeout = e.config.MaxTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	scriptPath := fmt.Sprintf("%s/%s.sh", dir, r.ScriptName)

	cmd := exec.CommandContext(ctx, "bash", scriptPath)

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

// ExecuteScriptCreate writes a script file to the default scripts directory.
func (e *Executor) ExecuteScriptCreate(r *store.Request, dir string) *store.Result {
	scriptPath := fmt.Sprintf("%s/%s.sh", dir, r.ScriptName)

	if err := os.WriteFile(scriptPath, r.Stdin, 0755); err != nil {
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
