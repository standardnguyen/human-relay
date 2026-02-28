package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"git.ekaterina.net/administrator/human-relay/store"
)

const maxOutputBytes = 1 << 20 // 1MB

// limitedWriter caps writes at a maximum byte count, silently discarding overflow.
type limitedWriter struct {
	buf bytes.Buffer
	max int
	hit bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	n := len(p) // always report full consumption so the process continues
	remaining := w.max - w.buf.Len()
	if remaining <= 0 {
		w.hit = true
		return n, nil
	}
	if n > remaining {
		w.hit = true
		p = p[:remaining]
	}
	w.buf.Write(p)
	return n, nil
}

type Config struct {
	DefaultTimeout int
	MaxTimeout     int
	AllowedDirs    []string
}

type Executor struct {
	config Config
}

func New(cfg Config) *Executor {
	return &Executor{config: cfg}
}

func (e *Executor) Execute(r *store.Request) *store.Result {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = e.config.DefaultTimeout
	}
	if timeout > e.config.MaxTimeout {
		timeout = e.config.MaxTimeout
	}

	if err := e.validateWorkingDir(r.WorkingDir); err != nil {
		return &store.Result{
			ExitCode: -1,
			Stderr:   err.Error(),
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if r.Shell {
		// Shell mode: concatenate command and args for sh -c
		full := r.Command
		if len(r.Args) > 0 {
			full += " " + strings.Join(r.Args, " ")
		}
		cmd = exec.CommandContext(ctx, "sh", "-c", full)
	} else {
		cmd = exec.CommandContext(ctx, r.Command, r.Args...)
	}

	if r.WorkingDir != "" {
		cmd.Dir = filepath.Clean(r.WorkingDir)
	}

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
			result.Stderr = fmt.Sprintf("command timed out after %ds\n%s", timeout, result.Stderr)
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
			result.Stderr = err.Error()
		}
	}

	return result
}

func (e *Executor) validateWorkingDir(dir string) error {
	if dir == "" || len(e.config.AllowedDirs) == 0 {
		return nil
	}
	cleanDir := filepath.Clean(dir)
	for _, allowed := range e.config.AllowedDirs {
		cleanAllowed := filepath.Clean(allowed)
		if cleanDir == cleanAllowed || strings.HasPrefix(cleanDir, cleanAllowed+"/") {
			return nil
		}
	}
	return fmt.Errorf("working directory %q is not in allowed directories", dir)
}
