package tools

import "context"

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
}

type Tool interface {
	Definition() Definition
	Execute(ctx context.Context, args map[string]string) Result
}

type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

func (r *Registry) Register(t Tool) {
	r.tools[t.Definition().Name] = t
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) All() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

func (r *Registry) Definitions() []Definition {
	out := make([]Definition, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Definition())
	}
	return out
}

// Without returns a new Registry that contains all tools except the named ones.
func (r *Registry) Without(names ...string) *Registry {
	exclude := make(map[string]struct{}, len(names))
	for _, n := range names {
		exclude[n] = struct{}{}
	}
	nr := NewRegistry()
	for name, t := range r.tools {
		if _, skip := exclude[name]; !skip {
			nr.tools[name] = t
		}
	}
	return nr
}
