package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	URL       string            `json:"url,omitempty"`
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

type Config struct {
	Provider        string            `json:"provider"`
	ClaudeAPIKey    string            `json:"claude_api_key,omitempty"`
	DefaultModel    string            `json:"default_model"`
	OpenAITokens    *OAuthTokens      `json:"openai_tokens,omitempty"`
	MCPServers      []MCPServerConfig `json:"mcp_servers,omitempty"`
	WatchedChats       []string          `json:"watched_chats,omitempty"`
	MaxConcurrentAgents    int              `json:"max_concurrent_agents,omitempty"`
	NotificationsEnabled   *bool            `json:"notifications_enabled,omitempty"`
	APIEnabled             bool             `json:"api_enabled,omitempty"`
	APIPort                int              `json:"api_port,omitempty"`
	VoiceEnabled           *bool            `json:"voice_enabled,omitempty"`
	TTSVoice               string           `json:"tts_voice,omitempty"`
	STTModel               string           `json:"stt_model,omitempty"`
	STTLanguage            string           `json:"stt_language,omitempty"`
	AudioDevice            string           `json:"audio_device,omitempty"`
	SkippedVersion         string           `json:"skipped_version,omitempty"`
	BetaLipReading         bool             `json:"beta_lip_reading,omitempty"`
	path               string
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
	cfg := &Config{
		Provider:     "openai",
		DefaultModel: "gpt-5.1-codex",
		path:         path,
	}

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
	return cfg, nil
}

func (c *Config) ConfigDir() string {
	return filepath.Dir(c.path)
}

func (c *Config) Save() error {
	data, err := json.MarshalIndent(c, "", "  ")
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
