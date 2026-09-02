package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// loadFrom writes body to a temporary config.json and loads it, standing in
// for Load() without touching the real configuration directory.
func loadFrom(t *testing.T, body string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	cfg := &Config{path: path}
	if err := json.Unmarshal(data, cfg); err != nil {
		t.Fatalf("parsing config: %v", err)
	}
	cfg.migrateProviderCredentials()
	return cfg
}

// An installation written before the registry keeps its Claude API key and its
// OpenAI OAuth session: losing either would log the user out on upgrade.
func TestMigratesPreRegistryCredentials(t *testing.T) {
	cfg := loadFrom(t, `{
		"provider": "openai",
		"default_model": "gpt-5.1-codex",
		"claude_api_key": "sk-ant-legacy",
		"openai_tokens": {
			"access_token": "at-legacy",
			"refresh_token": "rt-legacy",
			"expires_at": 1234567890,
			"account_id": "acct-legacy"
		}
	}`)

	if got := cfg.ProviderStore("claude").Get("api_key"); got != "sk-ant-legacy" {
		t.Errorf("claude api_key = %q, want sk-ant-legacy", got)
	}

	raw := cfg.ProviderStore("openai").Get("tokens")
	if raw == "" {
		t.Fatal("openai tokens = empty, want the migrated session")
	}
	var tokens OAuthTokens
	if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
		t.Fatalf("migrated tokens are not valid JSON: %v", err)
	}
	if tokens.AccessToken != "at-legacy" || tokens.RefreshToken != "rt-legacy" {
		t.Errorf("migrated tokens = %+v, want the legacy pair", tokens)
	}
	if tokens.ExpiresAt != 1234567890 || tokens.AccountID != "acct-legacy" {
		t.Errorf("migrated tokens lost fields: %+v", tokens)
	}

	// The legacy fields are cleared, so the next Save writes only the
	// current shape.
	if cfg.ClaudeAPIKey != "" || cfg.OpenAITokens != nil {
		t.Errorf("legacy fields still set: key=%q tokens=%v", cfg.ClaudeAPIKey, cfg.OpenAITokens)
	}
	if !containsProviders(t, cfg) {
		t.Error("saved config does not carry the providers section")
	}

	// Everything else survives untouched.
	if cfg.Provider != "openai" || cfg.DefaultModel != "gpt-5.1-codex" {
		t.Errorf("provider/model = %q/%q, want openai/gpt-5.1-codex", cfg.Provider, cfg.DefaultModel)
	}
}

// Migration must not clobber a credential already stored in the new shape.
func TestMigrationDoesNotOverwriteExistingValues(t *testing.T) {
	cfg := loadFrom(t, `{
		"claude_api_key": "sk-ant-legacy",
		"providers": {"claude": {"api_key": "sk-ant-current"}}
	}`)

	if got := cfg.ProviderStore("claude").Get("api_key"); got != "sk-ant-current" {
		t.Errorf("claude api_key = %q, want the value already stored", got)
	}
}

// A configuration with nothing to migrate comes back untouched, and running
// migration twice changes nothing.
func TestMigrationIsIdempotent(t *testing.T) {
	cfg := loadFrom(t, `{"provider": "claude", "providers": {"claude": {"api_key": "sk-ant"}}}`)
	cfg.migrateProviderCredentials()

	if got := cfg.ProviderStore("claude").Get("api_key"); got != "sk-ant" {
		t.Errorf("claude api_key = %q, want sk-ant", got)
	}
	if cfg.ClaudeAPIKey != "" || cfg.OpenAITokens != nil {
		t.Error("migration reintroduced the legacy fields")
	}
}

// A fresh configuration names no provider: which one to start on is the
// registry's business, not this package's.
func TestFreshConfigNamesNoProvider(t *testing.T) {
	cfg := loadFrom(t, `{}`)
	if cfg.Provider != "" || cfg.DefaultModel != "" {
		t.Errorf("fresh config = provider %q, model %q; want both empty", cfg.Provider, cfg.DefaultModel)
	}
}

// Each provider sees only its own slot, and Set persists immediately so a
// refreshed OAuth token survives a restart.
func TestProviderStoreIsolatesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &Config{path: path}

	claude := cfg.ProviderStore("claude")
	other := cfg.ProviderStore("elsewhere")

	if err := claude.Set("api_key", "sk-ant"); err != nil {
		t.Fatalf("Set() = %v, want nil", err)
	}
	if got := other.Get("api_key"); got != "" {
		t.Errorf("elsewhere sees %q, want its own empty slot", got)
	}
	if got := claude.Get("missing"); got != "" {
		t.Errorf("Get(missing) = %q, want empty", got)
	}

	// Reload from disk: the write was persisted, under the new shape.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Set() did not write the config: %v", err)
	}
	reloaded := &Config{path: path}
	if err := json.Unmarshal(data, reloaded); err != nil {
		t.Fatalf("saved config is not valid JSON: %v", err)
	}
	if got := reloaded.ProviderStore("claude").Get("api_key"); got != "sk-ant" {
		t.Errorf("reloaded claude api_key = %q, want sk-ant", got)
	}
}

func containsProviders(t *testing.T, cfg *Config) bool {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshalling config: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("re-parsing config: %v", err)
	}
	_, ok := out["providers"]
	return ok
}
