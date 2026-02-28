package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"git.ekaterina.net/administrator/human-relay/store"
)

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
		cmd.Dir = r.WorkingDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := &store.Result{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
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
	for _, allowed := range e.config.AllowedDirs {
		if strings.HasPrefix(dir, allowed) {
			return nil
		}
	}
	return fmt.Errorf("working directory %q is not in allowed directories", dir)
}
