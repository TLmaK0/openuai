package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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
	if cfg.data.ClaudeAPIKey != "" || cfg.data.OpenAITokens != nil {
		t.Errorf("legacy fields still set: key=%q tokens=%v", cfg.data.ClaudeAPIKey, cfg.data.OpenAITokens)
	}
	if !containsProviders(t, cfg) {
		t.Error("saved config does not carry the providers section")
	}

	// Everything else survives untouched.
	if provider, model := cfg.ProviderAndModel(); provider != "openai" || model != "gpt-5.1-codex" {
		t.Errorf("provider/model = %q/%q, want openai/gpt-5.1-codex", provider, model)
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
	if cfg.data.ClaudeAPIKey != "" || cfg.data.OpenAITokens != nil {
		t.Error("migration reintroduced the legacy fields")
	}
}

// A fresh configuration names no provider: which one to start on is the
// registry's business, not this package's.
func TestFreshConfigNamesNoProvider(t *testing.T) {
	cfg := loadFrom(t, `{}`)
	if provider, model := cfg.ProviderAndModel(); provider != "" || model != "" {
		t.Errorf("fresh config = provider %q, model %q; want both empty", provider, model)
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
	if plugins := cfg.ProviderPluginList(); len(plugins) != 1 {
		t.Fatalf("plugins = %d, want 1 after replacing", len(plugins))
	} else if plugins[0].Command != "openuai-provider-acme-v2" {
		t.Errorf("plugin was not replaced: %+v", plugins[0])
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

// Save() marshals ProviderPlugins, and a plugin can be added or removed while
// an agent turn triggers a save — an OAuth refresh persists through the same
// Config. Reads, writes and Save must therefore agree on one lock.
func TestProviderPluginsAreSafeUnderConcurrentUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &Config{path: path}

	const rounds = 100
	var wg sync.WaitGroup

	// A plugin being added and removed, as the settings screen does.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			cfg.SetProviderPlugin(ProviderPluginConfig{Name: "churn", Command: "churn"})
			cfg.RemoveProviderPlugin("churn")
		}
	}()

	// A credential being persisted, as an OAuth refresh does mid-turn: this
	// goes through Save(), which marshals the whole struct.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			cfg.ProviderStore("openai").Set("tokens", "{}")
		}
	}()

	// And the core reading the list to build its providers.
	for r := 0; r < 2; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				for _, p := range cfg.ProviderPluginList() {
					_ = p.Name
				}
				cfg.ProviderPlugin("churn")
			}
		}()
	}

	wg.Wait()

	// The credential written all along survived the churn.
	if got := cfg.ProviderStore("openai").Get("tokens"); got != "{}" {
		t.Errorf("openai tokens = %q, want {}", got)
	}
}

// The settings screen writes on the goroutine Wails gave it, and an agent
// turn reads on its own, so every value in the struct is written and read at
// once. The measured example is Provider and DefaultModel, which are strings:
// unguarded, a reader can pair one write's pointer with another's length and
// come back with a provider that was never configured.
func TestSettingsAreSafeUnderConcurrentUse(t *testing.T) {
	cfg := &Config{path: filepath.Join(t.TempDir(), "config.json")}

	// The pairs the settings screen chooses between. A reader must observe
	// one of these, never a mixture.
	pairs := [][2]string{
		{"claude", "claude-opus-4-6"},
		{"openai", "gpt-5.1-codex"},
		{"a-plugin-with-a-much-longer-name", "a-model-with-a-much-longer-name"},
	}
	want := map[string]string{}
	for _, p := range pairs {
		want[p[0]] = p[1]
	}

	const rounds = 500
	var wg sync.WaitGroup

	// The settings screen, changing everything a user can change.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			p := pairs[i%len(pairs)]
			cfg.SetProviderAndModel(p[0], p[1])
			cfg.SetWakeWord("Pepito")
			cfg.SetWatchedChats([]string{"one@s.whatsapp.net", "two@s.whatsapp.net"})
			cfg.SetVoiceEnabled(i%2 == 0)
			cfg.SetComputerUseMonitor(i % 3)
			cfg.SetTTSVoice("alloy")
			cfg.SetAudioDevice("default")
			cfg.SetSTTLanguage("es")
		}
	}()

	// An agent turn, reading what it needs to run.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				provider, model := cfg.ProviderAndModel()
				if provider != "" && want[provider] != model {
					t.Errorf("read provider %q with model %q, a pair never configured", provider, model)
					return
				}
				_ = cfg.Provider()
				_ = cfg.DefaultModel()
				_ = cfg.WakeWord()
				_ = cfg.VoiceEnabled()
				_ = cfg.ComputerUseMonitor()
				_ = cfg.TTSVoice()
				_ = cfg.AudioDevice()
				_ = cfg.STTLanguage()
				for _, jid := range cfg.WatchedChats() {
					_ = jid
				}
			}
		}()
	}

	// And a save landing mid-change, as an OAuth refresh does: Save marshals
	// the whole struct, so it reads every value the settings screen writes.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if err := cfg.Save(); err != nil {
				t.Errorf("Save() = %v, want nil", err)
				return
			}
		}
	}()

	wg.Wait()
}

