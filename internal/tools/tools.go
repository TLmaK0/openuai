package tools

import (
	"context"
	"sort"
	"sync"
)

type Parameter struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

type Definition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  []Parameter `json:"parameters"`
	// RequiresPermission indicates the permission level needed
	// "none" = always allowed, "session" = ask once per session, "always" = ask every time
	RequiresPermission string `json:"requires_permission"`
}

type Result struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
	// Images holds base64-encoded PNG data produced by the tool (e.g. a
	// computer-use screenshot). The agent attaches these to the tool-result
	// message so vision models can see them.
	Images []string `json:"images,omitempty"`
}

type Tool interface {
	Definition() Definition
	Execute(ctx context.Context, args map[string]string) Result
}

// Registry holds the tools offered to the agent. It is safe for concurrent
// use: MCP servers connected or removed at runtime change it while a turn
// may be reading it.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Definition().Name] = t
}

// Unregister removes the named tool, if present.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) All() []Tool {
	r.mu.RLock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	r.mu.RUnlock()
	// Stable order: tool lists are sent to the model and hashed to reuse its
	// session, so a random map order would look like a different tool set.
	sort.Slice(out, func(i, j int) bool { return out[i].Definition().Name < out[j].Definition().Name })
	return out
}

func (r *Registry) Definitions() []Definition {
	r.mu.RLock()
	out := make([]Definition, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Definition())
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Without returns a new Registry that contains all tools except the named ones.
func (r *Registry) Without(names ...string) *Registry {
	exclude := make(map[string]struct{}, len(names))
	for _, n := range names {
		exclude[n] = struct{}{}
	}
	nr := NewRegistry()
	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, t := range r.tools {
		if _, skip := exclude[name]; !skip {
			nr.tools[name] = t
		}
	}
	return nr
}
