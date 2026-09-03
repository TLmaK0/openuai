// Package claudeheadless runs turns through the headless agent already
// installed on the machine, instead of calling a model API directly.
//
// It is a model backend, not a second agent. The headless run is given no
// tools of its own, so openuai keeps its agent loop, its tools and its
// permission prompts, and only the tokens come from the headless process.
// Handing a whole turn over instead would mean two agent loops and two
// permission models in one product, which is a different feature.
//
// Authentication is prior and external: the user signs in with their own
// agent, before and outside openuai, and this provider implements no login of
// any kind. An API key is accepted as an alternative, because a key is a value
// the user supplies rather than a login flow.
//
// It ships in the binary rather than as a plugin executable, which is what
// lets it be selected without anyone knowing an executable exists or typing a
// path to it. The plugin boundary is untouched and still buys what it was
// built for — adding a third-party or private provider at run time without a
// rebuild — but this provider needs none of that: it wraps a binary the user
// already has, and it travels with the product.
package claudeheadless

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"

	"openuai/internal/llm"
)

// secretKey is where the optional API key lives in the provider's own store.
const secretKey = "api_key"

// name is the stable identifier persisted in the configuration. It is the same
// name the plugin executable used, so a configuration that already selected
// that provider keeps resolving to this one.
const name = "claude-headless"

// The tier aliases the headless agent resolves to the current model of that
// tier. Aliases rather than pinned ids on purpose: a pinned dated id goes
// stale, and the resolved id comes back on every run anyway, so usage is still
// costed against a real model name.
var models = []string{"opus", "sonnet", "haiku"}

func init() {
	llm.Register(llm.Descriptor{
		Name: name,
		// Not "Claude Code": Anthropic's branding guidance does not permit a
		// third-party product to carry that name, and does permit "Claude
		// Agent". The shape follows the other in-tree providers, which name
		// the product and then how it authenticates.
		DisplayName: "Claude Agent (headless)",
		Credential:  llm.CredentialSecret,
		// The key is optional, and saying so here is the only place the user
		// finds out: the ordinary path is the session they already signed in
		// to, outside openuai.
		SecretPlaceholder: "optional — leave empty to use the session already signed in on this machine",
		DefaultModel:      "opus",
		New:               func(store llm.Store) llm.Provider { return New(store) },
	})

	// Prices per million input/output tokens, declared here so the core ships
	// no knowledge of this provider's models. Both the alias and the id it
	// currently resolves to are declared: a run reports the resolved id, which
	// is what gets priced, and the alias is the safety net for a run that
	// reported no id. A provider that declares no price has every turn costed
	// at zero, silently, because a missing price raises nothing.
	for model, price := range map[string][2]float64{
		"opus":             {5.00, 25.00},
		"claude-opus-5":    {5.00, 25.00},
		"sonnet":           {2.00, 10.00},
		"claude-sonnet-5":  {2.00, 10.00},
		"haiku":            {1.00, 5.00},
		"claude-haiku-4-5": {1.00, 5.00},
	} {
		llm.SetModelPricing(model, price[0], price[1])
	}
}

// systemPrompt replaces the headless agent's own. Its default prompt is for a
// coding agent driving its own tools; here it has none, and the instructions
// for the turn arrive from openuai's agent loop in the messages.
const systemPrompt = "You are the model behind another agent. Answer the last message using only the " +
	"conversation given to you. Do not describe tools, do not offer to run commands, and do not ask to " +
	"take actions: the agent you answer to owns all of that."

// Provider runs turns through the headless agent.
type Provider struct {
	store llm.Store

	mu     sync.RWMutex
	apiKey string
}

// New builds the provider, taking its optional API key from store.
func New(store llm.Store) *Provider {
	return &Provider{store: store, apiKey: store.Get(secretKey)}
}

func (p *Provider) Name() string     { return name }
func (p *Provider) Models() []string { return models }

func (p *Provider) key() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.apiKey
}