// Save marshals the entire struct, so it is a reader of every value. What it
// writes must be a state that existed: a document naming one write's provider
// beside another's model would be a configuration nobody chose, and it would
// survive the restart that reads it back.
func TestSaveNeverMarshalsAHalfUpdatedConfig(t *testing.T) {
	cfg := &Config{path: filepath.Join(t.TempDir(), "config.json")}

	pairs := [][2]string{
		{"claude", "claude-opus-4-6"},
		{"openai", "gpt-5.1-codex"},
	}

	const rounds = 500
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			p := pairs[i%len(pairs)]
			cfg.SetProviderAndModel(p[0], p[1])
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			// MarshalJSON is what Save writes out; going through it directly
			// keeps the test on the marshalling rather than on the file.
			data, err := cfg.MarshalJSON()
			if err != nil {
				t.Errorf("MarshalJSON() = %v, want nil", err)
				return
			}
			var saved struct {
				Provider     string `json:"provider"`
				DefaultModel string `json:"default_model"`
			}
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Errorf("the saved config does not parse: %v", err)
				return
			}
			for _, p := range pairs {
				if saved.Provider == p[0] {
					if saved.DefaultModel != p[1] {
						t.Errorf("saved provider %q with model %q, want %q", saved.Provider, saved.DefaultModel, p[1])
						return
					}
					break
				}
			}
		}
	}()

	wg.Wait()
}

// Moving the values behind accessors moved their json tags with them, and a
// tag lost in the move would drop a setting silently — the field would simply
// stop being written, and read back as its zero value. So every accessor is
// checked against the key it is stored under, in both directions.
func TestEverySettingRoundTrips(t *testing.T) {
	cfg := loadFrom(t, `{
		"provider": "openai",
		"default_model": "gpt-5.1-codex",
		"mcp_servers": [{"name": "wa", "command": "mcp-whatsapp", "auto_start": true}],
		"watched_chats": ["one@s.whatsapp.net"],
		"max_concurrent_agents": 7,
		"notifications_enabled": false,
		"api_enabled": true,
		"api_port": 9120,
		"voice_enabled": false,
		"tts_voice": "alloy",
		"stt_model": "small",
		"stt_language": "es",
		"wake_word": "Pepito",
		"audio_device": "default",
		"skipped_version": "v0.4.9",
		"beta_lip_reading": true,
		"computer_use_enabled": true,
		"computer_use_display": ":99",
		"computer_use_monitor": 2,
		"computer_use_profile": "/tmp/chrome-profile"
	}`)

	check := func(name string, got, want any) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}

	// Read what was on disk.
	assertAll := func(cfg *Config) {
		t.Helper()
		provider, model := cfg.ProviderAndModel()
		check("Provider()", provider, "openai")
		check("DefaultModel()", model, "gpt-5.1-codex")
		check("MaxConcurrentAgents()", cfg.MaxConcurrentAgents(), 7)
		check("NotificationsEnabled()", cfg.NotificationsEnabled(), false)
		check("APIEnabled()", cfg.APIEnabled(), true)
		check("APIPort()", cfg.APIPort(), 9120)
		check("VoiceEnabled()", cfg.VoiceEnabled(), false)
		check("TTSVoice()", cfg.TTSVoice(), "alloy")
		check("STTModel()", cfg.STTModel(), "small")
		check("STTLanguage()", cfg.STTLanguage(), "es")
		check("WakeWord()", cfg.WakeWord(), "Pepito")
		check("AudioDevice()", cfg.AudioDevice(), "default")
		check("SkippedVersion()", cfg.SkippedVersion(), "v0.4.9")
		check("BetaLipReading()", cfg.BetaLipReading(), true)
		check("ComputerUseEnabled()", cfg.ComputerUseEnabled(), true)
		check("ComputerUseDisplay()", cfg.ComputerUseDisplay(), ":99")
		check("ComputerUseMonitor()", cfg.ComputerUseMonitor(), 2)
		check("ComputerUseProfile()", cfg.ComputerUseProfile(), "/tmp/chrome-profile")

		chats := cfg.WatchedChats()
		if len(chats) != 1 || chats[0] != "one@s.whatsapp.net" {
			t.Errorf("WatchedChats() = %v, want one JID", chats)
		}
		servers := cfg.MCPServers()
		if len(servers) != 1 || servers[0].Name != "wa" || !servers[0].AutoStart {
			t.Errorf("MCPServers() = %+v, want the one auto-starting server", servers)
		}
	}
	assertAll(cfg)

	// Save it and read it back: nothing may be lost on the way through disk.
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() = %v, want nil", err)
	}
	data, err := os.ReadFile(cfg.Path())
	if err != nil {
		t.Fatalf("reading the saved config: %v", err)
	}
	reloaded := &Config{}
	if err := json.Unmarshal(data, reloaded); err != nil {
		t.Fatalf("parsing the saved config: %v", err)
	}
	assertAll(reloaded)
}

