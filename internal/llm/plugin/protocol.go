// Package plugin connects the core to model providers that ship as separate
// executables, spoken to over stdin/stdout. A plugin needs no rebuild of the
// core, and works wherever the core does: it is an ordinary child process.
//
// The wire format is JSON-RPC 2.0. Every payload below is built from the
// contract types in internal/llm, which already carry json tags, so a call
// crosses the boundary without losing anything the agent loop relies on.
package plugin

import (
	"fmt"

	"openuai/internal/llm"
)

// The methods a plugin answers. Describe is mandatory; the rest are optional
// and a plugin that does not implement one reports the corresponding
// capability as absent in its description.
const (
	MethodDescribe      = "provider/describe"
	MethodChat          = "provider/chat"
	MethodChatWithTools = "provider/chatWithTools"
	MethodReady         = "provider/ready"
	MethodSetSecret     = "provider/setSecret"
	MethodLogin         = "provider/login"
	MethodFetchModels   = "provider/fetchModels"
)

// Description is a plugin's answer to Describe: who it is, how it is
// authenticated, and which of the optional capabilities it supports. The core
// turns it into an llm.Descriptor without knowing anything else about the
// plugin.
type Description struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	// Credential is "secret", "login" or "none". It has to agree with the
	// capabilities below, and is checked against them when the plugin is
	// added; left out, it is derived from them.
	Credential   string `json:"credential"`
	DefaultModel string `json:"default_model"`
	// SecretPlaceholder and LoginLabel drive the credential widget, the way a
	// registered in-tree provider's descriptor does.
	SecretPlaceholder string `json:"secret_placeholder,omitempty"`
	LoginLabel        string `json:"login_label,omitempty"`
	// Models is the static fallback list.
	Models []string `json:"models,omitempty"`
	// Pricing is per million input and output tokens, keyed by model, so a
	// plugin's usage is costed like any other provider's.
	Pricing map[string][2]float64 `json:"pricing,omitempty"`
	// Capabilities the plugin implements beyond chat.
	SupportsTools       bool `json:"supports_tools,omitempty"`
	SupportsReady       bool `json:"supports_ready,omitempty"`
	SupportsSecret      bool `json:"supports_secret,omitempty"`
	SupportsLogin       bool `json:"supports_login,omitempty"`
	SupportsFetchModels bool `json:"supports_fetch_models,omitempty"`
}

// ChatRequest is the payload of Chat and ChatWithTools. Tools is empty for a
// plain Chat call.
type ChatRequest struct {
	Messages []llm.Message        `json:"messages"`
	Model    string               `json:"model"`
	Tools    []llm.ToolDefinition `json:"tools,omitempty"`
}

// ChatResult is the answer to both chat methods. ToolCalls is empty unless the
// model called a tool.
type ChatResult struct {
	Response  *llm.Response  `json:"response"`
	ToolCalls []llm.ToolCall `json:"tool_calls,omitempty"`
}

// ReadyResult is the answer to Ready.
type ReadyResult struct {
	Ready bool `json:"ready"`
}

// SecretRequest is the payload of SetSecret.
type SecretRequest struct {
	Secret string `json:"secret"`
}

// ModelsResult is the answer to FetchModels.
type ModelsResult struct {
	Models []string `json:"models"`
}

// Validate reports a description that contradicts itself, so that it is
// refused when the plugin is added — the one moment there is someone standing
// there to read the message — and again whenever a cached description is
// registered, since that one comes from a file a person can edit.
//
// Coercing was worse than refusing: a plugin that needs no credential
// declares neither a kind nor setSecret support, and the old default turned
// that into a secret field on the settings screen which rejected whatever was
// typed into it.
func (d Description) Validate() error {
	switch llm.CredentialKind(d.Credential) {
	case llm.CredentialSecret:
		if !d.SupportsSecret {
			return fmt.Errorf("provider plugin %q asks for a secret but does not implement %s", d.Name, MethodSetSecret)
		}
	case llm.CredentialLogin:
		if !d.SupportsLogin {
			return fmt.Errorf("provider plugin %q asks for an interactive login but does not implement %s", d.Name, MethodLogin)
		}
	case llm.CredentialNone, "":
		// Nothing is asked for, so there is nothing to contradict: an unstated
		// kind is derived from the capabilities instead.
	default:
		return fmt.Errorf("provider plugin %q declares credential %q, want %q, %q or %q",
			d.Name, d.Credential, llm.CredentialSecret, llm.CredentialLogin, llm.CredentialNone)
	}
	return nil
}

// credentialKind is the kind of credential the settings screen should ask
// for. A description that named one has been checked against the capability
// backing it by Validate; one that named none, or named something this core
// does not know, is answered from what the plugin says it can do — never with
// a widget whose input the plugin will refuse.
func (d Description) credentialKind() llm.CredentialKind {
	switch kind := llm.CredentialKind(d.Credential); kind {
	case llm.CredentialSecret, llm.CredentialLogin, llm.CredentialNone:
		return kind
	}
	switch {
	case d.SupportsSecret:
		return llm.CredentialSecret
	case d.SupportsLogin:
		return llm.CredentialLogin
	default:
		return llm.CredentialNone
	}
}

// Descriptor converts a plugin's description into the registry's descriptor.
// New is left to the caller, which owns the plugin's lifecycle.
func (d Description) Descriptor(new func(llm.Store) llm.Provider) llm.Descriptor {
	credential := d.credentialKind()

	display := d.DisplayName
	if display == "" {
		display = d.Name
	}

	return llm.Descriptor{
		Name:              d.Name,
		DisplayName:       display,
		Credential:        credential,
		SecretPlaceholder: d.SecretPlaceholder,
		LoginLabel:        d.LoginLabel,
		DefaultModel:      d.DefaultModel,
		New:               new,
	}
}