// SetSecret stores the optional API key.
func (p *Provider) SetSecret(secret string) error {
	p.mu.Lock()
	p.apiKey = secret
	p.mu.Unlock()
	return p.store.Set(secretKey, secret)
}

// Ready reports whether a turn could run at all. Unlike a provider that holds
// an API key, this one can be unusable for three different reasons, so the
// check answers all three — and it runs no inference, because a readiness
// check is on a settings screen and a turn with a bad credential is retried
// for three minutes.
func (p *Provider) Ready() bool {
	return readiness(p.key()) == nil
}

// Unavailable returns why a turn cannot run, or an empty string when it can.
// It is what names the missing thing rather than reporting a dead provider.
func (p *Provider) Unavailable() string {
	if err := readiness(p.key()); err != nil {
		return err.Error()
	}
	return ""
}

func (p *Provider) Chat(ctx context.Context, messages []llm.Message, model string) (*llm.Response, error) {
	secret := p.key()
	if err := readiness(secret); err != nil {
		return nil, err
	}
	out, err := invocation{
		model:  model,
		system: systemPrompt,
		prompt: renderPrompt(messages, nil),
		apiKey: secret,
	}.run(ctx)
	if err != nil {
		return nil, err
	}
	return out.response(model), nil
}

// ChatWithTools asks for a structured answer, because the headless agent has
// no tools of its own to call: the tools belong to openuai, so a call to one
// has to come back as data for openuai's loop to run.
func (p *Provider) ChatWithTools(ctx context.Context, messages []llm.Message, model string,
	toolDefs []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {

	if len(toolDefs) == 0 {
		resp, err := p.Chat(ctx, messages, model)
		return resp, nil, err
	}

	secret := p.key()
	if err := readiness(secret); err != nil {
		return nil, nil, err
	}

	out, err := invocation{
		model:      model,
		system:     systemPrompt + " " + toolInstruction,
		prompt:     renderPrompt(messages, toolDefs),
		jsonSchema: toolSchema,
		apiKey:     secret,
	}.run(ctx)
	if err != nil {
		return nil, nil, err
	}

	answer, err := parseAnswer(out)
	if err != nil {
		return nil, nil, err
	}
	resp := out.response(model)
	resp.Content = answer.Reply
	return resp, answer.toolCalls(), nil
}

// readiness reports why a turn would fail, before one is attempted. It runs no
// inference: a readiness check is made on a settings screen, while a turn with
// a bad credential is retried ten times over about three minutes, so asking
// the model would report nothing but "not ready" — a silent dead provider.
// Both probes below answer in well under a second.
func readiness(secret string) error {
	version, err := exec.Command(binary, "--version").Output()
	if err != nil {
		return fmt.Errorf("%s is not installed or not on PATH: install it, or use a provider that calls the API directly", binary)
	}
	if secret != "" {
		// An API key is checked when it is used. Nothing local can tell a
		// good key from a bad one, and pretending otherwise would need a
		// billed call on a settings screen.
		return nil
	}

	status, err := exec.Command(binary, "auth", "status", "--json").Output()
	if err != nil {
		return fmt.Errorf("%s %s cannot report its authentication status; it may be too old for `auth status`",
			binary, firstLine(string(version)))
	}
	var auth struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	if err := json.Unmarshal(status, &auth); err != nil {
		return fmt.Errorf("%s reported an authentication status this provider cannot read", binary)
	}
	if !auth.LoggedIn {
		return fmt.Errorf("%s is installed but not signed in: run `claude auth login`, or set an API key for this provider",
			binary)
	}
	return nil
}

// Compile-time proof that the capabilities the core reaches by type assertion
// are actually implemented, so a rename cannot drop one silently.
var (
	_ llm.Provider                         = (*Provider)(nil)
	_ llm.ToolCallProvider                 = (*Provider)(nil)
	_ interface{ Ready() bool }            = (*Provider)(nil)
	_ interface{ SetSecret(string) error } = (*Provider)(nil)
)
