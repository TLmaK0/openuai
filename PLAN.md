# OpenUAI — Plan (Draft Ideas)

> This document collects the initial ideas to later generate the definitive plan.

## Phase 1: Project Skeleton

- Initialize Go project with Wails v2
- Base folder structure (cmd/, internal/, frontend/)
- Basic build that produces a binary with an empty UI (hello world)
- Verify cross-platform compilation (Linux, macOS, Windows)

## Phase 2: LLM Client + Cost Tracking

- HTTP client for LLM APIs (Claude, OpenAI, Ollama local)
- API keys and model configuration from the UI
- Reusable prompt/template system
- Per-conversation context (maintain history per contact/channel)
- Support for tool use / function calling
- **Token cost tracking**: real-time display of input/output tokens, cost per request, accumulated cost per session/day/month, configurable budget alerts

## Phase 3: Agent Actions & Autonomy

- Filesystem access (read, write, organize files)
- Bash/shell execution (scripts, CLI tools, build commands)
- Git operations (commit, branch, diff, log)
- Web browsing (basic scraping for research)
- System command execution (with explicit permissions)
- Privilege escalation: detect when a command needs admin, prompt user once via UI, cache elevation for the session (sudo/polkit on Linux/macOS, UAC/runas on Windows)
- Action chaining (output of one → input of another)
- Plan-first mode: agent generates a visible plan, executes it autonomously, reports at the end
- Configurable autonomy level per command:
  - Ask every time
  - Allow for this session
  - Allow forever (persisted to config)
  - No confirmation needed (pre-approved safe commands)
- Plan persistence: save plan to disk so it survives interruptions

## Phase 4: Minimal UI

- Frontend with Svelte or React embedded via Wails
- Dashboard: recent events, connector status
- Configuration: API keys, model selection
- Real-time logs
- Basic chat interface with the agent

## Phase 5: Distribution (first usable release)

- Public GitHub repository
- CI/CD with GitHub Actions for multi-platform builds
- Automatic releases: binary per OS/arch on GitHub Releases
- **GitHub Pages landing site**: project description, features, download links per OS/arch, getting started guide
- First public release: functional agent with LLM + actions + UI

## Phase 6: Event Bus

- Implement internal event bus with goroutines + channels
- Define `EventSource` interface that all connectors implement
- Define generic `Event` struct (source, type, payload, timestamp, metadata)
- Subscription system: register listeners by event type
- Internal queue with backpressure to avoid overwhelming the agent

## ~~Phase 7: Rules Engine + Triggers~~ ❌ Removed

Removed — the LLM handles all routing and decision-making directly.
The only filtering needed is which event sources/chats the agent watches,
which is handled by `watch_chat` / `unwatch_chat` tools.

## Phase 8: First Connector — WhatsApp ✅

- ~~Integrate whatsmeow for WhatsApp connection via QR~~ → replaced with external MCP bridge (mcp-whatsapp)
- WhatsApp MCP server subscribes to event bus: incoming messages → events
- Agent tools: `watch_chat` / `unwatch_chat` to subscribe to specific JIDs
- Event bridge: watched chats queue notifications that are prepended to the next agent turn
- **Loop prevention**: `is_from_me=true` messages are ignored for unwatched chats (no noise from normal WA activity)
- **Watched chats process all messages**: if a chat is explicitly watched, own messages are included too — the user can message themselves to control the agent
- **Debug visibility**: Events panel renders `message`-type events as conversation bubbles (← incoming / → sent), showing message body and sender
- **Notifications include message body**: agent receives the full message content directly in the notification, no need to call list_messages again

## Phase 9: Memory System

- Persistent memory across sessions (stored locally)
- User profile: preferences, role, how they like to work
- Project context: what's being worked on, decisions made, conventions
- Conversation history: per contact/channel for continuity
- Feedback memory: corrections and guidance the user has given
- Memory index for fast retrieval by relevance

## Phase 10: Multi-Agent

- Spawn sub-agents for parallel task execution
- Parent agent decomposes a complex task into independent sub-tasks
- Each sub-agent runs in its own goroutine with isolated context
- Results aggregated back to parent agent
- Configurable max concurrent agents

## Phase 11: More Connectors

- Email (go-imap for receiving, net/smtp for sending)
- Telegram (bot API)
- Generic webhooks (embedded HTTP server)
- **Teams** (via MCP bridge — same pattern as WhatsApp): read channels/chats, send messages, react to mentions
- Slack (via MCP or API)
- Filesystem watcher (monitor folder changes)

## Phase 12: MCP Compatible + API Mode

- **MCP client**: connect to external MCP servers (use existing tools/connectors from the ecosystem)
- **MCP server**: expose OpenUAI's capabilities so other tools can call it
- **REST API mode**: run headless as a local API server, other apps can send tasks and receive results via HTTP
- API authentication for local access

## Phase 13: System Tray + Full UI

- **System tray**: run in background, native OS notifications on events and completed tasks, quick actions from tray menu (pause/resume, open dashboard)
- WhatsApp screen: scan QR, view conversations, status
- Visual rule editor (trigger → condition → action)
- Configuration: connectors, rules
- Embedded auto-update (check + download new version)

## Phase 14: Voice

- Speech-to-text input (whisper.cpp embedded or cloud STT API)
- Text-to-speech output (local TTS or cloud API)
- Voice activation: wake word or push-to-talk
- Conversation mode: back-and-forth voice dialog with the agent

## Phase 15: Marketplace

- Community repository of shared rules, connectors, and prompt templates
- Browse, search, and install from the UI
- Publish your own rules/connectors
- Version control and ratings
- Local-first: marketplace is optional, everything works offline

---

## Decisions

- [x] Frontend: **Svelte** (smaller bundles, less boilerplate, native Wails support)
- [x] Rule format: **YAML** (human-readable, standard, good Go support)
- [x] Embedded database: **SQLite** (via modernc.org/sqlite, pure Go, no CGO)
- [x] Plugin/extension: **Yes, but later** (design interfaces now, implement plugin system after phase 15)
- [x] Project license: **Commons Clause + MIT** (dual licensing)
- [x] Final name: **OpenUAI** (to be revisited)
- [x] Voice: **Cloud STT first** (OpenAI Whisper API), local whisper.cpp as optional later
- [x] Marketplace: **GitHub-based** (repo as registry, community PRs, zero infra)
- [x] MCP: **Latest stable spec**
