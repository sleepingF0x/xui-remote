// Package config loads and validates the bot's YAML configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Panel    Panel    `yaml:"panel"`
	Telegram Telegram `yaml:"telegram"`
	State    State    `yaml:"state"`
}

type Panel struct {
	// BaseURL must end at the API group, e.g. https://host/panel/panel/api/
	// — the panel's webBasePath and the /panel/api route group both appear.
	BaseURL  string `yaml:"base_url"`
	APIToken string `yaml:"api_token"`
	// PublicHost overrides the Host header so generated share links carry a
	// reachable address instead of whatever interface the bot dialled.
	PublicHost     string `yaml:"public_host"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	InsecureTLS    bool   `yaml:"insecure_skip_verify"`
}

type Telegram struct {
	Token          string  `yaml:"token"`
	AllowedChatIDs []int64 `yaml:"allowed_chat_ids"`
}

type State struct {
	Dir          string `yaml:"dir"`
	MaxSnapshots int    `yaml:"max_snapshots"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	c.Panel.BaseURL = strings.TrimSpace(c.Panel.BaseURL)
	if c.Panel.BaseURL != "" && !strings.HasSuffix(c.Panel.BaseURL, "/") {
		c.Panel.BaseURL += "/"
	}
	if c.Panel.TimeoutSeconds <= 0 {
		c.Panel.TimeoutSeconds = 20
	}
	if c.State.Dir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			c.State.Dir = filepath.Join(home, ".xui-remote")
		} else {
			c.State.Dir = ".xui-remote"
		}
	}
	if c.State.MaxSnapshots <= 0 {
		c.State.MaxSnapshots = 20
	}
}

func (c *Config) validate() error {
	var missing []string
	if c.Panel.BaseURL == "" {
		missing = append(missing, "panel.base_url")
	}
	if strings.TrimSpace(c.Panel.APIToken) == "" {
		missing = append(missing, "panel.api_token")
	}
	if strings.TrimSpace(c.Telegram.Token) == "" {
		missing = append(missing, "telegram.token")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required settings: %s", strings.Join(missing, ", "))
	}
	// An empty allowlist would accept commands from any chat that finds the bot.
	if len(c.Telegram.AllowedChatIDs) == 0 {
		return fmt.Errorf("telegram.allowed_chat_ids must list at least one chat id")
	}
	return nil
}

func (c *Config) IsAllowed(chatID int64) bool {
	for _, id := range c.Telegram.AllowedChatIDs {
		if id == chatID {
			return true
		}
	}
	return false
}
