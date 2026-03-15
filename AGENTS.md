# Agents Research

## OpenUAI — Project Vision

OpenUAI extends the Cowork model with a **real-time event subscription system**. Beyond executing tasks on demand, the agent can automatically react to external events (WhatsApp messages, emails, notifications, webhooks, etc.).

### Agent Behavior Model

OpenUAI combines the **autonomy of Cowork** with the **power of Claude Code**:

- **Plan-first**: the agent generates a full plan before acting
- **Long-running execution**: works for minutes without interruption
- **Low interruption**: only asks the user on critical decisions or ambiguities
- **Final report**: presents a summary of everything it did
- **Full tool access**: Bash, Git, filesystem, web scraping, system commands — not just file editing
- **Autonomy level**: simple per-command permission model:
  1. **Ask every time**: confirm each command before execution
  2. **Allow for this session**: don't ask again for this command type until restart
  3. **Allow forever**: permanently trust this command type (saved to config)
  4. **No confirmation needed**: pre-approved safe commands (read files, list dirs, etc.)
- **Privilege escalation**: when a command needs admin/root privileges, the agent detects it and requests elevation through the UI. The user authenticates once (password/biometrics) and the agent can use elevated privileges for the current session. Works cross-platform:
  - Linux/macOS: sudo with cached credentials or polkit
  - Windows: UAC prompt via runas

### Target Capabilities

1. **Everything Cowork does**: filesystem access, autonomous planning, multi-step execution, MCP connectors
2. **Full Claude Code power**: Bash execution, Git operations, system commands, code editing, web browsing
3. **Event subscription**: the agent subscribes to event sources and reacts when they occur
   - WhatsApp messages (via WhatsApp Business API / Baileys / Evolution API)
   - Incoming emails
   - Generic webhooks
   - Telegram, Slack, Teams messages, etc.
   - Scheduled triggers (cron-style)
   - Screen/clipboard watchers
4. **Rules and triggers**: the user defines rules like "when I receive a message from X, do Y"
5. **Reactive execution**: the agent doesn't just wait for instructions, it also acts on events based on configured rules
6. **Multi-agent**: spawn sub-agents for parallel task execution, parent decomposes complex tasks into independent sub-tasks
7. **Persistent memory**: remembers user preferences, project context, feedback, and conversation history across sessions
8. **MCP compatible**: acts as both MCP client (use external tools) and MCP server (expose capabilities to other tools)
9. **API mode**: run headless as a local REST API server for integration with other apps
10. **System tray**: runs in background with native OS notifications, quick actions from tray menu
11. **Token cost tracking**: real-time token usage, cost per request, accumulated cost, budget alerts
12. **Voice**: speech-to-text input and text-to-speech output for hands-free interaction
13. **Marketplace**: community repository of shared rules, connectors, and prompt templates

### Distribution

- **Single executable** with minimal dependencies. The user downloads and runs it.
- **Cross-platform**: Windows, macOS, Linux (amd64 + arm64)
- No external runtime (no Node, no Python, no Docker)
- Everything embedded: web server/UI, event bus, rules engine, connectors, LLM client
- **Platform dependencies**:
  - **macOS**: none (uses native WKWebView, included with macOS)
  - **Windows**: none (uses WebView2, included with Windows 10+)
  - **Linux**: requires `libwebkit2gtk-4.1` (pre-installed on most desktop distributions)
- **Technology: Go**
  - Wails v2 for UI (native OS webview, embedded web frontend, no Electron)
  - `go:embed` for static assets
  - Goroutines + channels for the event bus and concurrent subscriptions
  - Trivial cross-compile: `GOOS=<os> GOARCH=<arch> go build`
  - Key libraries: whatsmeow (WhatsApp), net/http (webhooks), go-imap (email)

### Conceptual Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                    openuai (single binary)                    │
│                                                              │
│  [Event Sources]        [Engine]            [Actions]        │
│   WhatsApp ──┐                             ┌── Filesystem    │
│   Email ─────┤                             ├── Reply         │
│   Webhooks ──┤                             ├── Bash/Git      │
│   Telegram ──┼→ Event Bus → Rules ──┐      ├── APIs          │
│   Slack ─────┤              Engine  ├→ Agent├── Voice Out     │
│   Cron ──────┤                      │      └── Notify        │
│   Clipboard ─┘                      │                        │
│                                     ├→ Sub-Agent (parallel)  │
│                                     └→ Sub-Agent (parallel)  │
│                                                              │
│  [System Tray]  [Embedded UI]  [LLM Client]  [Cost Tracker] │
│  [Memory]       [MCP Client/Server]  [REST API]  [Voice In] │
│  [Marketplace]                                               │
└──────────────────────────────────────────────────────────────┘
```

---

## Reference: Claude Cowork

Claude Cowork is an Anthropic tool that brings Claude Code's agentic capabilities to general knowledge work (beyond coding).

### What it is

An agent that runs on Claude Desktop with direct access to the user's filesystem. The user grants access to a folder and Claude can read, edit, and create files autonomously.

### How it works

1. The user describes a desired goal or outcome
2. Claude creates a plan and executes it step by step
3. It keeps the user informed of progress and asks when needed
4. The user comes back to find the work completed

### Core Capabilities

- **File management**: organize, rename, create, and edit documents
- **Research**: browse the web and synthesize information
- **Connectors/Plugins**: integrates with Slack, Notion, Figma, Google Drive, Gmail, DocuSign, Microsoft 365, etc. via MCP
- **Multi-step tasks**: can execute complex workflows autonomously

### Typical Use Cases

- Organize folders by sorting and renaming files
- Create expense spreadsheets from screenshots
- Draft reports from scattered notes
- Synthesize research from multiple sources

### Key Architectural Pattern

An agent with filesystem access + external tools (MCP) + autonomous planning and execution capability.

### Sources

- [Introducing Cowork | Claude](https://claude.com/blog/cowork-research-preview)
- [Cowork: Claude Code power for knowledge work](https://claude.com/product/cowork)
- [Get started with Cowork | Claude Help Center](https://support.claude.com/en/articles/13345190-get-started-with-cowork)
- [Anthropic's Cowork tool | TechCrunch](https://techcrunch.com/2026/01/12/anthropics-new-cowork-tool-offers-claude-code-without-the-code/)
