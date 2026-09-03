package all

import (
	"testing"

	"openuai/internal/llm"
)

// memStore is a Store that keeps values in memory.
type memStore map[string]string

func (m memStore) Get(key string) string       { return m[key] }
func (m memStore) Set(key, value string) error { m[key] = value; return nil }

// The providers that ship in the binary must register themselves, and describe
// themselves well enough that a caller never needs to know their names.
func TestShippedProvidersRegister(t *testing.T) {
	want := map[string]struct {
		display    string
		credential llm.CredentialKind
		model      string
	}{
		"claude":          {"Claude (API key)", llm.CredentialSecret, "claude-sonnet-4-20250514"},
		"claude-headless": {"Claude Agent (headless)", llm.CredentialSecret, "opus"},
		"openai":          {"OpenAI (ChatGPT subscription)", llm.CredentialLogin, "gpt-5.1-codex"},
	}

	descriptors := llm.Descriptors()
	if len(descriptors) != len(want) {
		t.Fatalf("registered providers = %d, want %d", len(descriptors), len(want))
	}

	for _, d := range descriptors {
		w, ok := want[d.Name]
		if !ok {
			t.Errorf("unexpected provider registered: %q", d.Name)
			continue
		}
		if d.DisplayName != w.display {
			t.Errorf("%s DisplayName = %q, want %q", d.Name, d.DisplayName, w.display)
		}
		if d.Credential != w.credential {
			t.Errorf("%s Credential = %q, want %q", d.Name, d.Credential, w.credential)
		}
		if d.DefaultModel != w.model {
			t.Errorf("%s DefaultModel = %q, want %q", d.Name, d.DefaultModel, w.model)
		}
		if d.New == nil {
			t.Errorf("%s registered without a constructor", d.Name)
		}
	}
}

// A secret provider must offer a placeholder and a login provider a label,
// because the UI draws its credential widget from those two fields alone.
func TestCredentialMetadataIsComplete(t *testing.T) {
	for _, d := range llm.Descriptors() {
		switch d.Credential {
		case llm.CredentialSecret:
			if d.SecretPlaceholder == "" {
				t.Errorf("%s takes a secret but offers no placeholder", d.Name)
			}
		case llm.CredentialLogin:
			if d.LoginLabel == "" {
				t.Errorf("%s logs in interactively but offers no label", d.Name)
			}
		default:
			t.Errorf("%s has an unknown credential kind: %q", d.Name, d.Credential)
		}
	}
}

// The provider a fresh installation starts on stays the one it was before the
// registry existed, rather than whichever sorts first.
func TestDefaultProviderIsUnchanged(t *testing.T) {
	d, ok := llm.DefaultDescriptor()
	if !ok {
		t.Fatal("DefaultDescriptor() = not found, want a provider")
	}
	if d.Name != "openai" {
		t.Errorf("default provider = %q, want openai", d.Name)
	}
	if d.DefaultModel != "gpt-5.1-codex" {
		t.Errorf("default model = %q, want gpt-5.1-codex", d.DefaultModel)
	}
}

// Every shipped provider drives the agent loop, which relies on native tool
// calls, so each must implement ToolCallProvider.
func TestShippedProvidersSupportToolCalls(t *testing.T) {
	for _, d := range llm.Descriptors() {
		p := d.New(memStore{})
		if p == nil {
			t.Errorf("%s New() = nil", d.Name)
			continue
		}
		if p.Name() != d.Name {
			t.Errorf("%s reports Name() = %q", d.Name, p.Name())
		}
		if len(p.Models()) == 0 {
			t.Errorf("%s offers no static models", d.Name)
		}
		if _, ok := p.(llm.ToolCallProvider); !ok {
			t.Errorf("%s does not implement ToolCallProvider", d.Name)
		}
	}
}

// readinessIsNotAFunctionOfTheStore names the providers whose readiness cannot
// be decided from their store, with the reason. Every assertion below that
// reads readiness is about a provider that holds its own credential; for one
// whose credential comes from outside, "empty store" says nothing about
// whether a turn could run, in either direction.
//
// Keeping this as a named exemption rather than dropping the assertions keeps
// them meaningful for the providers they were written for.
var readinessIsNotAFunctionOfTheStore = map[string]string{
	// Its credential is normally the session the user signed in to outside
	// openuai, and its readiness also depends on an external binary being
	// installed — so it reports ready with an empty store on a machine that is
	// signed in, and not ready even with a secret on a machine where the
	// binary is absent. Its readiness is covered in its own package, where
	// that binary is faked.
	"claude-headless": "credential and binary are both external",
}

// Credentials come from, and go back to, the provider's own store — which is
// what lets the core stop declaring per-provider configuration fields.
func TestProvidersReadAndWriteTheirStore(t *testing.T) {
	for _, d := range llm.Descriptors() {
		t.Run(d.Name, func(t *testing.T) {
			external := readinessIsNotAFunctionOfTheStore[d.Name] != ""

			// With an empty store, a provider needing credentials is not ready.
			if !external && llm.Ready(d.New(memStore{})) {
				t.Error("provider with an empty store reports ready")
			}

			switch d.Credential {
			case llm.CredentialSecret:
				store := memStore{}
				p := d.New(store)
				if err := llm.SetSecret(p, "sk-test-secret"); err != nil {
					t.Fatalf("SetSecret() = %v, want nil", err)
				}
				if !external && !llm.Ready(p) {
					t.Error("provider is not ready after being given a secret")
				}
				// Persisted, so a restart finds it. This holds for every
				// provider that takes a secret, external readiness or not.
				if !storeHasValue(store, "sk-test-secret") {
					t.Errorf("secret was not persisted to the store: %v", store)
				}
				if !external && llm.Ready(d.New(store)) != true {
					t.Error("a provider rebuilt from the store is not ready")
				}

			case llm.CredentialLogin:
				// A saved session is restored from the store, without any
				// interactive login.
				store := memStore{"tokens": `{"access_token":"at","refresh_token":"rt","account_id":"acct"}`}
				if !llm.Ready(d.New(store)) {
					t.Error("saved session was not restored from the store")
				}
				// Unreadable state must not be fatal: the provider comes up
				// unauthenticated so the user can log in again.
				if llm.Ready(d.New(memStore{"tokens": "not json"})) {
					t.Error("provider reports ready on unreadable saved state")
				}
			}
		})
	}
}

func storeHasValue(store memStore, want string) bool {
	for _, v := range store {
		if v == want {
			return true
		}
	}
	return false
}

// Providers declare their own prices, so the cost tracker must find them
// without the core shipping a pricing table.
func TestShippedProvidersDeclarePricing(t *testing.T) {
	tracker := llm.NewCostTracker()
	entry := tracker.Track(&llm.Response{
		Model:        "claude-sonnet-4-20250514",
		InputTokens:  1_000_000,
		OutputTokens: 1_000_000,
	})
	// 3.0 per million in + 15.0 per million out, as before the move.
	if entry.CostUSD != 18.0 {
		t.Errorf("cost for one million tokens each way = %v, want 18", entry.CostUSD)
	}

	// A model nobody declared is counted as free rather than guessed at.
	free := tracker.Track(&llm.Response{Model: "unregistered-model", InputTokens: 1_000_000})
	if free.CostUSD != 0 {
		t.Errorf("cost for an unpriced model = %v, want 0", free.CostUSD)
	}
}
