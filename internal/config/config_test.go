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

// A provider plugin is configuration, not code: adding one is a write to the
// configuration file, which is what lets a provider arrive without a rebuild.
func TestProviderPluginRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &Config{path: path}

	if _, ok := cfg.ProviderPlugin("acme"); ok {
		t.Error("ProviderPlugin on an empty config = found, want not found")
	}

	plugin := ProviderPluginConfig{
		Name:        "acme",
		Command:     "openuai-provider-acme",
		Args:        []string{"--serve"},
		Env:         map[string]string{"ACME_REGION": "eu"},
		Description: json.RawMessage(`{"name":"acme"}`),
	}
	if err := cfg.SetProviderPlugin(plugin); err != nil {
		t.Fatalf("SetProviderPlugin() = %v, want nil", err)
	}

	got, ok := cfg.ProviderPlugin("acme")
	if !ok {
		t.Fatal("ProviderPlugin(acme) = not found after adding it")
	}
	if got.Command != "openuai-provider-acme" || len(got.Args) != 1 || got.Env["ACME_REGION"] != "eu" {
		t.Errorf("stored plugin = %+v", got)
	}
	if string(got.Description) != `{"name":"acme"}` {
		t.Errorf("cached description = %s", got.Description)
	}

	// Adding the same name again replaces it rather than duplicating it.
	plugin.Command = "openuai-provider-acme-v2"
	if err := cfg.SetProviderPlugin(plugin); err != nil {
		t.Fatalf("SetProviderPlugin() on replace = %v, want nil", err)
	}
	if len(cfg.ProviderPlugins) != 1 {
		t.Fatalf("plugins = %d, want 1 after replacing", len(cfg.ProviderPlugins))
	}
	if cfg.ProviderPlugins[0].Command != "openuai-provider-acme-v2" {
		t.Errorf("plugin was not replaced: %+v", cfg.ProviderPlugins[0])
	}

	// It survives a reload: the plugin is available again at next start
	// without being probed.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("SetProviderPlugin() did not write the config: %v", err)
	}
	reloaded := &Config{path: path}
	if err := json.Unmarshal(data, reloaded); err != nil {
		t.Fatalf("saved config is not valid JSON: %v", err)
	}
	if _, ok := reloaded.ProviderPlugin("acme"); !ok {
		t.Error("reloaded config lost the plugin")
	}
}

// Removing a plugin must also drop whatever the core was holding for it, so a
// later plugin of the same name does not inherit stale credentials.
func TestRemoveProviderPluginDropsItsCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &Config{path: path}

	if err := cfg.SetProviderPlugin(ProviderPluginConfig{Name: "acme", Command: "acme"}); err != nil {
		t.Fatalf("SetProviderPlugin() = %v", err)
	}
	if err := cfg.ProviderStore("acme").Set("api_key", "acme-key"); err != nil {
		t.Fatalf("Set() = %v", err)
	}
	if err := cfg.SetProviderPlugin(ProviderPluginConfig{Name: "other", Command: "other"}); err != nil {
		t.Fatalf("SetProviderPlugin() = %v", err)
	}
	if err := cfg.ProviderStore("other").Set("api_key", "other-key"); err != nil {
		t.Fatalf("Set() = %v", err)
	}

	if err := cfg.RemoveProviderPlugin("acme"); err != nil {
		t.Fatalf("RemoveProviderPlugin() = %v, want nil", err)
	}
	if _, ok := cfg.ProviderPlugin("acme"); ok {
		t.Error("the plugin is still configured after removal")
	}
	if got := cfg.ProviderStore("acme").Get("api_key"); got != "" {
		t.Errorf("credentials survived removal: %q", got)
	}
	// The other plugin is untouched.
	if got := cfg.ProviderStore("other").Get("api_key"); got != "other-key" {
		t.Errorf("removal disturbed another plugin: %q", got)
	}
	if _, ok := cfg.ProviderPlugin("other"); !ok {
		t.Error("removal dropped the wrong plugin")
	}

	// Removing something that is not there is not an error.
	if err := cfg.RemoveProviderPlugin("never-existed"); err != nil {
		t.Errorf("RemoveProviderPlugin(unknown) = %v, want nil", err)
	}
}
