package subagents

// ScopeBuiltin marks the agents that ship with OpenUAI. Any user or project
// definition with the same name replaces them.
const ScopeBuiltin = "builtin"

// Built-in agent names.
const (
	BuiltinExplore = "explore"
	BuiltinGeneral = "general-purpose"
)

// exploreTools are the read-only tools of the exploration agent. Names that
// are not registered (e.g. memory tools disabled) are simply skipped.
var exploreTools = []string{
	"read_file", "list_dir", "search_files",
	"git_status", "git_diff", "git_log",
	"web_fetch", "image_search",
	"search_memory", "read_memory", "list_memories",
}

const explorePrompt = `You are a read-only exploration sub-agent of OpenUAI. Your job is to find and understand things quickly: locate files, search code, read documentation and answer questions about a codebase or a topic.

Rules:
- You cannot modify anything: you have no tools to write, delete, move, commit or run commands. Do not try.
- Search broadly first (file patterns, directory listings), then read only what is relevant.
- When a search returns nothing, try variations before concluding something does not exist.
- Be thorough but efficient: stop as soon as you can answer.

When you finish, reply with a concise report: what you found, with exact file paths (and line references when useful), and anything you could not determine. That report is all the main agent will see.`

const generalPrompt = `You are a general-purpose sub-agent of OpenUAI. You handle complex, multi-step tasks on your own: researching, searching code, editing files, running commands and verifying the result.

Rules:
- Do exactly the task you were given; do not expand its scope.
- Prefer editing existing files over creating new ones, and never create documentation files unless the task asks for them.
- Verify your work (build, tests, re-reading the result) before reporting it as done.
- Never fabricate output: if something fails or you cannot finish, say so.

When you finish, reply with a clear summary of what you did, which files you changed and how you verified it. That summary is all the main agent will see.`

// Builtins returns the agents that ship with OpenUAI and need no file.
func Builtins() []*Definition {
	return []*Definition{
		{
			Name:        BuiltinExplore,
			Description: "Fast read-only agent for exploring codebases and documentation: finding files, searching code, reading and answering questions about it. Cannot modify anything. Use it for any search or investigation that would otherwise take several reads.",
			Tools:       exploreTools,
			HasTools:    true,
			Model:       "haiku",
			Prompt:      explorePrompt,
			Scope:       ScopeBuiltin,
			Path:        "(built-in)",
		},
		{
			Name:        BuiltinGeneral,
			Description: "General-purpose agent for complex, multi-step tasks that need both exploring and acting: researching, editing files, running commands and verifying the result. Use it when a task is self-contained and does not fit a more specific agent.",
			Model:       "inherit",
			Prompt:      generalPrompt,
			Scope:       ScopeBuiltin,
			Path:        "(built-in)",
		},
	}
}