// The default for a setting stored as a pointer lives in its accessor, so
// that no caller has to know the value is optional — and so that no accessor
// hands out the pointer itself, which would be a way to write past the lock.
func TestPointerSettingsDefaultWhenUnset(t *testing.T) {
	cfg := loadFrom(t, `{}`)

	if !cfg.NotificationsEnabled() {
		t.Error("NotificationsEnabled() = false on a fresh config, want true")
	}
	if !cfg.VoiceEnabled() {
		t.Error("VoiceEnabled() = false on a fresh config, want true")
	}
	if got := cfg.ComputerUseMonitor(); got != 0 {
		t.Errorf("ComputerUseMonitor() = %d on a fresh config, want 0 (primary)", got)
	}

	// Turning one off is not the same as leaving it unset.
	cfg.SetNotificationsEnabled(false)
	if cfg.NotificationsEnabled() {
		t.Error("NotificationsEnabled() = true after being turned off")
	}
}

// Choosing the active provider is a read-modify-write: read the configured
// pair, decide whether it still names something usable, write the decision.
// Split into a read and a later write, a settings write lands in the gap and
// is then overwritten by a decision made from the older snapshot — the user
// changes provider and watches it undo itself, on disk too, because the
// caller persists the settled choice.
//
// So the settle has to be one critical section. This pins that: the settings
// write is released while the settle is mid-flight, and must not take effect
// until the settle has finished, which leaves the user's choice standing.
func TestSettleProviderAndModelIsOneCriticalSection(t *testing.T) {
	cfg := &Config{path: filepath.Join(t.TempDir(), "config.json")}
	cfg.SetProviderAndModel("stale", "stale-model")

	inSettle := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-inSettle
		// The settings screen, writing while the settle holds the lock.
		cfg.SetProviderAndModel("chosen", "chosen-model")
	}()

	settled, _ := cfg.SettleProviderAndModel(func(provider, model string) (string, string) {
		close(inSettle)
		// Give the writer every chance to interleave. Under a split
		// read-modify-write it would, and the write below would bury it.
		time.Sleep(50 * time.Millisecond)
		return "settled", "settled-model"
	})
	wg.Wait()

	if settled != "settled" {
		t.Errorf("SettleProviderAndModel() = %q, want the settled name", settled)
	}
	provider, model := cfg.ProviderAndModel()
	if provider != "chosen" || model != "chosen-model" {
		t.Errorf("provider/model = %q/%q, want chosen/chosen-model: the settings write was lost to the settle", provider, model)
	}
}

// Settling on what is already configured leaves the stored pair alone: the
// common path, where the configured provider is registered and has a model.
//
// This pins the value, not the absence of a write. Under one hold of the write
// lock the two are indistinguishable from outside, so this passes against a
// split read-modify-write as well — it documents the common path, and nothing
// here guards the conditional in SettleProviderAndModel.
func TestSettleLeavesAnUnchangedPairAlone(t *testing.T) {
	cfg := &Config{path: filepath.Join(t.TempDir(), "config.json")}
	cfg.SetProviderAndModel("claude", "claude-opus-4-6")

	provider, model := cfg.SettleProviderAndModel(func(provider, model string) (string, string) {
		return provider, model // registered, and it has a model: nothing to do
	})
	if provider != "claude" || model != "claude-opus-4-6" {
		t.Errorf("settle returned %q/%q, want the configured pair unchanged", provider, model)
	}
	if provider, model = cfg.ProviderAndModel(); provider != "claude" || model != "claude-opus-4-6" {
		t.Errorf("stored pair = %q/%q, want it untouched", provider, model)
	}
}

