package marketplace

// AuthType describes what credentials the server needs.
type AuthType string

const (
	AuthNone   AuthType = "none"
	AuthAPIKey AuthType = "api_key"
	AuthOAuth  AuthType = "oauth"
)

// Category groups catalog entries in the UI.
type Category string

const (
	CatOffice    Category = "Office"
	CatDev       Category = "Dev"
	CatSearch    Category = "Search"
	CatUtilities Category = "Utilities"
)

// CatalogEntry is a pre-configured MCP server template.
type CatalogEntry struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Icon        string            `json:"icon"`
	Category    Category          `json:"category"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env,omitempty"`   // values may contain {{PLACEHOLDER}}
	AuthType    AuthType          `json:"auth_type"`       // none, api_key, oauth
	AuthLabel   string            `json:"auth_label"`      // UI label, e.g. "GitHub Personal Access Token"
	AuthEnvVar  string            `json:"auth_env_var"`    // env var name where the secret goes
	Subscribe   []string          `json:"subscribe,omitempty"`
}

// Catalog is the built-in curated list of MCP servers.
var Catalog = []CatalogEntry{
	{
		Name:        "Google Drive",
		Description: "List, read and search Google Drive files",
		Icon:        "GD",
		Category:    CatOffice,
		Command:     "npx",
		Args:        []string{"-y", "@modelcontextprotocol/server-gdrive"},
		AuthType:    AuthOAuth,
		AuthLabel:   "Google OAuth (configured in server)",
	},
	{
		Name:        "GitHub",
		Description: "Repos, issues, pull requests, code search",
		Icon:        "GH",
		Category:    CatDev,
		Command:     "npx",
		Args:        []string{"-y", "@modelcontextprotocol/server-github"},
		Env:         map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": "{{SECRET}}"},
		AuthType:    AuthAPIKey,
		AuthLabel:   "GitHub Personal Access Token",
		AuthEnvVar:  "GITHUB_PERSONAL_ACCESS_TOKEN",
	},
	{
		Name:        "Slack",
		Description: "Channels, messages, users, search",
		Icon:        "SL",
		Category:    CatOffice,
		Command:     "npx",
		Args:        []string{"-y", "@modelcontextprotocol/server-slack"},
		Env:         map[string]string{"SLACK_BOT_TOKEN": "{{SECRET}}"},
		AuthType:    AuthAPIKey,
		AuthLabel:   "Slack Bot Token (xoxb-...)",
		AuthEnvVar:  "SLACK_BOT_TOKEN",
	},
	{
		Name:        "PostgreSQL",
		Description: "Query tables, inspect schema, run SQL",
		Icon:        "PG",
		Category:    CatDev,
		Command:     "npx",
		Args:        []string{"-y", "@modelcontextprotocol/server-postgres", "{{SECRET}}"},
		AuthType:    AuthAPIKey,
		AuthLabel:   "PostgreSQL connection string",
		AuthEnvVar:  "_ARG_SECRET",
	},
	{
		Name:        "Git",
		Description: "Read, search and manage git repositories",
		Icon:        "GT",
		Category:    CatDev,
		Command:     "npx",
		Args:        []string{"-y", "@modelcontextprotocol/server-git"},
		AuthType:    AuthNone,
	},
	{
		Name:        "Puppeteer",
		Description: "Browser automation, screenshots, scraping",
		Icon:        "PP",
		Category:    CatSearch,
		Command:     "npx",
		Args:        []string{"-y", "@modelcontextprotocol/server-puppeteer"},
		Env: map[string]string{
			"PUPPETEER_LAUNCH_OPTIONS": `{"args":["--no-sandbox","--disable-setuid-sandbox"]}`,
			"ALLOW_DANGEROUS":          "true",
		},
		AuthType: AuthNone,
	},
	{
		Name:        "Playwright",
		Description: "Browser automation, can connect to existing browsers",
		Icon:        "PW",
		Category:    CatSearch,
		Command:     "npx",
		Args:        []string{"-y", "@playwright/mcp@latest", "--cdp-endpoint", "http://127.0.0.1:9222"},
		AuthType:    AuthNone,
	},
}

// GetCatalog returns all catalog entries.
func GetCatalog() []CatalogEntry {
	return Catalog
}

// GetByName returns a catalog entry by name, or nil if not found.
func GetByName(name string) *CatalogEntry {
	for i := range Catalog {
		if Catalog[i].Name == name {
			return &Catalog[i]
		}
	}
	return nil
}
