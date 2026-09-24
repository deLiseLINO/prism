package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"sync"
	"time"

	"prism/internal/integrations"
	"prism/internal/management"
)

// newIntegrationRegistry builds one full client registry against a transport:
// nil io is the local disk, a remote host passes its SshIO and home.
func newIntegrationRegistry(port int, env integrations.Env, home string, io integrations.FileIO, modelsSrc func() []integrations.Model) (*integrations.Registry, *integrations.CodexIntegration, error) {
	registry := integrations.NewRegistry()
	models := integrations.DefaultPrismModels
	codexIntegration := integrations.NewCodex(integrations.CodexOptions{Port: port, Models: models, ModelsSource: modelsSrc, Env: env, Home: home, IO: io})
	if err := registry.Register(codexIntegration); err != nil {
		return nil, nil, err
	}
	if err := registry.Register(integrations.NewGrok(integrations.GrokOptions{Port: port, Models: models, ModelsSource: modelsSrc, Env: env, Home: home, IO: io})); err != nil {
		return nil, nil, err
	}
	if err := registry.Register(integrations.NewOmp(integrations.OmpOptions{Port: port, Models: models, ModelsSource: modelsSrc, Env: env, Home: home, IO: io})); err != nil {
		return nil, nil, err
	}
	if err := registry.Register(integrations.NewClaude(integrations.ClaudeOptions{Port: port, Models: models, ModelsSource: modelsSrc, Env: env, Home: home, IO: io})); err != nil {
		return nil, nil, err
	}
	if err := registry.Register(integrations.NewPi(integrations.PiOptions{Port: port, Models: models, ModelsSource: modelsSrc, Env: env, Home: home, IO: io})); err != nil {
		return nil, nil, err
	}
	if err := registry.Register(integrations.NewOpencode(integrations.OpencodeOptions{Port: port, Models: models, ModelsSource: modelsSrc, Env: env, Home: home, IO: io})); err != nil {
		return nil, nil, err
	}
	if err := registry.Register(integrations.NewHermes(integrations.HermesOptions{Port: port, Models: models, ModelsSource: modelsSrc, Env: env, Home: home, IO: io})); err != nil {
		return nil, nil, err
	}
	return registry, codexIntegration, nil
}

// superviseReverseTunnel keeps one `ssh -N -R` alive per remote host so client
// traffic on the host's loopback reaches this daemon. A dead tunnel restarts
// with backoff; daemon shutdown cancels the context and the ssh exits.
func superviseReverseTunnel(ctx context.Context, address string, port int) {
	const maxBackoff = 30 * time.Second
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		cmd := exec.CommandContext(ctx, "ssh", "-N",
			"-o", "ExitOnForwardFailure=yes",
			"-o", "ServerAliveInterval=15",
			"-o", "ServerAliveCountMax=3",
			"-R", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", port, port),
			address)
		err := cmd.Run()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("prismd: reverse tunnel to %s exited: %v", address, err)
		}
		log.Printf("prismd: reverse tunnel to %s dropped, reconnecting in %s", address, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// hostSupervisor owns the live half of every remote host: the SSH probe, the
// per-host integration registry, and the reverse tunnel. It is the single
// writer behind management's HostLifecycle, so config mutations and runtime
// state never race on the same host id.
type hostSupervisor struct {
	baseCtx context.Context
	port    int
	models  func() []integrations.Model
	table   *management.HostRegistries

	mu      sync.Mutex
	cancels map[string]func()
}

func newHostSupervisor(ctx context.Context, port int, models func() []integrations.Model, table *management.HostRegistries) *hostSupervisor {
	return &hostSupervisor{baseCtx: ctx, port: port, models: models, table: table, cancels: map[string]func(){}}
}

func (h *hostSupervisor) Ensure(ctx context.Context, id string, address string) error {
	h.Remove(id)
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	remoteHome, err := integrations.SshHome(probeCtx, address)
	if err != nil {
		h.table.SetUnresolved(id, err.Error())
		return err
	}
	remoteRegistry, _, err := newIntegrationRegistry(h.port, integrations.Env{}, remoteHome, integrations.NewSshIO(address), h.models)
	if err != nil {
		h.table.SetUnresolved(id, err.Error())
		return err
	}
	tunnelCtx, tunnelCancel := context.WithCancel(h.baseCtx)
	h.mu.Lock()
	h.cancels[id] = tunnelCancel
	h.mu.Unlock()
	h.table.SetRemote(id, remoteRegistry)
	go superviseReverseTunnel(tunnelCtx, address, h.port)
	log.Printf("prismd: host %s (%s) connected, home=%s", id, address, remoteHome)
	return nil
}

func (h *hostSupervisor) Remove(id string) {
	h.mu.Lock()
	cancel, present := h.cancels[id]
	if present {
		delete(h.cancels, id)
	}
	h.mu.Unlock()
	if present {
		cancel()
	}
	h.table.Forget(id)
}
