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

## Phase 5: Distribution (first usable release) ✅

- Public GitHub repository
- CI/CD with GitHub Actions for multi-platform builds (Linux amd64/arm64, macOS universal, Windows amd64)
- Automatic releases: binary per OS/arch on GitHub Releases (triggered on `v*` tags)
- **GitHub Pages landing site** ✅: `docs/index.html` — features, download links, getting started, architecture diagram
- First public release: tag `v0.1.0` to trigger

## Phase 6: Event Bus ✅

- Internal event bus with goroutines + channels (`internal/eventbus/`)
- `EventSource` interface that all connectors implement
- Generic `Event` struct (source, type, payload, timestamp, metadata)
- Handler registration: `On(eventType, handler)` for specific types + `OnAny(handler)` for all events
- Worker pool (4 concurrent workers) + buffered queue (256 events) with backpressure
- Stats tracking: events received/handled/dropped by source and type
- Fan-out: events dispatched simultaneously to UI, agent, and stats

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

## Phase 10: Multi-Agent ✅

- `spawn_agents` tool: parent agent decomposes tasks into concurrent sub-agents
- Each sub-agent runs in its own goroutine with isolated context and CostTracker
- Semaphore-based concurrency limit (configurable, default 5)
- No nesting: sub-agents cannot spawn sub-sub-agents (`Registry.Without()`)
- Results aggregated back as single tool result, costs rolled up to parent
- UI receives `[sub-agent:taskID]`-prefixed steps

## Phase 11: More Connectors ✅

- All connectors are external MCP servers — no custom Go code needed
- **Teams** ✅: real-time via Trouter WebSocket + MCP bridge
- **WhatsApp** ✅: via mcp-whatsapp bridge
- **Email, Telegram, Slack, webhooks, filesystem watcher**: available as community MCP servers, connect via existing MCP client infrastructure
- Architecture already supports: MCP auto-start, SSE subscriptions, event bus integration

## Phase 12: MCP Compatible + API Mode ✅

- **MCP client** ✅: connect to external MCP servers (use existing tools/connectors from the ecosystem)
- **MCP server**: expose OpenUAI's capabilities so other tools can call it — deferred
- **REST API** ✅: `internal/api/` — Echo v4 on `127.0.0.1:9120`, 18 endpoints + WebSocket
  - `POST /api/chat` async (202 + request_id) or blocking (`?wait=true`)
  - WebSocket `/ws` with gorilla/websocket hub: broadcasts `agent_step`, `event_received`, `permission_request`, `chat_complete`
  - Config: `api_enabled: true`, `api_port: 9120` (disabled by default, localhost only)
  - Callback-based `Handlers` struct avoids import cycles with main package

## Phase 13: System Tray + Notifications ✅

- **System tray** ✅: `fyne.io/systray` — tray icon with Show/Hide, Notifications toggle, Quit menu
- **Native notifications** ✅: `github.com/gen2brain/beeep` — notify on watched chat messages + agent completion
- **Hide on close** ✅: `HideWindowOnClose: true` — window hides, tray stays, "Show" brings it back, "Quit" exits
- **Config** ✅: `notifications_enabled` persisted, toggleable from tray menu + Wails API
- Embedded auto-update (check + download new version) — deferred to future phase

## Phase 14: Voice ✅

- **STT** ✅: Push-to-talk mic button → `MediaRecorder` (webm/opus) → base64 → OpenAI Whisper API via ChatGPT OAuth tokens
- **TTS** ✅: Speaker icon on assistant messages → OpenAI TTS API via ChatGPT OAuth tokens → mp3 playback
- **Auth**: Reuses existing ChatGPT OAuth tokens (`api.openai.com/v1/audio/*`) — no separate API key needed
- **Cost tracking** ✅: Whisper ($0.006/min) and TTS ($0.015/1K chars) tracked via `TrackDirect` on CostTracker
- **Config** ✅: `voice_enabled`, `tts_voice` (10 voice options) persisted in config
- **MIME fallback**: tries `audio/webm;codecs=opus` → `audio/webm` → `audio/ogg;codecs=opus` → `audio/mp4`
- Voice activation (wake word) and conversation mode: deferred to future iteration

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