// A settle running against concurrent settings writes must never produce a
// pair that was not configured, and must never be handed a torn one.
func TestSettleIsSafeUnderConcurrentUse(t *testing.T) {
	cfg := &Config{path: filepath.Join(t.TempDir(), "config.json")}

	// The registered providers, each with its default model — the shape
	// buildProviders settles against.
	defaultModels := map[string]string{
		"claude": "claude-opus-4-6",
		"openai": "gpt-5.1-codex",
	}
	cfg.SetProviderAndModel("claude", "claude-opus-4-6")

	const rounds = 500
	var wg sync.WaitGroup

	// The settings screen.
	wg.Add(1)
	go func() {
		defer wg.Done()
		names := []string{"claude", "openai", "not-registered"}
		for i := 0; i < rounds; i++ {
			name := names[i%len(names)]
			cfg.SetProviderAndModel(name, defaultModels[name])
		}
	}()

	// Providers being rebuilt, as adding or removing a plugin does.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			provider, model := cfg.SettleProviderAndModel(func(provider, model string) (string, string) {
				if _, known := defaultModels[provider]; !known {
					provider, model = "claude", ""
				}
				if model == "" {
					model = defaultModels[provider]
				}
				return provider, model
			})
			// Whatever it settled on must be a registered provider paired
			// with that provider's own model.
			if want, ok := defaultModels[provider]; !ok || model != want {
				t.Errorf("settled on %q/%q, which is not a registered pair", provider, model)
				return
			}
		}
	}()

	// And a turn reading the pair it is about to run with.
	for r := 0; r < 2; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				provider, model := cfg.ProviderAndModel()
				if want, ok := defaultModels[provider]; ok && model != want {
					t.Errorf("read %q/%q, a pair never configured", provider, model)
					return
				}
			}
		}()
	}

	wg.Wait()
}

// The three settings stored as pointers carry three states, not two: never
// set, set on, set off. Only the pointer keeps them apart — with a plain bool
// and omitempty, "off" would be omitted and read back as the default "on", so
// turning notifications off would survive until the next restart and then
// undo itself.
//
// The accessors return a value with the default applied, which is what stops
// a caller writing past the lock through the pointer. This pins that the
// three states still make it through disk intact behind them.
func TestPointerSettingsKeepThreeStatesThroughDisk(t *testing.T) {
	saveAndReload := func(t *testing.T, cfg *Config) *Config {
		t.Helper()
		if err := cfg.Save(); err != nil {
			t.Fatalf("Save() = %v, want nil", err)
		}
		data, err := os.ReadFile(cfg.Path())
		if err != nil {
			t.Fatalf("reading the saved config: %v", err)
		}
		reloaded := &Config{}
		if err := json.Unmarshal(data, reloaded); err != nil {
			t.Fatalf("parsing the saved config: %v", err)
		}
		return reloaded
	}

	// Never set: the keys are absent, and the accessors answer the default.
	fresh := &Config{path: filepath.Join(t.TempDir(), "config.json")}
	reloaded := saveAndReload(t, fresh)
	for _, k := range []string{"notifications_enabled", "voice_enabled", "computer_use_monitor"} {
		data, err := os.ReadFile(fresh.Path())
		if err != nil {
			t.Fatalf("reading the saved config: %v", err)
		}
		if bytes.Contains(data, []byte(k)) {
			t.Errorf("a config that never set %s wrote the key anyway: %s", k, data)
		}
	}
	if !reloaded.NotificationsEnabled() || !reloaded.VoiceEnabled() {
		t.Error("unset notifications/voice came back off, want on by default")
	}
	if got := reloaded.ComputerUseMonitor(); got != 0 {
		t.Errorf("unset monitor = %d, want 0", got)
	}

	// Set off: the keys are written, and "off" is not mistaken for unset on
	// the way back — the regression a plain bool would introduce.
	off := &Config{path: filepath.Join(t.TempDir(), "config.json")}
	off.SetNotificationsEnabled(false)
	off.SetVoiceEnabled(false)
	off.SetComputerUseMonitor(0)
	reloaded = saveAndReload(t, off)
	if reloaded.NotificationsEnabled() {
		t.Error("notifications turned off came back on: off was written as unset")
	}
	if reloaded.VoiceEnabled() {
		t.Error("voice turned off came back on: off was written as unset")
	}
	if got := reloaded.ComputerUseMonitor(); got != 0 {
		t.Errorf("monitor set to 0 came back %d", got)
	}

	// Set on, and a monitor that is not the default: both round-trip.
	on := &Config{path: filepath.Join(t.TempDir(), "config.json")}
	on.SetNotificationsEnabled(true)
	on.SetVoiceEnabled(true)
	on.SetComputerUseMonitor(-1) // the whole desktop
	reloaded = saveAndReload(t, on)
	if !reloaded.NotificationsEnabled() || !reloaded.VoiceEnabled() {
		t.Error("notifications/voice turned on came back off")
	}
	if got := reloaded.ComputerUseMonitor(); got != -1 {
		t.Errorf("monitor set to -1 came back %d, want the whole desktop preserved", got)
	}
}
