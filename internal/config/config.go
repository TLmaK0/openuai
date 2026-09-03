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

// settings is what gets written to disk. Its fields are exported so that
// encoding/json can see them; the type itself is not, so no caller outside
// this package can name it and reach a field without holding Config's lock.
type settings struct {
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
}

// Config is the configuration, and it is shared mutable state: its values are
// written from Wails methods, each of which runs on its own goroutine, and
// read from the goroutines that run agent turns. A settings write and a
// turn's read therefore overlap as a matter of course, not as an edge case.
//
// So every value lives in an unexported struct reached only through the
// accessors below, and one mutex guards all of it. There is no exported
// field to write past the guard, and no accessor that hands out a pointer
// into the struct: the pointer-valued settings are returned as values with
// their default already applied. Save() holds the same lock while
// marshalling, so it cannot see the struct half-updated.
//
// One lock for the whole struct rather than one per value is deliberate. A
// guard on some values would leave the rest looking guarded, and the next
// reader would have no way to tell which is which.
//
// The other shape worth weighing was an owner goroutine, with reads and
// writes as messages. It was dropped because it turns every read — and reads
// here are frequent and on the path of a turn — into a channel round trip,
// to remove a lock discipline that no caller outside this package can even
// get at.
type Config struct {
	mu   sync.RWMutex
	data settings
	path string
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
	// No SetPath here: cfg was built with the path, and UnmarshalJSON writes
	// only c.data, so there is nothing to put back.
	cfg.migrateProviderCredentials()
	return cfg, nil
}

// MarshalJSON writes the persisted settings. It is what makes Save() a reader
// of every value: it takes the read lock for the whole struct, so a write
// landing mid-marshal cannot be half-included.
func (c *Config) MarshalJSON() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return json.Marshal(c.data)
}

// UnmarshalJSON reads the persisted settings, under the write lock so that
// loading over a live Config is no less safe than any other write.
func (c *Config) UnmarshalJSON(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return json.Unmarshal(data, &c.data)
}

// migrateProviderCredentials moves credentials written in the pre-registry
// shape into the per-provider store, so an existing installation keeps its
// API key and its OAuth session. Already-migrated values win, and the legacy
// fields are cleared so the next Save writes only the current shape.
func (c *Config) migrateProviderCredentials() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.data.ClaudeAPIKey != "" {
		if c.data.Providers["claude"]["api_key"] == "" {
			c.setProviderValueLocked("claude", "api_key", c.data.ClaudeAPIKey)
		}
		c.data.ClaudeAPIKey = ""
	}

	if c.data.OpenAITokens != nil {
		if c.data.Providers["openai"]["tokens"] == "" {
			if data, err := json.Marshal(c.data.OpenAITokens); err == nil {
				c.setProviderValueLocked("openai", "tokens", string(data))
			}
		}
		c.data.OpenAITokens = nil
	}
}

func (c *Config) providerValue(provider, key string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.Providers[provider][key]
}

func (c *Config) setProviderValue(provider, key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setProviderValueLocked(provider, key, value)
}

// setProviderValueLocked is the body of setProviderValue, for callers that
// already hold the lock.
func (c *Config) setProviderValueLocked(provider, key, value string) {
	if c.data.Providers == nil {
		c.data.Providers = map[string]map[string]string{}
	}
	if c.data.Providers[provider] == nil {
		c.data.Providers[provider] = map[string]string{}
	}
	c.data.Providers[provider][key] = value
}

// SetPath points the configuration at a file. It exists for callers that
// build a Config without Load, which otherwise has nowhere to save.
func (c *Config) SetPath(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.path = path
}

// ProviderPlugin returns the configuration of the named plugin.
func (c *Config) ProviderPlugin(name string) (ProviderPluginConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, p := range c.data.ProviderPlugins {
		if p.Name == name {
			return p, true
		}
	}
	return ProviderPluginConfig{}, false
}

// ProviderPluginList returns a copy of the configured plugins, so a caller
// cannot iterate the slice while another goroutine replaces it.
func (c *Config) ProviderPluginList() []ProviderPluginConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]ProviderPluginConfig, len(c.data.ProviderPlugins))
	copy(out, c.data.ProviderPlugins)
	return out
}

