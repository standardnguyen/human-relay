package executor

import (
	"encoding/json"
	"fmt"
	"os"

	"git.ekaterina.net/administrator/human-relay/store"
)

const defaultScriptsDir = "/scripts"

// ExecuteScript runs a named script from the default scripts directory.
func (e *Executor) ExecuteScript(r *store.Request) *store.Result {
	return e.ExecuteScriptIn(r, defaultScriptsDir)
}

// ExecuteScriptIn reads a pipeline JSON file and executes it.
func (e *Executor) ExecuteScriptIn(r *store.Request, dir string) *store.Result {
	scriptPath := fmt.Sprintf("%s/%s.json", dir, r.ScriptName)

	data, err := os.ReadFile(scriptPath)
	if err != nil {
		return &store.Result{
			ExitCode: -1,
			Stderr:   fmt.Sprintf("failed to read script: %v", err),
		}
	}

	var p Pipeline
	if err := json.Unmarshal(data, &p); err != nil {
		return &store.Result{
			ExitCode: -1,
			Stderr:   fmt.Sprintf("failed to parse script: %v", err),
		}
	}

	return e.ExecutePipeline(&p, r.Timeout)
}

// ExecuteScriptCreate writes a script file to the given directory.
func (e *Executor) ExecuteScriptCreate(r *store.Request, dir string) *store.Result {
	scriptPath := fmt.Sprintf("%s/%s.json", dir, r.ScriptName)

	if err := os.WriteFile(scriptPath, r.Stdin, 0644); err != nil {
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
