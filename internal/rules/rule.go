package rules

// Rule defines a trigger → action mapping.
type Rule struct {
	ID          string   `yaml:"id"          json:"id"`
	Name        string   `yaml:"name"        json:"name"`
	Description string   `yaml:"description" json:"description,omitempty"`
	Enabled     bool     `yaml:"enabled"     json:"enabled"`
	Trigger     Trigger  `yaml:"trigger"     json:"trigger"`
	Actions     []Action `yaml:"actions"     json:"actions"`
}

// Trigger defines when a rule fires.
type Trigger struct {
	Source   string `yaml:"source"    json:"source"`              // event source filter (e.g. "whatsapp", "email", "*" for any)
	Type     string `yaml:"type"      json:"type"`                // event type filter (e.g. "message", "*" for any)
	Sender   string `yaml:"sender"    json:"sender,omitempty"`    // exact sender match (metadata "sender")
	Keyword  string `yaml:"keyword"   json:"keyword,omitempty"`   // substring match on payload
	Regex    string `yaml:"regex"     json:"regex,omitempty"`     // regex match on payload
	Schedule string `yaml:"schedule"  json:"schedule,omitempty"`  // cron expression (e.g. "0 9 * * MON")
	Metadata map[string]string `yaml:"metadata" json:"metadata,omitempty"` // match specific metadata fields
}

// ActionType identifies what a rule action does.
type ActionType string

const (
	ActionReply     ActionType = "reply"      // send a reply via the same source
	ActionLLM       ActionType = "llm"        // run an LLM prompt with event context
	ActionWriteFile ActionType = "write_file" // write content to a file
	ActionBash      ActionType = "bash"       // execute a shell command
	ActionWebhook   ActionType = "webhook"    // call an external URL
	ActionNotify    ActionType = "notify"     // send a desktop notification
)

// Action defines what happens when a rule triggers.
type Action struct {
	Type     ActionType `yaml:"type"     json:"type"`
	Template string     `yaml:"template" json:"template,omitempty"` // go template with {{.Event}} context
	Prompt   string     `yaml:"prompt"   json:"prompt,omitempty"`   // LLM prompt template
	Path     string     `yaml:"path"     json:"path,omitempty"`     // file path for write_file
	Command  string     `yaml:"command"  json:"command,omitempty"`  // bash command
	URL      string     `yaml:"url"      json:"url,omitempty"`      // webhook URL
}

// RulesFile is the top-level YAML structure.
type RulesFile struct {
	Rules []Rule `yaml:"rules" json:"rules"`
}
