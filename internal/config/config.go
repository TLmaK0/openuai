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

type Config struct {
	Provider        string       `json:"provider"`
	ClaudeAPIKey    string       `json:"claude_api_key,omitempty"`
	DefaultModel    string       `json:"default_model"`
	OpenAITokens    *OAuthTokens `json:"openai_tokens,omitempty"`
	path            string
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
