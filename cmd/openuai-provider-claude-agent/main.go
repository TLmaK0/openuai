// Command openuai-provider-claude-agent is a model provider that runs turns
// through the headless agent already installed on the machine, instead of
// calling a model API directly.
//
// It is a provider plugin: a newline-delimited JSON-RPC server on stdin and
// stdout, started by the core as an ordinary child process. Adding it needs no
// change to the core and no rebuild of it.
//
// It is a model *backend*, not a second agent. The headless agent is given no
// tools of its own, so openuai keeps its own agent loop, its own tools and its
// own permission prompts, and only the tokens come from here. Handing a whole
// turn over to the headless agent instead would mean two agent loops and two
// permission models in one product, which is a different feature and a
// different decision.
//
// Authentication is *prior* and external: the user signs in with their own
// agent, before and outside openuai, and this plugin implements no login of
// any kind. An API key is accepted as an alternative for whoever prefers one,
// because a key is a value the user supplies rather than a login flow.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"openuai/internal/llm"
	"openuai/internal/llm/plugin"
)

// The tier aliases the headless agent resolves to the current model of that
// tier. Aliases rather than pinned ids on purpose: a pinned dated id goes
// stale, and the resolved id comes back on every run anyway, so usage is still
// costed against a real model name.
var models = []string{"opus", "sonnet", "haiku"}

// pricing is per million input and output tokens. The core ships no prices of
// its own — each provider declares what its models cost — so a provider that
// declares none has every turn costed at zero.
//
// Both the alias and the id it currently resolves to are declared. A run
// reports the resolved id, which is what gets priced; the alias is the safety
// net for a run that reported no id to resolve.
//
// These are published rates, so they go stale when the rates change or a tier
// moves to a new model. That is the same exposure the in-tree providers carry,
// and it is why the run's own total_cost_usd would be better — but that figure
// cannot cross this boundary, since llm.Response carries tokens and a model
// name and no cost. Noted on the pull request rather than worked around here.
var pricing = map[string][2]float64{
	"opus":             {5.00, 25.00},
	"claude-opus-5":    {5.00, 25.00},
	"sonnet":           {2.00, 10.00},
	"claude-sonnet-5":  {2.00, 10.00},
	"haiku":            {1.00, 5.00},
	"claude-haiku-4-5": {1.00, 5.00},
}

// systemPrompt replaces the headless agent's own. Its default prompt is for a
// coding agent driving its own tools; here it has none, and the instructions
// for the turn arrive from openuai's agent loop in the messages.
const systemPrompt = "You are the model behind another agent. Answer the last message using only the " +
	"conversation given to you. Do not describe tools, do not offer to run commands, and do not ask to " +
	"take actions: the agent you answer to owns all of that."

func main() {
	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	store := loadStore()

	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return
		}

		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			// A caller that is not speaking JSON-RPC is not a caller.
			return
		}

		reply := func(result any) {
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			out.Write(body)
			out.WriteString("\n")
			out.Flush()
		}
		replyError := func(err error) {
			body, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32000, "message": err.Error()},
			})
			out.Write(body)
			out.WriteString("\n")
			out.Flush()
		}

		switch req.Method {
		case plugin.MethodDescribe:
			reply(describe())

		case plugin.MethodReady:
			// ReadyResult carries a bool and nothing else, so the reason
			// cannot travel with the verdict. It goes to stderr, which the
			// core drains into its log, and it is returned again by the first
			// turn that is attempted — see the note in the issue: putting it
			// on the settings screen needs a field this plugin is not allowed
			// to add.
			err := readiness(store.Secret)
			if err != nil {
				fmt.Fprintf(os.Stderr, "not ready: %s\n", err)
			}
			reply(plugin.ReadyResult{Ready: err == nil})

		case plugin.MethodSetSecret:
			var sr plugin.SecretRequest
			if err := json.Unmarshal(req.Params, &sr); err != nil {
				replyError(err)
				continue
			}
			store.Secret = strings.TrimSpace(sr.Secret)
			if err := store.save(); err != nil {
				replyError(err)
				continue
			}
			reply(struct{}{})

		case plugin.MethodChat:
			var call plugin.ChatRequest
			if err := json.Unmarshal(req.Params, &call); err != nil {
				replyError(err)
				continue
			}
			result, err := chat(context.Background(), store.Secret, call)
			if err != nil {
				replyError(err)
				continue
			}
			reply(result)

		case plugin.MethodChatWithTools:
			var call plugin.ChatRequest
			if err := json.Unmarshal(req.Params, &call); err != nil {
				replyError(err)
				continue
			}
			result, err := chatWithTools(context.Background(), store.Secret, call)
			if err != nil {
				replyError(err)
				continue
			}
			reply(result)

		default:
			replyError(fmt.Errorf("unknown method %q", req.Method))
		}
	}
}