// SetProviderPlugin adds or replaces a plugin's configuration and persists it.
func (c *Config) SetProviderPlugin(plugin ProviderPluginConfig) error {
	c.mu.Lock()
	replaced := false
	for i, p := range c.data.ProviderPlugins {
		if p.Name == plugin.Name {
			c.data.ProviderPlugins[i] = plugin
			replaced = true
			break
		}
	}
	if !replaced {
		c.data.ProviderPlugins = append(c.data.ProviderPlugins, plugin)
	}
	c.mu.Unlock()

	// Save takes the same lock, so it is called after releasing it.
	return c.Save()
}

// RemoveProviderPlugin drops a plugin's configuration, along with any
// credentials the core was holding for it, and persists the result.
func (c *Config) RemoveProviderPlugin(name string) error {
	c.mu.Lock()
	kept := make([]ProviderPluginConfig, 0, len(c.data.ProviderPlugins))
	for _, p := range c.data.ProviderPlugins {
		if p.Name != name {
			kept = append(kept, p)
		}
	}
	c.data.ProviderPlugins = kept
	delete(c.data.Providers, name)
	c.mu.Unlock()

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

// --- Provider and model ---

func (c *Config) Provider() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.Provider
}

func (c *Config) DefaultModel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.DefaultModel
}

// ProviderAndModel returns both under one lock, for a caller that needs the
// two to belong together. Read separately they can straddle a write and come
// back as a pair that was never configured.
func (c *Config) ProviderAndModel() (provider, model string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.Provider, c.data.DefaultModel
}

// SetProviderAndModel stores both, which is how they are always set: the
// model belongs to the provider, so there is no setter for the provider
// alone that would leave the model naming a model it cannot serve.
func (c *Config) SetProviderAndModel(provider, model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.Provider = provider
	c.data.DefaultModel = model
}

// SetDefaultModel changes the model within the provider in use.
func (c *Config) SetDefaultModel(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.DefaultModel = model
}

// SettleProviderAndModel hands the stored pair to settle and keeps what comes
// back, all under one hold of the lock. It exists because choosing the active
// provider is a read-modify-write: read the configured pair, decide whether it
// still names something usable, write the decision. Split into a read and a
// later write, another goroutine's write lands in the gap and is then
// overwritten by a decision made from the older snapshot — a user changing
// provider in the settings screen would see the change undone, and persisted
// undone.
//
// The single critical section is what closes that: nothing can be read or
// written between the settle's read and its write. The assignment being
// conditional is hygiene rather than mechanism — under one hold of the write
// lock, assigning the same values and not assigning them cannot be told apart
// from outside — and is there so that settling on what is already configured
// leaves the stored pair alone.
//
// settle runs while the lock is held, so it MUST be a pure function of its two
// arguments: no call back into this Config, which would deadlock, and no I/O,
// which would hold the lock across it. Anything it needs from elsewhere is
// gathered before the call and captured.
func (c *Config) SettleProviderAndModel(settle func(provider, model string) (string, string)) (provider, model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	provider, model = settle(c.data.Provider, c.data.DefaultModel)
	if provider != c.data.Provider || model != c.data.DefaultModel {
		c.data.Provider, c.data.DefaultModel = provider, model
	}
	return provider, model
}

// --- MCP servers ---

// MCPServers returns a copy of the configured servers, so a caller cannot
// iterate the slice while another goroutine adds to or removes from it.
func (c *Config) MCPServers() []MCPServerConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]MCPServerConfig, len(c.data.MCPServers))
	copy(out, c.data.MCPServers)
	return out
}

// AddMCPServer appends a server, reporting false if one of that name is
// already configured. The check and the append are one step, so two
// concurrent adds of the same name cannot both find it absent.
func (c *Config) AddMCPServer(server MCPServerConfig) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.data.MCPServers {
		if s.Name == server.Name {
			return false
		}
	}
	c.data.MCPServers = append(c.data.MCPServers, server)
	return true
}

// RemoveMCPServer drops the named server, reporting whether it was there.
func (c *Config) RemoveMCPServer(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, s := range c.data.MCPServers {
		if s.Name == name {
			c.data.MCPServers = append(c.data.MCPServers[:i], c.data.MCPServers[i+1:]...)
			return true
		}
	}
	return false
}

