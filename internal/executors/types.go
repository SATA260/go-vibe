package executors

import (
	"context"
	"io"
)

type Capability string

const (
	CapSessionFork  Capability = "SESSION_FORK"
	CapContextUsage Capability = "CONTEXT_USAGE"
	CapSetupHelper  Capability = "SETUP_HELPER"
)

type ExecutionEnv struct {
	Env map[string]string
}

type SpawnRequest struct {
	Dir    string
	Prompt string
	Env    *ExecutionEnv
}

type Spawned struct {
	Stdout    io.ReadCloser
	Stderr    io.ReadCloser
	Stdin     io.WriteCloser
	Wait      func() (int, error)
	Kill      func() error
	ForceKill func() error
	PID       int
}

type Availability struct {
	Installed bool
	LoggedIn  bool
	Detail    string
}

type Info struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Detail    string `json:"detail"`
}

type Executor interface {
	ID() string
	Name() string
	Spawn(ctx context.Context, request SpawnRequest) (*Spawned, error)
	Capabilities() []Capability
	Availability(ctx context.Context) Availability
}