func describe() plugin.Description {
	return plugin.Description{
		Name: "claude-headless",
		// Not "Claude Code": Anthropic's branding guidance does not permit a
		// third-party product to carry that name, and does permit "Claude
		// Agent". The shape follows the in-tree providers, which name the
		// product and then how it authenticates.
		DisplayName: "Claude Agent (headless)",
		Credential:  string(llm.CredentialSecret),
		// The key is optional, and saying so here is the only place the user
		// finds out: the ordinary path is the session they already signed in
		// to, outside openuai.
		SecretPlaceholder:   "optional — leave empty to use the session already signed in on this machine",
		DefaultModel:        "opus",
		Models:              models,
		Pricing:             pricing,
		SupportsTools:       true,
		SupportsReady:       true,
		SupportsSecret:      true,
		SupportsFetchModels: false,
	}
}

// readiness reports why a turn would fail, before one is attempted. It runs no
// inference: a readiness check is bounded at five seconds by the core, while a
// turn with a bad credential is retried for three minutes, so asking the model
// would report nothing but "not ready" — the silent dead provider this exists
// to avoid. Both probes below answer in well under a second.
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
		return fmt.Errorf("%s reported an authentication status this plugin cannot read", binary)
	}
	if !auth.LoggedIn {
		return fmt.Errorf("%s is installed but not signed in: run `claude auth login`, or set an API key for this provider",
			binary)
	}
	return nil
}

func chat(ctx context.Context, secret string, call plugin.ChatRequest) (plugin.ChatResult, error) {
	if err := readiness(secret); err != nil {
		return plugin.ChatResult{}, err
	}
	out, err := invocation{
		model:  call.Model,
		system: systemPrompt,
		prompt: renderPrompt(call.Messages, nil),
		apiKey: secret,
	}.run(ctx)
	if err != nil {
		return plugin.ChatResult{}, err
	}
	return plugin.ChatResult{Response: out.response(call.Model)}, nil
}

// chatWithTools asks for a structured answer, because the headless agent has
// no tools of its own to call: the tools belong to openuai, so a call to one
// has to come back as data for openuai's loop to run.
func chatWithTools(ctx context.Context, secret string, call plugin.ChatRequest) (plugin.ChatResult, error) {
	if err := readiness(secret); err != nil {
		return plugin.ChatResult{}, err
	}
	if len(call.Tools) == 0 {
		return chat(ctx, secret, call)
	}

	out, err := invocation{
		model:      call.Model,
		system:     systemPrompt + " " + toolInstruction,
		prompt:     renderPrompt(call.Messages, call.Tools),
		jsonSchema: toolSchema,
		apiKey:     secret,
	}.run(ctx)
	if err != nil {
		return plugin.ChatResult{}, err
	}

	answer, err := parseAnswer(out)
	if err != nil {
		return plugin.ChatResult{}, err
	}
	resp := out.response(call.Model)
	resp.Content = answer.Reply
	return plugin.ChatResult{Response: resp, ToolCalls: answer.toolCalls()}, nil
}
