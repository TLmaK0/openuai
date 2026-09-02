package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type OAuthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	AccountID    string `json:"account_id"`
}

// MCPServerConfig configures a single MCP server connection.
type MCPServerConfig struct {
	Name      string            `json:"name"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	AutoStart bool              `json:"auto_start"`
	Subscribe []string          `json:"subscribe,omitempty"`
	// URL is the HTTP endpoint for remote MCP servers (mutually exclusive with Command).
	URL string `json:"url,omitempty"`
	// OAuth tokens for HTTP MCP servers (persisted across restarts).
	OAuthTokens *MCPOAuthTokens `json:"oauth_tokens,omitempty"`
}

// IsHTTP returns true if this server connects via HTTP (may also have Command for auto-start).
func (c MCPServerConfig) IsHTTP() bool {
	return c.URL != ""
}

// NeedsLaunch returns true if a subprocess should be started before connecting.
func (c MCPServerConfig) NeedsLaunch() bool {
	return c.Command != "" && c.URL != ""
}

// MCPOAuthTokens stores OAuth tokens for an HTTP MCP server.
type MCPOAuthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
}

// ProviderPluginConfig configures a model provider that runs as a separate
// executable, spoken to over stdin/stdout. Adding one needs no rebuild of the
// core.
type ProviderPluginConfig struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// Description caches what the plugin answered when it was added, so the
	// provider list can be built at startup without running every plugin.
	// Its shape belongs to the plugin protocol, not to this package.
	Description json.RawMessage `json:"description,omitempty"`
}

type Config struct {
	Provider     string `json:"provider"`
	DefaultModel string `json:"default_model"`
	// Providers holds each provider's own credentials and settings, keyed by
	// provider name. The core declares no provider-specific fields: a
	// provider reaches its slot through ProviderStore.
	Providers map[string]map[string]string `json:"providers,omitempty"`
	// ProviderPlugins are the providers that run as separate executables.
	ProviderPlugins []ProviderPluginConfig `json:"provider_plugins,omitempty"`
	// ClaudeAPIKey and OpenAITokens are the pre-registry shape. They are
	// still read, migrated into Providers by Load, and then dropped.
	ClaudeAPIKey         string            `json:"claude_api_key,omitempty"`
	OpenAITokens         *OAuthTokens      `json:"openai_tokens,omitempty"`
	MCPServers           []MCPServerConfig `json:"mcp_servers,omitempty"`
	WatchedChats         []string          `json:"watched_chats,omitempty"`
	MaxConcurrentAgents  int               `json:"max_concurrent_agents,omitempty"`
	NotificationsEnabled *bool             `json:"notifications_enabled,omitempty"`
	APIEnabled           bool              `json:"api_enabled,omitempty"`
	APIPort              int               `json:"api_port,omitempty"`
	VoiceEnabled         *bool             `json:"voice_enabled,omitempty"`
	TTSVoice             string            `json:"tts_voice,omitempty"`
	STTModel             string            `json:"stt_model,omitempty"`
	STTLanguage          string            `json:"stt_language,omitempty"`
	WakeWord             string            `json:"wake_word,omitempty"` // name that triggers hands-free listening (e.g. "Pepito")
	AudioDevice          string            `json:"audio_device,omitempty"`
	SkippedVersion       string            `json:"skipped_version,omitempty"`
	BetaLipReading       bool              `json:"beta_lip_reading,omitempty"`
	ComputerUseEnabled   bool              `json:"computer_use_enabled,omitempty"`
	ComputerUseDisplay   string            `json:"computer_use_display,omitempty"` // X display, e.g. ":0" (screen) or ":99" (virtual)
	ComputerUseMonitor   *int              `json:"computer_use_monitor,omitempty"` // monitor index (xrandr); nil = primary (0), -1 = whole desktop
	ComputerUseProfile   string            `json:"computer_use_profile,omitempty"` // chrome user-data-dir; empty = the user's own profile/session
	path                 string

	// providersMu guards Providers, which providers write to from their own
	// goroutines (an OAuth refresh lands mid-request).
	providersMu sync.Mutex
}

func Load() (*Config, error) {
	configDir, err := configDir()
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, err
	}

	path := filepath.Join(configDir, "config.json")
	// Provider and DefaultModel are deliberately left empty: which provider
	// to start with is the registry's business, not this package's.
	cfg := &Config{path: path}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	cfg.path = path
	cfg.migrateProviderCredentials()
	return cfg, nil
}

// migrateProviderCredentials moves credentials written in the pre-registry
// shape into the per-provider store, so an existing installation keeps its
// API key and its OAuth session. Already-migrated values win, and the legacy
// fields are cleared so the next Save writes only the current shape.
func (c *Config) migrateProviderCredentials() {
	if c.ClaudeAPIKey != "" {
		if c.providerValue("claude", "api_key") == "" {
			c.setProviderValue("claude", "api_key", c.ClaudeAPIKey)
		}
		c.ClaudeAPIKey = ""
	}

	if c.OpenAITokens != nil {
		if c.providerValue("openai", "tokens") == "" {
			if data, err := json.Marshal(c.OpenAITokens); err == nil {
				c.setProviderValue("openai", "tokens", string(data))
			}
		}
		c.OpenAITokens = nil
	}
}

func (c *Config) providerValue(provider, key string) string {
	c.providersMu.Lock()
	defer c.providersMu.Unlock()
	return c.Providers[provider][key]
}

func (c *Config) setProviderValue(provider, key, value string) {
	c.providersMu.Lock()
	defer c.providersMu.Unlock()
	if c.Providers == nil {
		c.Providers = map[string]map[string]string{}
	}
	if c.Providers[provider] == nil {
		c.Providers[provider] = map[string]string{}
	}
	c.Providers[provider][key] = value
}

// ProviderPlugin returns the configuration of the named plugin. It is read
// under the same lock Save() holds, because Save marshals the slice.
func (c *Config) ProviderPlugin(name string) (ProviderPluginConfig, bool) {
	c.providersMu.Lock()
	defer c.providersMu.Unlock()
	for _, p := range c.ProviderPlugins {
		if p.Name == name {
			return p, true
		}
	}
	return ProviderPluginConfig{}, false
}

// ProviderPluginList returns a copy of the configured plugins, so a caller
// cannot iterate the slice while another goroutine replaces it.
func (c *Config) ProviderPluginList() []ProviderPluginConfig {
	c.providersMu.Lock()
	defer c.providersMu.Unlock()
	out := make([]ProviderPluginConfig, len(c.ProviderPlugins))
	copy(out, c.ProviderPlugins)
	return out
}

// SetProviderPlugin adds or replaces a plugin's configuration and persists it.
func (c *Config) SetProviderPlugin(plugin ProviderPluginConfig) error {
	c.providersMu.Lock()
	replaced := false
	for i, p := range c.ProviderPlugins {
		if p.Name == plugin.Name {
			c.ProviderPlugins[i] = plugin
			replaced = true
			break
		}
	}
	if !replaced {
		c.ProviderPlugins = append(c.ProviderPlugins, plugin)
	}
	c.providersMu.Unlock()

	// Save takes the same lock, so it is called after releasing it.
	return c.Save()
}

// RemoveProviderPlugin drops a plugin's configuration, along with any
// credentials the core was holding for it, and persists the result.
func (c *Config) RemoveProviderPlugin(name string) error {
	c.providersMu.Lock()
	kept := make([]ProviderPluginConfig, 0, len(c.ProviderPlugins))
	for _, p := range c.ProviderPlugins {
		if p.Name != name {
			kept = append(kept, p)
		}
	}
	c.ProviderPlugins = kept
	delete(c.Providers, name)
	c.providersMu.Unlock()

	return c.Save()
}

// ProviderStore returns the settings slot belonging to one provider.
func (c *Config) ProviderStore(provider string) *ProviderStore {
	return &ProviderStore{cfg: c, provider: provider}
}

// ProviderStore is one provider's view of the configuration: its own
// credentials and settings, and nothing else.
type ProviderStore struct {
	cfg      *Config
	provider string
}

func (s *ProviderStore) Get(key string) string {
	return s.cfg.providerValue(s.provider, key)
}

// Set stores a value and persists the configuration immediately.
func (s *ProviderStore) Set(key, value string) error {
	s.cfg.setProviderValue(s.provider, key, value)
	return s.cfg.Save()
}

func (c *Config) ConfigDir() string {
	return filepath.Dir(c.path)
}

func (c *Config) Save() error {
	c.providersMu.Lock()
	data, err := json.MarshalIndent(c, "", "  ")
	c.providersMu.Unlock()
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, data, 0o600)
}

func configDir() (string, error) {
	home, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "openuai"), nil
}