// SetMCPServerTokens stores the named server's OAuth tokens — or, with nil,
// clears them — and returns the server as it now stands, so a caller can
// reconnect with it without a second lookup that a removal could invalidate.
func (c *Config) SetMCPServerTokens(name string, tokens *MCPOAuthTokens) (MCPServerConfig, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, s := range c.data.MCPServers {
		if s.Name == name {
			c.data.MCPServers[i].OAuthTokens = tokens
			return c.data.MCPServers[i], true
		}
	}
	return MCPServerConfig{}, false
}

// --- Chats and agents ---

// WatchedChats returns a copy of the watched chat JIDs.
func (c *Config) WatchedChats() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, len(c.data.WatchedChats))
	copy(out, c.data.WatchedChats)
	return out
}

// SetWatchedChats replaces the watched chats with a copy of jids, so the
// caller's slice stays the caller's.
func (c *Config) SetWatchedChats(jids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.WatchedChats = append([]string(nil), jids...)
}

func (c *Config) MaxConcurrentAgents() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.MaxConcurrentAgents
}

// --- Notifications and API ---

// NotificationsEnabled reports whether notifications are on, which they are
// unless they have been turned off. The default lives here because the
// stored value is a pointer, and returning that pointer would hand a caller
// a way to write past the lock.
func (c *Config) NotificationsEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.NotificationsEnabled == nil || *c.data.NotificationsEnabled
}

func (c *Config) SetNotificationsEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.NotificationsEnabled = &enabled
}

func (c *Config) APIEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.APIEnabled
}

func (c *Config) APIPort() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.APIPort
}

// --- Voice ---

// VoiceEnabled reports whether voice is on, which it is unless it has been
// turned off. See NotificationsEnabled on where the default lives.
func (c *Config) VoiceEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.VoiceEnabled == nil || *c.data.VoiceEnabled
}

func (c *Config) SetVoiceEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.VoiceEnabled = &enabled
}

func (c *Config) TTSVoice() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.TTSVoice
}

func (c *Config) SetTTSVoice(voice string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.TTSVoice = voice
}

func (c *Config) STTModel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.STTModel
}

func (c *Config) STTLanguage() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.STTLanguage
}

func (c *Config) SetSTTLanguage(lang string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.STTLanguage = lang
}

func (c *Config) WakeWord() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.WakeWord
}

func (c *Config) SetWakeWord(word string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.WakeWord = word
}

func (c *Config) AudioDevice() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.AudioDevice
}

func (c *Config) SetAudioDevice(device string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.AudioDevice = device
}

// --- Updates and betas ---

func (c *Config) SkippedVersion() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.SkippedVersion
}

func (c *Config) SetSkippedVersion(version string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.SkippedVersion = version
}

func (c *Config) BetaLipReading() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.BetaLipReading
}

func (c *Config) SetBetaLipReading(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.BetaLipReading = enabled
}

// --- Computer use ---

func (c *Config) ComputerUseEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.ComputerUseEnabled
}

func (c *Config) SetComputerUseEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.ComputerUseEnabled = enabled
}

func (c *Config) ComputerUseDisplay() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.ComputerUseDisplay
}

func (c *Config) SetComputerUseDisplay(display string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.ComputerUseDisplay = display
}

// ComputerUseMonitor returns the monitor index to control, defaulting to 0
// (the primary one). See NotificationsEnabled on where the default lives.
func (c *Config) ComputerUseMonitor() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.data.ComputerUseMonitor == nil {
		return 0
	}
	return *c.data.ComputerUseMonitor
}

func (c *Config) SetComputerUseMonitor(monitor int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.ComputerUseMonitor = &monitor
}

func (c *Config) ComputerUseProfile() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.ComputerUseProfile
}

// --- Persistence ---

// Path returns the file the configuration is read from and written to.
func (c *Config) Path() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.path
}

func (c *Config) ConfigDir() string {
	return filepath.Dir(c.Path())
}

// Save writes the configuration out. MarshalJSON takes the read lock, and
// Path takes it separately afterwards: the two are sequential rather than
// nested, because a second RLock while a writer waits would deadlock.
func (c *Config) Save() error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.Path(), data, 0o600)
}

func configDir() (string, error) {
	home, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "openuai"), nil
}
