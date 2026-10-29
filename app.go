package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"openuai/internal/agent"
	"openuai/internal/api"
	"openuai/internal/computeruse"
	"openuai/internal/config"
	"openuai/internal/eventbus"
	"openuai/internal/hooks"
	"openuai/internal/lipreading"
	"openuai/internal/llm"
	// Registers the providers that ship in the binary. This blank import is
	// the one place an in-tree provider has to be named.
	_ "openuai/internal/llm/providers/all"
	"openuai/internal/logger"
	"openuai/internal/marketplace"
	"openuai/internal/mcpclient"
	"openuai/internal/mcpserver"
	"openuai/internal/memory"
	"openuai/internal/piper"
	"openuai/internal/procs"
	"openuai/internal/tools"
	"openuai/internal/tray"
	"openuai/internal/updater"
	"openuai/internal/voice"
	"openuai/internal/whisper"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"openuai/internal/subagents"
)

type App struct {
	subRuns    subAgentRuns    // running sub-agents, cancellable one by one
	subLimiter subAgentLimiter // one concurrency limit for the whole sub-agent tree
	skills     skillState      // files worked on, skill usage, forks running (app_skills.go)
	ctx        context.Context
	cfg        *config.Config
	// providers is written while the app runs — a plugin can be added or
	// removed from the settings screen — and read from the goroutines that
	// run agent turns, so it is guarded.
	providersMu  sync.RWMutex
	providers    map[string]llm.Provider
	costTracker  *llm.CostTracker
	registry     *tools.Registry
	permissions  *agent.PermissionManager
	currentAgent *agent.Agent
	eventBus     *eventbus.Bus
	procs        *procs.Manager

	mcpManager     *mcpclient.Manager
	apiServer      *api.Server
	recorder       *voice.Recorder
	wake           *voice.WakeListener
	lipRecorder    *lipreading.Recorder
	whisperVersion string
	version        string

	permMu       sync.Mutex
	permResponse chan permAnswer

	agentMu     sync.Mutex // serializes all agent.Run() calls (user messages + autonomous triggers)
	agentCancel context.CancelFunc

	memoryStore *memory.Store

	watchedChats   map[string]struct{} // JIDs being watched; all messages (including own) are processed
	watchedChatsMu sync.RWMutex

	// recentSentIDs tracks message IDs sent by the agent to prevent echo loops.
	// When the agent sends via MCP, the response includes the message ID.
	// When the same ID arrives back via Trouter, we filter it out.
	recentSentMu  sync.Mutex
	recentSentIDs map[string]int64 // message ID → unix timestamp

	// speakMu guards the sound switch together with speakCtx: every speech
	// synthesis runs under speakCtx, and turning sound off cancels it, which
	// kills any synthesis still in progress.
	speakMu     sync.Mutex
	speakCtx    context.Context
	speakCancel context.CancelFunc

	// Restart recovery (app_restart.go): background messages waiting for the
	// agent survive restarts, and the turn a restart interrupted is resumed.
	pending      *pendingStore
	resumeOnce   sync.Once
	shuttingDown atomic.Bool

	hk hookState // hooks (app_hooks.go)
}

type permAnswer struct {
	level    agent.PermissionLevel
	approved bool
}

func NewApp() *App {
	return &App{
		costTracker:   llm.NewCostTracker(),
		permResponse:  make(chan permAnswer, 1),
		watchedChats:  make(map[string]struct{}),
		recentSentIDs: make(map[string]int64),
		lipRecorder:   lipreading.NewRecorder(),
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	cfg, err := config.Load()
	if err != nil {
		println("Error loading config:", err.Error())
		cfg = &config.Config{}
	}
	a.cfg = cfg

	// Init logger
	if err := logger.Init(cfg.ConfigDir()); err != nil {
		println("Error init logger:", err.Error())
	}
	logger.Info("OpenUAI %s starting up", a.version)
	logger.Info("Config dir: %s", cfg.ConfigDir())

	// A configuration written when providers could ship as separate
	// executables may still list some. They cannot be honoured, so they are
	// dropped on load — said once here rather than disappearing quietly.
	if ignored := cfg.IgnoredProviderPlugins(); len(ignored) > 0 {
		logger.Info("Ignoring %d provider plugin(s) left in the configuration: %v. "+
			"Providers now ship with the app; nothing has to be registered by hand.",
			len(ignored), ignored)
	}

	// Model providers: every one that registered itself, each handed its own
	// slot in the configuration. Nothing here names a provider.
	a.buildProviders()
	logger.Info("Provider: %s, Model: %s", cfg.Provider(), cfg.DefaultModel())

	// Check for updates in background (delay to let frontend mount and register event listeners)
	go func() {
		time.Sleep(3 * time.Second)
		info := updater.CheckForUpdate(a.version, cfg.SkippedVersion())
		if info != nil {
			logger.Info("Emitting update_available event to frontend")
			wailsRuntime.EventsEmit(a.ctx, "update_available", info)
		}
	}()

	// Auto-download whisper-cli + model in background
	go func() {
		sttModel := cfg.STTModel()
		if sttModel == "" {
			sttModel = "small"
		}
		if err := whisper.EnsureReady(cfg.ConfigDir(), a.whisperVersion, sttModel); err != nil {
			logger.Error("Whisper setup: %s", err.Error())
		}
	}()

	// Voice recorder (native mic capture via miniaudio + whisper + Piper TTS)
	a.recorder = voice.NewRecorder()
	a.recorder.Device = cfg.AudioDevice()
	a.recorder.OnLevel = func(level int) {
		wailsRuntime.EventsEmit(a.ctx, "voice_level", level)
	}

	// Wake-word listener: hands-free "say the name, then your message".
	a.wake = &voice.WakeListener{
		WakeWord: func() string { return a.cfg.WakeWord() },
		Device:   func() string { return a.cfg.AudioDevice() },
		Transcribe: func(wav []byte) (string, error) {
			// Use the fast model: medium is far too slow on CPU (~16s/utterance),
			// which breaks interactivity. Full-phrase context lets small recognise
			// the wake word fine. Bias toward the wake word with --prompt.
			res := voice.TranscribeWAV(wav, a.cfg.STTModel(), a.cfg.STTLanguage(), a.cfg.WakeWord(), a.cfg.ConfigDir())
			if res.Error != "" {
				return "", fmt.Errorf("%s", res.Error)
			}
			return res.Text, nil
		},
		OnMessage: func(text string) {
			wailsRuntime.EventsEmit(a.ctx, "wake_message", text)
		},
		OnListening: func() {
			wailsRuntime.EventsEmit(a.ctx, "wake_listening", nil)
		},
		OnCapture: func() {
			wailsRuntime.EventsEmit(a.ctx, "wake_capturing", nil)
		},
		OnDiscard: func(heard string) {
			wailsRuntime.EventsEmit(a.ctx, "wake_discard", heard)
		},
		OnSession: func(active bool) {
			wailsRuntime.EventsEmit(a.ctx, "wake_session", active)
		},
		OnLevel: func(level int) {
			wailsRuntime.EventsEmit(a.ctx, "voice_level", level)
		},
	}

	// Memory store
	a.memoryStore = memory.New(filepath.Join(cfg.ConfigDir(), "memory"))

	// Tools registry
	a.registry = tools.NewRegistry()
	a.registry.Register(tools.ReadFile{})
	a.registry.Register(tools.WriteFile{})
	a.registry.Register(tools.ListDir{})
	a.registry.Register(tools.DeleteFile{})
	a.registry.Register(tools.MoveFile{})
	a.registry.Register(tools.SearchFiles{})
	a.registry.Register(tools.BashSudo{})
	a.procs = procs.NewManager(a.procCallbacks())
	a.pending = loadPendingStore(filepath.Join(cfg.ConfigDir(), "sessions"))
	a.registry.Register(tools.ProcStart{M: a.procs})
	a.registry.Register(tools.ProcRead{M: a.procs})
	a.registry.Register(tools.ProcWrite{M: a.procs})
	a.registry.Register(tools.ProcKill{M: a.procs})
	a.registry.Register(tools.ProcList{M: a.procs})
	a.registry.Register(tools.Bash{Procs: a.procs})
	a.registry.Register(tools.Skill{Listing: a.skillListing, Invoke: a.invokeSkill})
	a.registry.Register(tools.GitStatus{})
	a.registry.Register(tools.GitDiff{})
	a.registry.Register(tools.GitLog{})
	a.registry.Register(tools.GitAdd{})
	a.registry.Register(tools.GitCommit{})
	a.registry.Register(tools.GitBranch{})
	a.registry.Register(tools.WebFetch{})
	a.registry.Register(tools.ImageSearch{})
	a.registry.Register(tools.WatchChat{Fn: a.WatchChat})
	a.registry.Register(tools.UnwatchChat{Fn: a.UnwatchChat})
	a.registry.Register(tools.SaveMemory{Store: a.memoryStore})
	a.registry.Register(tools.ReadMemory{Store: a.memoryStore})
	a.registry.Register(tools.DeleteMemory{Store: a.memoryStore})
	a.registry.Register(tools.ListMemories{Store: a.memoryStore})
	a.registry.Register(tools.SearchMemory{Store: a.memoryStore})
	a.registry.Register(tools.SpawnAgents{Fn: a.spawnSubAgents, Agents: a.describeSubAgents, Models: a.describeSubAgentModels})
	if cfg.ComputerUseEnabled() {
		disp := a.GetComputerUseDisplay()
		computeruse.RegisterTools(a.registry, computeruse.New(disp, cfg.ComputerUseMonitor(), cfg.ComputerUseProfile()))
		logger.Info("Computer-use tools registered (display %s)", disp)
	}
	logger.Info("Registered %d tools", len(a.registry.Definitions()))

	// Permissions
	a.permissions = agent.NewPermissionManager(
		cfg.ConfigDir(),
		func(ctx context.Context, tool, command string) (agent.PermissionLevel, bool) {
			// Drop an answer left over from a request that was cancelled.
			select {
			case <-a.permResponse:
			default:
			}
			logger.Info("Permission request: tool=%s command=%s", tool, command)
			a.notifyHook("permission_prompt", "Permission needed", "OpenUAI needs your permission to use "+tool+": "+command)
			wailsRuntime.EventsEmit(a.ctx, "permission_request", map[string]string{
				"tool":    tool,
				"command": command,
			})
			if a.apiServer != nil {
				a.apiServer.Broadcast("permission_request", "", map[string]string{
					"tool":    tool,
					"command": command,
				})
			}
			select {
			case answer := <-a.permResponse:
				logger.Info("Permission response: approved=%v level=%v", answer.approved, answer.level)
				return answer.level, answer.approved
			case <-ctx.Done():
				// Stop / Esc: close the dialog and deny.
				logger.Info("Permission request cancelled: tool=%s", tool)
				wailsRuntime.EventsEmit(a.ctx, "permission_cancelled", map[string]string{"tool": tool})
				if a.apiServer != nil {
					a.apiServer.Broadcast("permission_cancelled", "", map[string]string{"tool": tool})
				}
				return agent.PermAlways, false
			}
		},
	)

	// Event bus
	a.eventBus = eventbus.New()
	a.eventBus.OnAny(func(event eventbus.Event) error {
		logger.Info("Event received: source=%s type=%s payload_len=%d", event.Source, event.Type, len(event.Payload))
		wailsRuntime.EventsEmit(a.ctx, "event_received", event)
		if a.apiServer != nil {
			a.apiServer.Broadcast("event_received", "", event)
		}
		return nil
	})

	// Immediately trigger the agent when a message from a watched chat arrives.
	// All messages are processed (including own) — the user explicitly chose to watch this chat.
	// is_from_me messages in unwatched chats are ignored to avoid noise from normal WA activity.
	a.eventBus.On(eventbus.EventMessage, func(event eventbus.Event) error {
		chatJID := event.Metadata["chat_jid"]
		sender := event.Metadata["sender"]

		a.watchedChatsMu.RLock()
		_, watchedByChat := a.watchedChats[chatJID]
		_, watchedBySender := a.watchedChats[sender]
		a.watchedChatsMu.RUnlock()

		if !watchedByChat && !watchedBySender {
			logger.Debug("Event ignored (not watched): chat_jid=%q sender=%q", chatJID, sender)
			return nil
		}

		// Prevent echo loops: if the message ID was sent by the agent, skip it.
		msgID := event.Metadata["message_id"]
		if msgID != "" {
			a.recentSentMu.Lock()
			// Clean old entries (>5min)
			now := time.Now().Unix()
			for k, ts := range a.recentSentIDs {
				if now-ts > 300 {
					delete(a.recentSentIDs, k)
				}
			}
			if _, isEcho := a.recentSentIDs[msgID]; isEcho {
				delete(a.recentSentIDs, msgID)
				a.recentSentMu.Unlock()
				logger.Debug("Event ignored (echo, msg_id=%s): chat_jid=%q", msgID, chatJID)
				return nil
			}
			a.recentSentMu.Unlock()
		}

		fromMe := event.Metadata["is_from_me"] == "true"
		senderName := event.Metadata["sender_name"]
		if senderName == "" {
			if fromMe {
				senderName = "me"
			} else {
				senderName = sender
			}
		}
		notification := fmt.Sprintf("[New %s message from %s (chat: %s): %s]", event.Source, senderName, chatJID, event.Payload)
		logger.Info("Watched event received, triggering agent: %s", notification)
		preview := event.Payload
		if len(preview) > 80 {
			preview = preview[:80] + "..."
		}
		a.notify("message_received", fmt.Sprintf("%s — %s", event.Source, senderName), preview)
		// Queued on disk first, so a restart before the agent handles it
		// loses nothing, and a message delivered again is not processed twice.
		key := event.Source + ":" + msgID
		if msgID == "" {
			key = fmt.Sprintf("%s:%s:%d", event.Source, chatJID, time.Now().UnixNano())
		}
		if a.pending.Add(key, notification) {
			go a.drainPending()
		}
		return nil
	})
	a.eventBus.Start(ctx)
	logger.Info("EventBus started")

	// MCP manager (if servers configured)
	mcpServers := cfg.MCPServers()
	if len(mcpServers) > 0 {
		a.mcpManager = mcpclient.NewManager(mcpServers)
		a.mcpManager.OnTokensSaved(func(name string, tokens *config.MCPOAuthTokens) {
			if _, ok := a.cfg.SetMCPServerTokens(name, tokens); ok {
				a.cfg.Save()
				logger.Info("MCP OAuth tokens saved for %s", name)
			}
		})
		a.mcpManager.OnReady(func() {
			mcpclient.RegisterMCPTools(a.registry, a.mcpManager, a.TrackSentMessageID)
			logger.Info("MCP tools registered: %d total tools now", len(a.registry.Definitions()))
			// Reset agent so it picks up new tools. Under agentMu so it never
			// races with startup recovery or a running turn.
			a.agentMu.Lock()
			a.currentAgent = nil
			a.agentMu.Unlock()
		})
		if err := a.eventBus.RegisterSource(a.mcpManager); err != nil {
			logger.Error("Failed to register MCP manager: %s", err.Error())
		} else {
			logger.Info("MCP manager registered with %d servers", len(mcpServers))
		}
	}

	// Restore watched chats from config
	watched := cfg.WatchedChats()
	for _, jid := range watched {
		a.watchedChats[jid] = struct{}{}
	}
	if len(watched) > 0 {
		logger.Info("Restored %d watched chats: %v", len(watched), watched)
	}

	// System tray
	tray.SetEnabled(cfg.NotificationsEnabled())
	tray.SetSoundEnabled(cfg.VoiceEnabled())
	tray.SetIconBytes(appIcon)
	tray.Start(tray.Config{
		Icon: appIcon,
		OnShow: func() {
			wailsRuntime.WindowShow(a.ctx)
		},
		OnQuit: func() {
			wailsRuntime.Quit(a.ctx)
		},
	})
	a.updateTrayTooltip()

	// API server (optional, disabled by default)
	if cfg.APIEnabled() {
		port := cfg.APIPort()
		if port == 0 {
			port = 9120
		}
		a.apiServer = api.New(api.Config{
			Host: "127.0.0.1",
			Port: port,
		}, api.Handlers{
			SendMessageSync: func(content string) any {
				return a.SendMessage(content)
			},
			GetCostSummary:    func() any { return a.GetCostSummary() },
			GetModels:         func() any { return a.GetModels() },
			GetModel:          func() any { return a.GetDefaultModel() },
			SetModel:          func(model string) any { return a.setModel(model, "sdk") },
			GetProvider:       func() any { return a.GetProvider() },
			SetProvider:       func(provider string) any { return a.SetProvider(provider) },
			GetWatchedChats:   func() any { return a.GetWatchedChats() },
			WatchChat:         func(jid string) any { return a.WatchChat(jid) },
			UnwatchChat:       func(jid string) any { return a.UnwatchChat(jid) },
			GetEventStats:     func() any { return a.GetEventStats() },
			GetMCPServers:     func() any { return a.GetMCPServers() },
			RespondPermission: func(level string, approved bool) { a.RespondPermission(level, approved) },
			ClearChat:         func() { a.ClearChat() },
			ResetCosts:        func() { a.ResetCosts() },
			GetNotifications: func() any {
				return map[string]bool{"enabled": a.GetNotificationsEnabled()}
			},
			SetNotifications: func(enabled bool) { a.ToggleNotifications(enabled) },
			RequestRestart:   func(worktree string, window []int) (any, error) { return a.RequestRestart(worktree, window) },
			// OpenUAI as an MCP server for other clients (issue #28).
			MCP: mcpserver.HTTPHandler(mcpserver.New(a.version, a.mcpBackend())),
		})
		if err := a.apiServer.Start(); err != nil {
			logger.Error("Failed to start API server: %s", err.Error())
		}
	}

	a.initHooks()
	a.recoverAfterRestart()
	// SessionStart: "resume" when a conversation was restored.
	source := "startup"
	a.agentMu.Lock()
	if ag := a.currentAgent; ag != nil && len(ag.Messages()) > 1 {
		source = "resume"
	}
	a.agentMu.Unlock()
	a.startSession(source, true)
	logger.Info("Startup complete")
}

func (a *App) shutdown(ctx context.Context) {
	logger.Info("Shutting down")
	a.endSession("other")
	a.saveWindowPosition()
	// Keep the list of running processes: the next start reports them.
	a.shuttingDown.Store(true)
	if a.wake != nil {
		a.wake.Stop() // terminate the listener so its capture device is released
	}
	if a.apiServer != nil {
		a.apiServer.Shutdown(ctx)
	}
	if a.procs != nil {
		a.procs.Shutdown()
	}
	tray.Stop()
}

// ToggleNotifications sets the notification enabled state and persists it.
func (a *App) ToggleNotifications(enabled bool) {
	tray.SetEnabled(enabled)
	tray.SyncNotifyCheckbox()
	a.cfg.SetNotificationsEnabled(enabled)
	a.cfg.Save()
	logger.Info("Notifications enabled: %v", enabled)
}

// GetNotificationsEnabled returns whether notifications are on.
func (a *App) GetNotificationsEnabled() bool {
	return tray.IsEnabled()
}

func (a *App) updateTrayTooltip() {
	a.watchedChatsMu.RLock()
	n := len(a.watchedChats)
	a.watchedChatsMu.RUnlock()
	if n == 0 {
		tray.SetTooltip("OpenUAI")
	} else {
		tray.SetTooltip(fmt.Sprintf("OpenUAI — %d chats watched", n))
	}
}

// setProvider puts a built provider in the map.
func (a *App) setProvider(name string, p llm.Provider) {
	a.providersMu.Lock()
	defer a.providersMu.Unlock()
	a.providers[name] = p
}

// provider returns the built provider registered under name, or nil.
func (a *App) provider(name string) llm.Provider {
	a.providersMu.RLock()
	defer a.providersMu.RUnlock()
	return a.providers[name]
}

// buildProviders instantiates every registered provider and settles which one
// is active, so that a configuration naming an unknown provider — or a fresh
// one naming none — still starts on something usable.
func (a *App) buildProviders() {
	a.providersMu.Lock()
	rebuilt := make(map[string]llm.Provider)
	// defaultModels serves as both the set of registered names and each one's
	// default model. Gathering it here is what lets the settle below be a pure
	// map lookup: it needs nothing from the llm registry, so no other lock and
	// no I/O is held while the configuration's lock is.
	//
	// Known divergence from master, left open deliberately. This map is filled
	// from one snapshot of the registry, so a provider unregistered and re-added
	// under the same name with a different DefaultModel while this rebuild runs
	// settles with its old default; master re-read the live registry with
	// llm.Lookup() at that point and picked up the new one. For a plugin the
	// trigger is a description that changed between its removal and its re-add.
	// What is settled is a model that was valid for that same provider, not the
	// empty model the fallback below could once produce.
	//
	// Closing it means one read of the registry serving both this map and the
	// fallback, which needs the "marked Default, else first by name" rule to
	// move behind an llm entry point instead of being repeated here — a change
	// to internal/llm, tracked separately rather than made under this fix.
	defaultModels := make(map[string]string)
	for _, d := range llm.Descriptors() {
		defaultModels[d.Name] = d.DefaultModel
		// Keep providers already built, so a running plugin is not restarted
		// and an in-memory credential is not lost.
		if existing, ok := a.providers[d.Name]; ok {
			rebuilt[d.Name] = existing
			continue
		}
		rebuilt[d.Name] = d.New(a.cfg.ProviderStore(d.Name))
	}
	a.providers = rebuilt
	a.providersMu.Unlock()

	// The fallback comes from a second read of the registry, which the loop
	// above cannot have covered: a provider registering between the two reads
	// can be chosen here while being absent from defaultModels. So its model
	// is taken from the same read that chose it, leaving the settle's lookup
	// covered whatever happened in between.
	fallback := ""
	if d, ok := llm.DefaultDescriptor(); ok {
		fallback = d.Name
		defaultModels[d.Name] = d.DefaultModel
	}

	// Which configured provider turned out not to be registered, recorded by
	// the settle and logged after it rather than inside it: the settle holds
	// the configuration's lock, and a log line is I/O.
	unknown := ""
	nothingRegistered := false
	provider, _ := a.cfg.SettleProviderAndModel(func(provider, model string) (string, string) {
		if _, known := defaultModels[provider]; !known {
			if fallback == "" {
				// Nothing is registered, so there is nothing to settle on.
				// Leave the configuration exactly as it stands.
				nothingRegistered = true
				return provider, model
			}
			unknown = provider
			provider, model = fallback, ""
		}
		if model == "" {
			model = defaultModels[provider]
		}
		return provider, model
	})

	if nothingRegistered {
		logger.Error("No model provider is registered")
		return
	}
	if unknown != "" {
		logger.Info("Configured provider %q is not registered, falling back to %q", unknown, provider)
	}
}

// newProvider builds one registered provider.
func (a *App) newProvider(name string) llm.Provider {
	d, ok := llm.Lookup(name)
	if !ok {
		return nil
	}
	return d.New(a.cfg.ProviderStore(name))
}

func (a *App) activeProvider() llm.Provider {
	return a.provider(a.cfg.Provider())
}

// --- Provider management ---

func (a *App) GetProvider() string {
	return a.cfg.Provider()
}

func (a *App) SetProvider(provider string) error {
	d, ok := llm.Lookup(provider)
	if !ok {
		return fmt.Errorf("unknown provider: %s", provider)
	}
	from := a.cfg.DefaultModel()
	if err := a.preModelSwitch(from, d.DefaultModel, "", "command"); err != nil {
		return err
	}
	logger.Info("Provider changed to: %s", provider)
	a.cfg.SetProviderAndModel(provider, d.DefaultModel)
	a.currentAgent = nil // reset agent for new provider
	err := a.cfg.Save()
	go a.postModelSwitch(from, d.DefaultModel, "", "command")
	return err
}

// GetProviders names every registered provider, so the UI can list one it has
// never heard of instead of branching on names.
//
// It asks the providers nothing. Readiness used to be gathered here, which
// meant listing the providers started every plugin process just to ask
// whether it held its credentials — on a screen that only ever shows the
// active provider's readiness. That belongs to GetActiveProvider.
func (a *App) GetProviders() []llm.ProviderSummary {
	descriptors := llm.Descriptors()
	summaries := make([]llm.ProviderSummary, 0, len(descriptors))
	for _, d := range descriptors {
		summaries = append(summaries, d.Summary())
	}
	return summaries
}

// GetActiveProvider describes the provider in use: which credential it needs
// and whether it already has it. This is the only place a provider is asked,
// so at most one plugin process is involved.
func (a *App) GetActiveProvider() llm.ProviderInfo {
	d, ok := llm.Lookup(a.cfg.Provider())
	if !ok {
		return llm.ProviderInfo{}
	}
	return d.Info(a.provider(d.Name))
}

// --- Provider credentials ---

// ProviderLogin runs the active provider's interactive login. It returns the
// error text, or an empty string on success.
func (a *App) ProviderLogin() string {
	logger.Info("Starting login for provider: %s", a.cfg.Provider())
	if err := llm.Login(a.activeProvider()); err != nil {
		logger.Error("Login failed: %s", err.Error())
		return err.Error()
	}
	logger.Info("Login successful")
	return ""
}

// SetProviderSecret hands a secret (an API key) to the active provider, which
// persists it in its own store.
func (a *App) SetProviderSecret(secret string) error {
	logger.Info("Credentials updated for provider: %s", a.cfg.Provider())
	return llm.SetSecret(a.activeProvider(), secret)
}

// ProviderReady reports whether the active provider holds the credentials it
// needs to serve a request.
func (a *App) ProviderReady() bool {
	return llm.Ready(a.activeProvider())
}

// --- Models ---

func (a *App) GetModels() []string {
	// Some providers gate the available models per account, so prefer the live
	// list and fall back to the static one when it cannot be fetched.
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	return llm.AvailableModels(ctx, a.activeProvider())
}

func (a *App) GetDefaultModel() string {
	return a.cfg.DefaultModel()
}

func (a *App) SetDefaultModel(model string) error {
	return a.setModel(model, "picker")
}

// setModel switches the model; source is how the switch was requested
// ("picker" from the UI, "sdk" from the REST API), for model-switch hooks.
func (a *App) setModel(model, source string) error {
	from := a.cfg.DefaultModel()
	if err := a.preModelSwitch(from, model, model, source); err != nil {
		return err
	}
	logger.Info("Model changed to: %s", model)
	a.cfg.SetDefaultModel(model)
	a.currentAgent = nil // reset agent for new model
	err := a.cfg.Save()
	go a.postModelSwitch(from, model, model, source)
	return err
}

// --- Permission response from UI ---

func (a *App) RespondPermission(level string, approved bool) {
	var pl agent.PermissionLevel
	switch level {
	case "session":
		pl = agent.PermSession
	case "forever":
		pl = agent.PermForever
	default:
		pl = agent.PermAlways
	}
	a.permResponse <- permAnswer{level: pl, approved: approved}
}

// --- Agent Chat ---

type ChatResponse struct {
	Content      string  `json:"content"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Model        string  `json:"model"`
	Error        string  `json:"error,omitempty"`
	// Refused is set when the message was not sent at all (e.g. images the
	// provider cannot see), so the UI can give it back to the user.
	Refused bool `json:"refused,omitempty"`
}

func (a *App) ensureAgent() *agent.Agent {
	if ag := a.currentAgent; ag != nil {
		return ag
	}
	{
		memoryText := a.memoryStore.LoadIndex()
		if memoryText != "" {
			a.instructionsLoaded(filepath.Join(a.cfg.ConfigDir(), "memory", "MEMORY.md"), "User", "session_start")
		}
		ag := agent.New(agent.Config{
			Provider:        a.activeProvider(),
			Model:           a.cfg.DefaultModel(),
			Registry:        a.registry,
			Permissions:     a.permissions,
			CostTracker:     a.costTracker,
			MemoryText:      memoryText,
			Hooks:           a.loadHooks,
			WorkDir:         a.GetWorkDir(),
			SessionID:       a.sessionID(),
			TranscriptPath:  a.transcriptPath(),
			BackgroundTasks: a.backgroundTasks,
			// Save after every tool call and result, not only at the end of the
			// turn: a restart mid-turn must not lose the work already shown.
			OnCheckpoint: func(ag *agent.Agent) { a.saveSession(ag) },
			// Files the agent works on can make skills available (paths,
			// nested .openuai/skills folders).
			OnToolUse: a.noteSkillPaths,
			OnStep: func(step agent.StepResult) {
				logger.Debug("Agent step: type=%s tool=%s content_len=%d", step.Type, step.ToolName, len(step.Content))
				// MessageDisplay hooks may change what is shown (never what is kept).
				step = a.displayStep(step)
				wailsRuntime.EventsEmit(a.ctx, "agent_step", step)
				if a.apiServer != nil {
					a.apiServer.Broadcast("agent_step", "", step)
				}
			},
		})
		// Try to restore previous session
		if err := ag.LoadSession(a.cfg.ConfigDir()); err != nil {
			logger.Error("Failed to load session: %s", err.Error())
		}
		// Return the local: a concurrent reset (e.g. MCP tools becoming ready)
		// may set currentAgent to nil at any moment.
		a.currentAgent = ag
		return ag
	}
}

func (a *App) SendMessage(content string) ChatResponse {
	return a.SendMessageWithImages(content, nil)
}

// maxImagesPerMessage and maxImageBytes bound what a single message carries.
const (
	maxImagesPerMessage = 10
	maxImageBytes       = 8 << 20 // base64 length of one image
)

// SendMessageWithImages sends a user message with pasted or dropped images
// (base64, PNG or JPEG). It refuses, before anything runs, when the active
// provider cannot pass images, so they are never dropped silently.
func (a *App) SendMessageWithImages(content string, images []string) ChatResponse {
	return a.SendMessageWithAttachments(content, images, nil)
}

// SendMessageWithAttachments sends a user message with images and files.
// Files must have been stored first with SaveAttachment or PickAttachments.
func (a *App) SendMessageWithAttachments(content string, images []string, files []llm.Attachment) ChatResponse {
	logger.Info("SendMessage: %s (%d images, %d files)", content, len(images), len(files))
	if len(files) > 0 {
		checked, err := a.checkAttachments(files)
		if err != nil {
			return ChatResponse{Refused: true, Error: err.Error()}
		}
		files = checked
	}
	if len(images) > 0 {
		if len(images) > maxImagesPerMessage {
			return ChatResponse{Refused: true, Error: fmt.Sprintf("Too many images: at most %d per message.", maxImagesPerMessage)}
		}
		for _, img := range images {
			if len(img) > maxImageBytes {
				return ChatResponse{Refused: true, Error: "An image is too large to send."}
			}
		}
		if p := a.activeProvider(); p == nil || !llm.SupportsImages(p) {
			name := a.cfg.Provider()
			return ChatResponse{Refused: true, Error: "The selected provider (" + name + ") cannot see images. Remove the images or switch to a provider that supports them (Claude, Claude Code, OpenAI)."}
		}
	}

	a.waitSessionStart(a.ctx)
	a.checkCwd(a.loadHooks())

	// UserPromptSubmit hooks see the message as typed (before a /skill
	// expands) and can block it or add context to it.
	var notes []string
	if hs := a.loadHooks(); hs.Has(hooks.UserPromptSubmit) {
		r := a.runHookOn(a.ctx, hs, hooks.UserPromptSubmit, "", map[string]any{"prompt": content})
		if r.Decision == "block" || r.StopAll {
			reason := r.Reason
			if r.StopAll && r.StopReason != "" {
				reason = r.StopReason
			}
			msg := "Blocked by a hook: " + reason
			if !r.SuppressPrompt {
				msg += "\nOriginal prompt: " + content
			}
			return ChatResponse{Refused: true, Error: msg}
		}
		a.setHookTitle(r.SessionTitle)
		notes = hookNotesOf(r)
	}

	// /<skill> at the start of the message loads that skill (or several
	// stacked ones) for this turn. UserPromptExpansion hooks can block it or
	// add context; the skills' frontmatter hooks are registered for the rest
	// of the session.
	plan, err := a.planSkillInvocation(a.ctx, content)
	if err != nil {
		return ChatResponse{Refused: true, Error: err.Error()}
	}
	// Context waiting from SessionStart, model switches and background hooks
	// goes first, then the context for this prompt.
	content = agent.WithHookContext(plan.content, append(append(a.takeHookNotes(), notes...), plan.notes...))

	a.agentMu.Lock()
	defer a.agentMu.Unlock()

	ctx, cancel := context.WithCancel(a.ctx)
	a.agentCancel = cancel
	defer func() { a.agentCancel = nil }()

	ag := a.ensureAgent()
	// A skill with context: fork runs in a sub-agent first; its report goes
	// with the message.
	content = a.runPlannedFork(ctx, skillPlan{content: content, fork: plan.fork, forkRendered: plan.forkRendered})
	a.applyHookTitle(ag)
	// Saved before running, so the message survives a restart mid-turn.
	ag.BeginWithAttachments(content, images, files)
	// Invoked skills' allowed-tools, disallowed-tools, model and effort
	// apply until the next message.
	for _, c := range plan.changes {
		ag.ApplyTurnChange(c)
	}
	a.saveSession(ag)
	err = ag.Continue(ctx)

	// Save session after each interaction (success or failure)
	if saveErr := ag.SaveSession(a.cfg.ConfigDir()); saveErr != nil {
		logger.Error("Failed to save session: %s", saveErr.Error())
	}

	if err != nil {
		logger.Error("Agent run error: %s", err.Error())
		return ChatResponse{Error: err.Error()}
	}

	summary := a.costTracker.Summary()
	logger.Info("Agent completed: tokens_in=%d tokens_out=%d cost=$%.4f",
		summary.TotalInputTokens, summary.TotalOutputTokens, summary.TotalCostUSD)

	return ChatResponse{
		// The UI shows the answer from the streamed steps; API and MCP
		// callers get it here, taken while this turn still holds agentMu.
		Content:      lastAnswer(ag.Messages()),
		InputTokens:  summary.TotalInputTokens,
		OutputTokens: summary.TotalOutputTokens,
		CostUSD:      summary.TotalCostUSD,
	}
}

// EditMessage rewinds the conversation to just before the n-th user message
// (0-based), then re-runs the agent with the edited content — letting the user
// edit a previous message and continue from there.
func (a *App) EditMessage(index int, content string) ChatResponse {
	logger.Info("EditMessage: rewind to user msg %d, content: %s", index, content)

	a.agentMu.Lock()
	defer a.agentMu.Unlock()

	ctx, cancel := context.WithCancel(a.ctx)
	a.agentCancel = cancel
	defer func() { a.agentCancel = nil }()

	ag := a.ensureAgent()
	images := ag.UserMessageImages(index)
	files := ag.UserMessageAttachments(index)
	ag.RewindToUserMessage(index)
	ag.BeginWithAttachments(content, images, files)
	a.saveSession(ag)
	err := ag.Continue(ctx)

	if saveErr := ag.SaveSession(a.cfg.ConfigDir()); saveErr != nil {
		logger.Error("Failed to save session: %s", saveErr.Error())
	}
	if err != nil {
		logger.Error("Agent run error: %s", err.Error())
		return ChatResponse{Error: err.Error()}
	}

	summary := a.costTracker.Summary()
	return ChatResponse{
		InputTokens:  summary.TotalInputTokens,
		OutputTokens: summary.TotalOutputTokens,
		CostUSD:      summary.TotalCostUSD,
	}
}

// AbortAgent cancels the currently running agent loop.
func (a *App) AbortAgent() {
	if cancel := a.agentCancel; cancel != nil {
		logger.Info("Agent abort requested by user")
		cancel()
	}
}

// triggerAgentWithNotification immediately runs the agent with an event notification.
// It acquires agentMu so it serializes with SendMessage and other autonomous triggers.
func (a *App) triggerAgentWithNotification(notification string) {
	a.triggerAgentIf(notification, nil)
}

// triggerAgentIf is triggerAgentWithNotification, but once it holds the agent
// it skips the run if proceed (when not nil) returns false.
func (a *App) triggerAgentIf(notification string, proceed func() bool) {
	a.agentMu.Lock()
	defer a.agentMu.Unlock()
	if proceed != nil && !proceed() {
		return
	}
	a.runNotificationLocked(notification, nil)
}

// runNotificationLocked runs the agent on a background notification. The
// caller holds agentMu. onBegin (optional) runs once the notification is
// saved in the session, i.e. once a restart can no longer lose it.
func (a *App) runNotificationLocked(notification string, onBegin func()) {
	ctx, cancel := context.WithCancel(a.ctx)
	a.agentCancel = cancel
	defer func() {
		a.agentCancel = nil
		cancel()
	}()

	ag := a.ensureAgent()
	a.applyHookTitle(ag)
	logger.Info("Autonomous agent run starting: %s", notification)
	ag.Begin(agent.WithHookContext(notification, a.takeHookNotes()))
	a.saveSession(ag)
	if onBegin != nil {
		onBegin()
	}
	if err := ag.Continue(ctx); err != nil {
		logger.Error("Autonomous agent run error: %s", err.Error())
	}
	a.saveSession(ag)
	a.notify("agent_completed", "OpenUAI", "Agent completed autonomous run")
	logger.Info("Autonomous agent run complete")
}

// spawnSubAgents is the bridge between the spawn_agents tool and the agent package.
// A task may name a subagent definition (.openuai/agents/ in the project,
// <config dir>/agents/ for the user) and a model. Definitions are read again
// on every call, so edits apply without restarting.
func (a *App) spawnSubAgents(ctx context.Context, tasks []tools.SubTask) []tools.SubTaskResult {
	maxConcurrent := a.cfg.MaxConcurrentAgents()
	if maxConcurrent <= 0 {
		maxConcurrent = agent.DefaultMaxConcurrentSubAgents
	}

	workDir := a.GetWorkDir()
	// Each sub-agent's work is kept here (apart from the main conversation)
	// so it can be resumed; old records are removed after the retention period.
	transcriptDir := filepath.Join(a.cfg.ConfigDir(), "subagents")
	if n := agent.PruneTranscripts(transcriptDir, agent.TranscriptRetention); n > 0 {
		logger.Info("Removed %d expired sub-agent records", n)
	}
	defs := a.loadSubAgentDefs(workDir, true)
	provider := a.activeProvider()
	parentModel := a.cfg.DefaultModel()
	globalModel := os.Getenv("OPENUAI_SUBAGENT_MODEL")
	var available []string
	availableLoaded := false

	// Nesting: the caller's level comes from ctx (main conversation = 0).
	// Children only get tools their parent has; the deepest level does not
	// get spawn_agents at all.
	scope := subAgentScopeFrom(ctx)
	childDepth := scope.depth + 1
	if childDepth > agent.MaxSubAgentDepth {
		msg := fmt.Sprintf("nesting limit reached: sub-agents can delegate up to %d levels deep; do this work yourself instead of calling spawn_agents again", agent.MaxSubAgentDepth)
		logger.Info("[sub-agent:%s] %s", scope.key, msg)
		out := make([]tools.SubTaskResult, len(tasks))
		for i, t := range tasks {
			out[i] = tools.SubTaskResult{ID: t.ID, Agent: t.Agent, Error: msg}
		}
		return out
	}
	// Background sub-agents (task "background" or definition background: true)
	// run detached from this turn; only the main conversation launches them.
	if scope.depth == 0 && !inBackgroundRun(ctx) {
		var fg, bg []tools.SubTask
		for _, t := range tasks {
			name := t.Agent
			if name == "" {
				name = subagents.BuiltinGeneral
			}
			if d := defs.Defs[name]; t.Background || (d != nil && d.Background) {
				bg = append(bg, t)
			} else {
				fg = append(fg, t)
			}
		}
		if len(bg) > 0 {
			return a.spawnWithBackground(ctx, fg, bg)
		}
	}
	baseRegistry := a.registry
	if scope.registry != nil {
		baseRegistry = scope.registry
	}
	if childDepth >= agent.MaxSubAgentDepth {
		baseRegistry = baseRegistry.Without("spawn_agents")
	}
	taskRegistries := map[string]*tools.Registry{}
	// Sub-agents with isolation: worktree get their own temporary worktree.
	worktrees := map[string]*subagents.Worktree{}
	// Inline MCP servers of sub-agent definitions only live while this run lasts.
	var scopedMCP []*mcpclient.Manager
	defer func() {
		for _, m := range scopedMCP {
			_ = m.Stop()
		}
	}()

	agentTasks := make([]agent.SubAgentTask, len(tasks))
	for i, t := range tasks {
		// A resumed sub-agent keeps its agent type unless the task names one.
		if t.Resume != "" && t.Agent == "" {
			if tr, err := agent.LoadTranscript(transcriptDir, t.Resume); err == nil {
				t.Agent = tr.Agent
			}
		}
		at := agent.SubAgentTask{ID: t.ID, Description: t.Description, Agent: t.Agent, ResumeID: t.Resume}
		var def *subagents.Definition
		if t.Agent == "" {
			// No agent named: run as the general-purpose agent.
			def = defs.Defs[subagents.BuiltinGeneral]
		} else {
			d, ok := defs.Defs[t.Agent]
			if !ok {
				at.SetupError = fmt.Sprintf("unknown agent %q; available agents: %s", t.Agent, joinOrNone(defs.Names()))
				agentTasks[i] = at
				continue
			}
			def = d
		}
		registry, err := subagents.FilterTools(baseRegistry, def)
		if err != nil {
			at.SetupError = err.Error()
			agentTasks[i] = at
			continue
		}
		// A skill run in a sub-agent: its disallowed-tools leave the pool and
		// its allowed-tools run without asking.
		if len(t.Disallow) > 0 {
			registry = registry.Without(t.Disallow...)
		}
		at.Allow = t.Allow
		if def != nil && def.Isolation == subagents.IsolationWorktree {
			wt, err := subagents.CreateWorktreeWithHooks(workDir, t.ID, a.loadHooks(), a.commonHookInput())
			if err != nil {
				at.SetupError = err.Error()
				agentTasks[i] = at
				continue
			}
			worktrees[t.ID] = wt
			registry = subagents.IsolateTools(registry, wt)
			logger.Info("[sub-agent:%s] Isolated in worktree %s (branch %s)", t.ID, wt.Dir, wt.Branch)
		}
		var mcpNote string
		if def != nil && len(def.MCPServers) > 0 {
			var mgr *mcpclient.Manager
			mgr, mcpNote = a.addSubAgentMCP(def, baseRegistry, registry)
			if mgr != nil {
				scopedMCP = append(scopedMCP, mgr)
			}
		}
		defModel, maxTurns := "", 0
		if def != nil {
			defModel, maxTurns = def.Model, def.MaxTurns
		}
		if !availableLoaded && (t.Model != "" || defModel != "" || globalModel != "") {
			available = llm.AvailableModels(ctx, provider)
			availableLoaded = true
		}
		model, notice := subagents.ResolveModel(t.Model, defModel, globalModel, parentModel, available)
		if notice != "" {
			logger.Info("[sub-agent:%s] %s", t.ID, notice)
		}
		at.Model, at.Registry, at.MaxTurns, at.Notice = model, registry, maxTurns, notice
		if mcpNote != "" {
			logger.Info("[sub-agent:%s] %s", t.ID, mcpNote)
			at.Notice = strings.TrimSpace(at.Notice + " " + mcpNote)
		}
		taskRegistries[t.ID] = registry
		promptDir := workDir
		if wt := worktrees[t.ID]; wt != nil {
			promptDir = wt.Dir
			at.SystemPrompt = subagents.SystemPrompt(def, wt.Dir) + subagents.IsolationPrompt(wt)
		} else {
			at.SystemPrompt = subagents.SystemPrompt(def, workDir)
		}
		for _, f := range instructionFilesFor(def, promptDir) {
			a.instructionsLoaded(f, "Project", "session_start")
		}
		// Persistent memory: its contents go in the prompt and the agent always
		// gets the file tools it needs to manage it.
		if memDir := subagents.MemoryDir(def, workDir, filepath.Join(a.cfg.ConfigDir(), "agent-memory")); memDir != "" {
			at.SystemPrompt += subagents.MemoryPrompt(memDir)
			for _, tl := range baseRegistry.All() {
				for _, n := range subagents.MemoryTools {
					if tl.Definition().Name == n {
						registry.Register(tl)
					}
				}
			}
		}
		// Effort: the per-task parameter wins over the definition.
		if e := strings.ToLower(strings.TrimSpace(t.Effort)); e != "" {
			if llm.ValidEffort(e) {
				at.Effort = e
			} else {
				at.Notice = strings.TrimSpace(at.Notice + " Ignored invalid effort \"" + t.Effort + "\".")
			}
		}
		if at.Effort == "" && def != nil {
			at.Effort = def.Effort
		}
		at.Hooks = subagents.HookSet(def)
		if section, notice := a.preloadSkills(def, workDir); section != "" || notice != "" {
			at.SystemPrompt += section
			if notice != "" {
				at.Notice = strings.TrimSpace(at.Notice + " " + notice)
			}
		}
		at.WorkDir = workDir
		if wt := worktrees[t.ID]; wt != nil {
			at.WorkDir = wt.Dir
		}
		if def != nil {
			at.PermissionMode = def.PermissionMode
		}
		agentTasks[i] = at
	}

	run := a.subRuns.newRun()
	results := agent.RunSubAgents(ctx, agent.SubAgentConfig{
		Provider:      provider,
		Model:         parentModel,
		Registry:      baseRegistry.Without(subagents.AlwaysExcluded...),
		Permissions:   a.permissions,
		MaxConcurrent: maxConcurrent,
		TranscriptDir: transcriptDir,
		Hooks:         a.loadHooks,
		WorkDir:       workDir,
		SessionID:     a.sessionID(),
		// One limit for the whole tree: top-level tasks wait for a free slot,
		// nested ones fail at once (waiting while the parent holds its slot
		// could deadlock).
		Acquire: func(c context.Context, _ agent.SubAgentTask) (func(), error) {
			return a.subLimiter.acquire(c, maxConcurrent, scope.depth == 0)
		},
		// Each sub-agent gets its own view: its steps go to subagent_* events,
		// never into the main conversation stream.
		OnStep: func(taskID string, step agent.StepResult) {
			logger.Debug("[sub-agent:%s] step: type=%s tool=%s", taskID, step.Type, step.ToolName)
			a.emitSubAgent("subagent_step", map[string]any{
				"key": run + "/" + taskID, "type": step.Type, "content": step.Content, "tool_name": step.ToolName,
			})
		},
		OnStart: func(t agent.SubAgentTask, model string) {
			name := t.Agent
			if name == "" {
				name = subagents.BuiltinGeneral
			}
			logger.Info("[sub-agent:%s] level=%d parent=%q", t.ID, childDepth, scope.key)
			a.emitSubAgent("subagent_start", map[string]any{
				"key": run + "/" + t.ID, "run": run, "id": t.ID, "agent": name, "model": model, "description": t.Description,
				"parent": scope.key, "depth": childDepth, "resume": t.ResumeID, "color": defs.Color(name),
			})
		},
		OnDone: func(r agent.SubAgentResult) {
			a.emitSubAgent("subagent_done", map[string]any{
				"key": run + "/" + r.ID, "agent_id": r.AgentID, "output": r.Output, "error": r.Error, "notice": r.Notice,
				"partial": r.Partial, "cancelled": r.Cancelled, "cost_usd": r.CostUSD,
				"input_tokens": r.InputTokens, "output_tokens": r.OutputTokens,
			})
		},
		TaskContext: func(taskID string, parent context.Context) (context.Context, func()) {
			c, release := a.subRuns.taskContext(run+"/"+taskID, parent)
			return withSubAgentScope(c, subAgentScope{depth: childDepth, key: run + "/" + taskID, registry: taskRegistries[taskID]}), release
		},
	}, agentTasks)

	// Roll up sub-agent costs into parent tracker
	// Isolated worktrees: removed when unchanged, kept (and reported) otherwise.
	for i := range results {
		if wt := worktrees[results[i].ID]; wt != nil {
			_, note := wt.Finish()
			logger.Info("[sub-agent:%s] %s", results[i].ID, note)
			results[i].Notice = strings.TrimSpace(results[i].Notice + " " + note)
		}
	}
	for _, r := range results {
		if r.InputTokens > 0 || r.OutputTokens > 0 || r.CostUSD > 0 {
			m := r.Model
			if m == "" {
				m = parentModel
			}
			a.costTracker.Track(&llm.Response{
				Model:        m,
				InputTokens:  r.InputTokens,
				OutputTokens: r.OutputTokens,
				CostUSD:      r.CostUSD,
			})
		}
	}

	// Convert agent results to tool results
	toolResults := make([]tools.SubTaskResult, len(results))
	for i, r := range results {
		toolResults[i] = tools.SubTaskResult{
			ID:           r.ID,
			Agent:        r.Agent,
			Model:        r.Model,
			Output:       r.Output,
			Error:        r.Error,
			Notice:       r.Notice,
			Partial:      r.Partial,
			CostUSD:      r.CostUSD,
			InputTokens:  r.InputTokens,
			OutputTokens: r.OutputTokens,
			AgentID:      r.AgentID,
		}
	}
	return toolResults
}

// loadSubAgentDefs reads the subagent definitions for workDir; skipped
// definitions are logged when logWarnings is set.
func (a *App) loadSubAgentDefs(workDir string, logWarnings bool) subagents.Set {
	userDir := ""
	if a.cfg != nil {
		userDir = filepath.Join(a.cfg.ConfigDir(), "agents")
	}
	defs := subagents.Load(workDir, userDir)
	if logWarnings {
		for _, w := range defs.Warnings {
			logger.Error("Subagent definition skipped: %s", w)
		}
	}
	return defs
}

// describeSubAgents lists the available subagent definitions for the
// spawn_agents tool description.
func (a *App) describeSubAgents() string {
	return a.loadSubAgentDefs(a.GetWorkDir(), false).Describe()
}

// describeSubAgentModels lists the models a sub-agent may be asked to run
// on: the active provider's model aliases (for Claude Code: opus, sonnet,
// haiku), like the aliases Claude Code offers on its Agent tool.
func (a *App) describeSubAgentModels() string {
	p := a.activeProvider()
	if p == nil {
		return ""
	}
	var b strings.Builder
	for _, m := range p.Models() {
		if m = strings.TrimSpace(m); m != "" {
			b.WriteString("- " + m + "\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func joinOrNone(names []string) string {
	if len(names) == 0 {
		return "(none defined)"
	}
	return strings.Join(names, ", ")
}

// WatchChat adds a JID or phone number to the event watch list.
// All messages from this chat are processed, including own messages.
// Called by the agent via the watch_chat tool.
func (a *App) WatchChat(jid string) string {
	a.watchedChatsMu.Lock()
	a.watchedChats[jid] = struct{}{}
	a.watchedChatsMu.Unlock()
	a.persistWatchedChats()
	a.updateTrayTooltip()
	logger.Info("Now watching chat: %s", jid)
	return fmt.Sprintf("Now watching %s for new messages", jid)
}

// UnwatchChat removes a JID from the event watch list.
func (a *App) UnwatchChat(jid string) string {
	a.watchedChatsMu.Lock()
	delete(a.watchedChats, jid)
	a.watchedChatsMu.Unlock()
	a.persistWatchedChats()
	a.updateTrayTooltip()
	logger.Info("Stopped watching chat: %s", jid)
	return fmt.Sprintf("Stopped watching %s", jid)
}

// persistWatchedChats saves the current watched JIDs to config so they survive restarts.
// TrackSentMessageID records a message ID so it can be filtered as echo when
// it arrives back via Trouter/event bus. Called by MCP tool wrappers after sending.
func (a *App) TrackSentMessageID(msgID string) {
	if msgID == "" {
		return
	}
	a.recentSentMu.Lock()
	a.recentSentIDs[msgID] = time.Now().Unix()
	a.recentSentMu.Unlock()
	logger.Debug("Tracking sent message ID: %s", msgID)
}

func (a *App) persistWatchedChats() {
	a.watchedChatsMu.RLock()
	jids := make([]string, 0, len(a.watchedChats))
	for jid := range a.watchedChats {
		jids = append(jids, jid)
	}
	a.watchedChatsMu.RUnlock()
	a.cfg.SetWatchedChats(jids)
	a.cfg.Save()
}

// GetWatchedChats returns the list of watched JIDs.
func (a *App) GetWatchedChats() []string {
	a.watchedChatsMu.RLock()
	defer a.watchedChatsMu.RUnlock()
	result := make([]string, 0, len(a.watchedChats))
	for jid := range a.watchedChats {
		result = append(result, jid)
	}
	return result
}

func (a *App) ClearChat() {
	logger.Info("Chat cleared")
	a.endSession("clear")

	// Capture messages before clearing for memory extraction
	if a.currentAgent != nil {
		msgs := a.currentAgent.Messages()
		if msgs != nil {
			msgsCopy := make([]llm.Message, len(msgs))
			copy(msgsCopy, msgs)
			go a.extractMemoriesFromSession(msgsCopy)
		}
	}

	a.currentAgent = nil
	a.costTracker.Reset()
	a.skills.reset()
	agent.ClearSession(a.cfg.ConfigDir())
	a.startSession("clear", false)
}

// extractMemoriesFromSession sends a truncated transcript to the LLM to extract
// key facts worth remembering. Runs in background, fire-and-forget.
func (a *App) extractMemoriesFromSession(msgs []llm.Message) {
	// Skip short sessions (fewer than 4 user messages)
	userMsgCount := 0
	for _, m := range msgs {
		if m.Role == llm.RoleUser {
			userMsgCount++
		}
	}
	if userMsgCount < 4 {
		return
	}

	// Build a truncated transcript (max ~4000 chars)
	var transcript strings.Builder
	for _, m := range msgs {
		if m.Role == llm.RoleSystem || m.Role == llm.RoleToolResult {
			continue
		}
		if m.Content == "" {
			continue
		}
		role := string(m.Role)
		line := role + ": " + m.Content + "\n"
		if transcript.Len()+len(line) > 4000 {
			break
		}
		transcript.WriteString(line)
	}

	if transcript.Len() < 100 {
		return
	}

	extractPrompt := `Analyze this conversation transcript and extract key facts worth remembering for future sessions.
Focus on:
- User preferences, role, or how they like to work (type: user_profile)
- Project decisions, context, or conventions (type: project)
- Per-contact notes or relationship context (type: contact)
- Corrections or guidance the user gave (type: feedback)

Return ONLY a JSON array (no markdown, no explanation). Each item:
{"name": "short_identifier", "type": "user_profile|project|contact|feedback", "description": "one-line summary", "content": "concise fact to remember"}

If nothing is worth remembering, return an empty array: []

Transcript:
` + transcript.String()

	provider := a.activeProvider()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := provider.Chat(ctx, []llm.Message{
		{Role: llm.RoleUser, Content: extractPrompt},
	}, a.cfg.DefaultModel())
	if err != nil {
		logger.Error("Memory extraction failed: %s", err.Error())
		return
	}

	// Parse JSON array from response
	type extractedMemory struct {
		Name        string `json:"name"`
		Type        string `json:"type"`
		Description string `json:"description"`
		Content     string `json:"content"`
	}

	// Find JSON array in response (may be wrapped in markdown code block)
	body := strings.TrimSpace(resp.Content)
	if idx := strings.Index(body, "["); idx >= 0 {
		if end := strings.LastIndex(body, "]"); end > idx {
			body = body[idx : end+1]
		}
	}

	var memories []extractedMemory
	if err := json.Unmarshal([]byte(body), &memories); err != nil {
		logger.Error("Memory extraction parse failed: %s", err.Error())
		return
	}

	for _, m := range memories {
		if m.Name == "" || m.Content == "" {
			continue
		}
		meta := memory.MemoryMeta{
			Type:        m.Type,
			Description: m.Description,
		}
		if err := a.memoryStore.SaveWithMeta(m.Name, meta, m.Content); err != nil {
			logger.Error("Failed to save extracted memory %q: %s", m.Name, err.Error())
		} else {
			logger.Info("Auto-extracted memory: %s [%s]", m.Name, m.Type)
		}
	}
}

func (a *App) GetCostSummary() llm.CostSummary {
	return a.costTracker.Summary()
}

// --- Marketplace ---

func (a *App) GetMarketplace() []marketplace.CatalogEntry {
	return marketplace.GetCatalog()
}

func (a *App) GetInstalledNames() []string {
	var names []string
	for _, s := range a.cfg.MCPServers() {
		names = append(names, s.Name)
	}
	return names
}

func (a *App) CheckNpx() bool {
	return marketplace.CheckNpx()
}

func (a *App) InstallMarketplace(name, secret string) string {
	entry := marketplace.GetByName(name)
	if entry == nil {
		return "unknown marketplace entry: " + name
	}

	// Check if already installed
	for _, s := range a.cfg.MCPServers() {
		if s.Name == entry.Name {
			return "already installed: " + entry.Name
		}
	}

	cfg := marketplace.Install(*entry, secret)
	return a.AddMCPServer(cfg.Name, cfg.Command, cfg.Args, cfg.Env, cfg.AutoStart, cfg.Subscribe, cfg.URL)
}

func (a *App) ResetCosts() {
	a.costTracker.Reset()
}

// GetLogPath returns the log file path for debugging
func (a *App) GetLogPath() string {
	return logger.Path()
}

// --- Event Bus ---

// GetEventSources returns the names of all registered event sources.
func (a *App) GetEventSources() []string {
	return a.eventBus.Sources()
}

// GetEventStats returns event bus statistics.
func (a *App) GetEventStats() eventbus.Stats {
	return a.eventBus.GetStats()
}

// --- MCP Servers ---

// MCPServerStatus holds the status of an MCP server for the UI.
type MCPServerStatus struct {
	Name      string `json:"name"`
	Command   string `json:"command"`
	URL       string `json:"url,omitempty"`
	AutoStart bool   `json:"auto_start"`
	Connected bool   `json:"connected"`
	Tools     int    `json:"tools"`
	Resources int    `json:"resources"`
	HasAuth   bool   `json:"has_auth"`
	NeedsAuth bool   `json:"needs_auth"`
}

// GetMCPServers returns configured MCP servers and their status.
func (a *App) GetMCPServers() []MCPServerStatus {
	configured := a.cfg.MCPServers()
	servers := make([]MCPServerStatus, 0, len(configured))
	for _, cfg := range configured {
		status := MCPServerStatus{
			Name:      cfg.Name,
			Command:   cfg.Command,
			URL:       cfg.URL,
			AutoStart: cfg.AutoStart,
		}
		if a.mcpManager != nil {
			conn, ok := a.mcpManager.GetConnection(cfg.Name)
			if ok && conn != nil {
				if conn.NeedsAuth() {
					status.NeedsAuth = true
				} else {
					status.Connected = true
					status.Tools = len(conn.Tools())
					status.Resources = len(conn.Resources())
					for _, t := range conn.Tools() {
						if t.Name == "get_auth_status" || t.Name == "get_qr_code" {
							status.HasAuth = true
							break
						}
					}
				}
			}
		}
		servers = append(servers, status)
	}
	return servers
}

// AddMCPServer adds a new MCP server configuration.
func (a *App) AddMCPServer(name, command string, args []string, env map[string]string, autoStart bool, subscribe []string, url string) string {
	server := config.MCPServerConfig{
		Name:      name,
		Command:   command,
		Args:      args,
		Env:       env,
		AutoStart: autoStart,
		Subscribe: subscribe,
		URL:       url,
	}
	if !a.cfg.AddMCPServer(server) {
		return "server with that name already exists"
	}
	if err := a.cfg.Save(); err != nil {
		return err.Error()
	}
	logger.Info("MCP server %q added", name)

	// Try to start the new server immediately, from the value just added
	// rather than by reading it back: a concurrent removal would have
	// invalidated the index.
	cfg := server
	if a.mcpManager != nil {
		go func() {
			if err := a.mcpManager.Reconnect(cfg); err != nil {
				logger.Info("MCP server %q: initial connect: %s", name, err.Error())
			} else {
				mcpclient.RegisterMCPTools(a.registry, a.mcpManager, a.TrackSentMessageID)
				a.currentAgent = nil
			}
			wailsRuntime.EventsEmit(a.ctx, "mcp_auth_done", map[string]string{"name": name})
		}()
	}
	return ""
}

// --- Sessions ---

// GetSessions returns all archived sessions.
func (a *App) GetSessions() []agent.SessionInfo {
	return agent.ListSessions(a.cfg.ConfigDir())
}

// ChatHistoryMessage is one visible conversation message for the UI.
type ChatHistoryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Images are the base64 images attached to a user message.
	Images []string `json:"images,omitempty"`
	// Attachments are the files attached to a user message.
	Attachments []llm.Attachment `json:"attachments,omitempty"`
	// Steps is set for role "tools": the tool calls of that part of the
	// conversation, so they are still visible after a restart.
	Steps []ChatHistoryStep `json:"steps,omitempty"`
}

// ChatHistoryStep is one tool call shown in a restored tool group.
type ChatHistoryStep struct {
	Tool    string `json:"tool"`
	Command string `json:"command"`
	Result  string `json:"result"`
	Status  string `json:"status"` // done, error, or interrupted (no result saved)
}

// GetChatHistory returns the agent's restored conversation for the UI:
// user/assistant text and the tool calls with their results, grouped the same
// way they are shown live. The system prompt and internal notes are left out.
func (a *App) GetChatHistory() []ChatHistoryMessage {
	ag := a.ensureAgent()
	return buildChatHistory(ag.Messages())
}

func buildChatHistory(msgs []llm.Message) []ChatHistoryMessage {
	out := []ChatHistoryMessage{}
	stepAt := map[string][2]int{} // tool call id -> (message index, step index)
	addTools := func(steps []ChatHistoryStep) [2]int {
		if n := len(out); n > 0 && out[n-1].Role == "tools" {
			out[n-1].Steps = append(out[n-1].Steps, steps...)
			return [2]int{n - 1, len(out[n-1].Steps) - len(steps)}
		}
		out = append(out, ChatHistoryMessage{Role: "tools", Steps: steps})
		return [2]int{len(out) - 1, 0}
	}
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			if m.Content == "" && len(m.Images) == 0 {
				continue
			}
			role := "user"
			// Restart notes are shown as a notice, not as something the user said.
			if strings.HasPrefix(m.Content, agent.RestartNotePrefix) {
				role = "notice"
			}
			out = append(out, ChatHistoryMessage{Role: role, Content: agent.DisplayContent(m), Images: m.Images, Attachments: m.Attachments})
		case llm.RoleAssistant:
			if m.Content != "" {
				out = append(out, ChatHistoryMessage{Role: "assistant", Content: m.Content})
			}
			if len(m.ToolCalls) > 0 {
				steps := make([]ChatHistoryStep, len(m.ToolCalls))
				for k, tc := range m.ToolCalls {
					steps[k] = ChatHistoryStep{Tool: tc.Name, Command: agent.ToolCallDesc(tc), Status: "interrupted"}
				}
				at := addTools(steps)
				for k, tc := range m.ToolCalls {
					stepAt[tc.ID] = [2]int{at[0], at[1] + k}
				}
			}
		case llm.RoleToolResult:
			at, ok := stepAt[m.ToolCallID]
			if !ok {
				continue
			}
			st := &out[at[0]].Steps[at[1]]
			st.Result = historyPreview(m.Content)
			st.Status = "done"
			if strings.HasPrefix(m.Content, "Error:") {
				st.Status = "error"
			}
		}
	}
	return out
}

// historyPreview keeps the first lines of a tool result, like the live view.
func historyPreview(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= 10 {
		return s
	}
	return strings.Join(lines[:10], "\n") + fmt.Sprintf("\n... (%d lines)", len(lines))
}

// ResumeSession loads an archived session by ID.
func (a *App) ResumeSession(id string) string {
	logger.Info("Resuming session: %s", id)
	a.endSession("resume")
	a.currentAgent = nil
	a.costTracker.Reset()
	ag := a.ensureAgent()
	if err := ag.LoadSessionByID(a.cfg.ConfigDir(), id); err != nil {
		return err.Error()
	}
	a.startSession("resume", false)
	return ""
}

// DeleteSession removes an archived session.
func (a *App) DeleteSession(id string) string {
	if err := agent.DeleteSession(a.cfg.ConfigDir(), id); err != nil {
		return err.Error()
	}
	return ""
}

// CallMCPTool calls a tool on a specific MCP server directly (without going through the agent).
// Returns the tool output as a string. Used for UI flows like WhatsApp QR pairing.
func (a *App) CallMCPTool(serverName, toolName string, args map[string]string) string {
	if a.mcpManager == nil {
		return `{"error":"no MCP manager"}`
	}
	// Convert string args to any
	mcpArgs := make(map[string]any, len(args))
	for k, v := range args {
		mcpArgs[k] = v
	}
	result, err := a.mcpManager.CallTool(a.ctx, serverName, toolName, mcpArgs)
	if err != nil {
		logger.Error("CallMCPTool %s.%s error: %s", serverName, toolName, err.Error())
		return `{"error":"` + err.Error() + `"}`
	}
	// Extract content
	output := mcpclient.ContentToString(result.Content)
	logger.Info("CallMCPTool %s.%s result: %s", serverName, toolName, output[:min(len(output), 120)])
	if result.IsError {
		return `{"error":"` + output + `"}`
	}
	return output
}

// --- Voice ---

// StartRecording begins capturing audio from the system microphone.
func (a *App) StartRecording() string {
	if err := a.recorder.Start(); err != nil {
		return err.Error()
	}
	return ""
}

// StopRecording stops capturing and transcribes the audio via Whisper.
func (a *App) StopRecording() map[string]interface{} {
	audioBase64, err := a.recorder.Stop()
	if err != nil {
		return map[string]interface{}{"error": err.Error()}
	}
	return a.TranscribeAudio(audioBase64)
}

// TranscribeAudio transcribes base64-encoded audio using local Whisper.
func (a *App) TranscribeAudio(audioBase64 string) map[string]interface{} {
	result := voice.Transcribe(audioBase64, a.cfg.STTModel(), a.cfg.STTLanguage(), "", a.cfg.ConfigDir())
	return map[string]interface{}{
		"text":  result.Text,
		"error": result.Error,
	}
}

// SpeakText converts text to speech (Piper, falling back to espeak-ng). While
// the sound switch is off nothing is synthesized and "muted" is true.
func (a *App) SpeakText(text string) map[string]interface{} {
	// While sound is off no speech is generated at all.
	ctx, ok := a.speechContext()
	if !ok {
		return map[string]interface{}{"audio_base64": "", "format": "", "char_count": 0, "error": "", "muted": true}
	}
	result := voice.Speak(ctx, text, normalizeVoice(a.cfg.TTSVoice()), a.cfg.ConfigDir())
	if ctx.Err() != nil {
		// Sound was turned off while synthesizing: the audio is discarded.
		return map[string]interface{}{"audio_base64": "", "format": "", "char_count": 0, "error": "", "muted": true}
	}
	return map[string]interface{}{
		"audio_base64": result.AudioBase64,
		"format":       result.Format,
		"char_count":   result.CharCount,
		"error":        result.Error,
		"muted":        false,
	}
}

// speechContext returns the context speech synthesis must run under, or false
// when sound is off.
func (a *App) speechContext() (context.Context, bool) {
	a.speakMu.Lock()
	defer a.speakMu.Unlock()
	if !a.cfg.VoiceEnabled() {
		return nil, false
	}
	if a.speakCtx == nil {
		a.speakCtx, a.speakCancel = context.WithCancel(context.Background())
	}
	return a.speakCtx, true
}

// StopSpeaking aborts the speech being synthesized right now (Stop button /
// Escape) without touching the sound switch; the next utterance starts afresh.
func (a *App) StopSpeaking() {
	a.speakMu.Lock()
	defer a.speakMu.Unlock()
	if a.speakCancel != nil {
		a.speakCancel()
		a.speakCtx, a.speakCancel = nil, nil
	}
}

// GetWorkDir returns the directory the agent saves files into (the process
// working directory), so the frontend can resolve a bare file name the agent
// mentions into a full path to open. Empty string if it can't be determined.
func (a *App) GetWorkDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// OpenPath opens a local file (or folder) with the OS default application, so a
// link to a file the agent generated opens the document directly. Returns "" on
// success or an error message. The path must exist (no opening of arbitrary
// strings); URLs are left to the browser via BrowserOpenURL on the frontend.
func (a *App) OpenPath(path string) string {
	path = strings.TrimPrefix(path, "file://")
	path = strings.TrimSpace(path)
	if path == "" {
		return "empty path"
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Sprintf("not found: %s", path)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Start(); err != nil {
		logger.Error("OpenPath: %s — %s", path, err.Error())
		return err.Error()
	}
	logger.Info("OpenPath: opened %s", path)
	return ""
}

const defaultVoice = "es_ES-davefx-medium"

// normalizeVoice maps a stored value to a full Piper voice code, tolerating
// legacy short codes (e.g. "" or "es_ES" -> the default). Full codes (which
// contain a "-", e.g. "en_US-amy-medium") are kept as-is.
func normalizeVoice(v string) string {
	if strings.Contains(v, "-") {
		return v
	}
	return defaultVoice
}

// GetTTSVoice returns the configured TTS voice code.
func (a *App) GetTTSVoice() string {
	return normalizeVoice(a.cfg.TTSVoice())
}

// GetTTSVoices returns the online Piper voice catalog with per-voice install state.
func (a *App) GetTTSVoices() []map[string]interface{} {
	dir := a.cfg.ConfigDir()
	cat, err := piper.Catalog(dir)
	if err != nil {
		logger.Error("Piper: catalog fetch failed: %s", err.Error())
		return nil
	}
	out := make([]map[string]interface{}, 0, len(cat))
	for _, v := range cat {
		out = append(out, map[string]interface{}{
			"code":      v.Code,
			"name":      v.Name,
			"language":  v.Language,
			"quality":   v.Quality,
			"installed": piper.VoiceInstalled(dir, v.Code),
		})
	}
	return out
}

// PiperSupported reports whether neural (Piper) TTS is available on this platform.
func (a *App) PiperSupported() bool { return piper.Supported() }

// SetTTSVoice persists the chosen voice and downloads the Piper binary + that
// voice model on demand (so selecting a language fetches what it needs).
// Returns an error string ("" on success) the UI can surface.
func (a *App) SetTTSVoice(v string) string {
	v = normalizeVoice(v)
	a.cfg.SetTTSVoice(v)
	if err := a.cfg.Save(); err != nil {
		return err.Error()
	}
	if !piper.Supported() {
		return "" // espeak-ng fallback, nothing to download
	}
	if err := piper.EnsureBinary(a.cfg.ConfigDir()); err != nil {
		logger.Error("Piper: ensure binary failed: %s", err.Error())
		return err.Error()
	}
	if err := piper.EnsureVoice(a.cfg.ConfigDir(), v); err != nil {
		logger.Error("Piper: ensure voice failed: %s", err.Error())
		return err.Error()
	}
	return ""
}

// GetSTTLanguage returns the configured STT language (empty or "auto" = auto-detect).
func (a *App) GetSTTLanguage() string {
	if lang := a.cfg.STTLanguage(); lang != "" {
		return lang
	}
	return "auto"
}

// SetSTTLanguage sets the STT language and persists.
func (a *App) SetSTTLanguage(lang string) error {
	a.cfg.SetSTTLanguage(lang)
	return a.cfg.Save()
}

// GetWakeWord returns the configured wake word (name that triggers listening).
func (a *App) GetWakeWord() string { return a.cfg.WakeWord() }

// SetWakeWord persists the wake word.
func (a *App) SetWakeWord(word string) error {
	a.cfg.SetWakeWord(strings.TrimSpace(word))
	return a.cfg.Save()
}

// GetWakeListening reports whether the continuous wake-word listener is running.
func (a *App) GetWakeListening() bool {
	return a.wake != nil && a.wake.Running()
}

// SetWakeListening turns the continuous wake-word listener on or off. Session
// state only — it does not persist, so the mic is never auto-enabled on launch.
func (a *App) SetWakeListening(on bool) string {
	if a.wake == nil {
		return "wake listener unavailable"
	}
	if !on {
		a.wake.Stop()
		return ""
	}
	if strings.TrimSpace(a.cfg.WakeWord()) == "" {
		return "Set a wake word in Settings first"
	}
	a.wake.Start()
	return ""
}

// SetWakePaused suspends/resumes listening (the frontend pauses it while the
// agent is replying or TTS is playing, so it doesn't hear the assistant).
func (a *App) SetWakePaused(paused bool) {
	if a.wake != nil {
		a.wake.SetPaused(paused)
	}
}

// GetVoiceEnabled returns whether the app's sound is on: the assistant's
// voice and every other sound OpenUAI produces. The microphone is unaffected.
func (a *App) GetVoiceEnabled() bool {
	// Enabled by default; the configuration is what says otherwise.
	return a.cfg.VoiceEnabled()
}

// GetAudioDevices returns available microphone devices.
func (a *App) GetAudioDevices() []voice.AudioDevice {
	return voice.ListDevices()
}

// GetAudioDevice returns the configured audio device ID.
func (a *App) GetAudioDevice() string {
	return a.cfg.AudioDevice()
}

// SetAudioDevice sets the audio input device and persists.
func (a *App) SetAudioDevice(deviceID string) error {
	a.cfg.SetAudioDevice(deviceID)
	a.recorder.Device = deviceID
	return a.cfg.Save()
}

// SetVoiceEnabled is the app-wide sound switch. Turning it off aborts any
// speech still being synthesized and silences every other app sound; turning
// it on only affects what happens from then on. The state is persisted.
func (a *App) SetVoiceEnabled(enabled bool) error {
	a.speakMu.Lock()
	a.cfg.SetVoiceEnabled(enabled)
	if !enabled && a.speakCancel != nil {
		a.speakCancel()
		a.speakCtx, a.speakCancel = nil, nil
	}
	a.speakMu.Unlock()
	tray.SetSoundEnabled(enabled)
	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "sound_enabled", enabled)
	}
	logger.Info("Sound enabled: %v", enabled)
	return a.cfg.Save()
}

// --- Update ---

// GetVersion returns the current app version.
func (a *App) GetVersion() string {
	return a.version
}

// ApplyUpdate downloads and installs the update, then signals the frontend.
func (a *App) ApplyUpdate(downloadURL string) string {
	if err := updater.DownloadAndApply(downloadURL); err != nil {
		logger.Error("Update failed: %s", err.Error())
		return err.Error()
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		os.Exit(42)
	}()
	return ""
}

// SkipVersion saves the given version as skipped so it won't prompt again.
func (a *App) SkipVersion(version string) {
	a.cfg.SetSkippedVersion(version)
	a.cfg.Save()
	logger.Info("Skipped update version: %s", version)
}

// --- Beta Features ---

// GetBetaLipReading returns whether the lip reading beta is enabled.
func (a *App) GetBetaLipReading() bool {
	return a.cfg.BetaLipReading()
}

// SetBetaLipReading enables or disables the lip reading beta.
func (a *App) SetBetaLipReading(enabled bool) error {
	a.cfg.SetBetaLipReading(enabled)
	logger.Info("Beta lip reading: %v", enabled)
	return a.cfg.Save()
}

// --- Computer Use ---

// GetComputerUseEnabled reports whether computer-use (screen control) is on.
func (a *App) GetComputerUseEnabled() bool { return a.cfg.ComputerUseEnabled() }

// GetComputerUseMonitor returns the configured monitor index.
func (a *App) GetComputerUseMonitor() int { return a.cfg.ComputerUseMonitor() }

// SetComputerUseMonitor sets which monitor the agent controls and re-registers tools.
func (a *App) SetComputerUseMonitor(monitor int) error {
	a.cfg.SetComputerUseMonitor(monitor)
	if a.cfg.ComputerUseEnabled() {
		computeruse.RegisterTools(a.registry, computeruse.New(a.GetComputerUseDisplay(), monitor, a.cfg.ComputerUseProfile()))
	}
	a.currentAgent = nil
	logger.Info("Computer-use monitor set to %d", monitor)
	return a.cfg.Save()
}

// GetComputerUseDisplay returns the configured X display (default ":0").
func (a *App) GetComputerUseDisplay() string {
	if display := a.cfg.ComputerUseDisplay(); display != "" {
		return display
	}
	return ":0"
}

// SetComputerUseDisplay sets the X display the agent controls (e.g. ":0" or ":99").
func (a *App) SetComputerUseDisplay(display string) error {
	a.cfg.SetComputerUseDisplay(display)
	if a.cfg.ComputerUseEnabled() {
		// Re-register the computer_* tools bound to the new display (Register
		// overwrites by name), so the change takes effect without a restart.
		computeruse.RegisterTools(a.registry, computeruse.New(display, a.cfg.ComputerUseMonitor(), a.cfg.ComputerUseProfile()))
	}
	a.currentAgent = nil // rebuild with new display
	logger.Info("Computer-use display set to %s", display)
	return a.cfg.Save()
}

// SetComputerUseEnabled toggles computer-use. Enabling registers the screen-control
// tools immediately; disabling takes effect for new agent runs.
func (a *App) SetComputerUseEnabled(enabled bool) error {
	a.cfg.SetComputerUseEnabled(enabled)
	if enabled {
		disp := a.GetComputerUseDisplay()
		computeruse.RegisterTools(a.registry, computeruse.New(disp, a.cfg.ComputerUseMonitor(), a.cfg.ComputerUseProfile()))
		logger.Info("Computer-use enabled (display %s)", disp)
	} else {
		logger.Info("Computer-use disabled (effective next restart)")
	}
	a.currentAgent = nil // rebuild so the tool set changes take effect
	return a.cfg.Save()
}

// CheckComputerUseDeps verifies the Linux/X11 dependencies are available.
// Returns an empty string if OK, or a human-readable problem description.
func (a *App) CheckComputerUseDeps() string {
	if runtime.GOOS != "linux" {
		return "Computer use currently requires Linux/X11"
	}
	missing := []string{}
	for _, bin := range []string{"xdotool", "import"} {
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}
	if len(missing) > 0 {
		return "Missing dependencies: " + strings.Join(missing, ", ") + " (install xdotool and imagemagick)"
	}
	return ""
}

// --- Lip Reading ---

// LipReadingModelReady returns whether the lip reading model is downloaded.
func (a *App) LipReadingModelReady() bool {
	return lipreading.IsModelReady(a.cfg.ConfigDir())
}

// DownloadLipReadingModel downloads the model and emits progress events.
func (a *App) DownloadLipReadingModel() string {
	// First ensure repo and python deps
	if err := lipreading.EnsureRepo(a.cfg.ConfigDir()); err != nil {
		return err.Error()
	}
	if err := lipreading.EnsurePythonDeps(); err != nil {
		return err.Error()
	}
	// Download model with progress
	err := lipreading.DownloadModel(a.cfg.ConfigDir(), func(downloaded int64) {
		wailsRuntime.EventsEmit(a.ctx, "lipreading_download_progress", downloaded)
	})
	if err != nil {
		return err.Error()
	}
	return ""
}

// StartLipRecording begins capturing video from the webcam.
func (a *App) StartLipRecording() string {
	if err := a.lipRecorder.Start(); err != nil {
		return err.Error()
	}
	return ""
}

// StopLipRecording stops video capture and runs lip reading inference.
func (a *App) StopLipRecording() map[string]interface{} {
	videoBase64, err := a.lipRecorder.Stop()
	if err != nil {
		return map[string]interface{}{"text": "", "error": err.Error()}
	}
	transcript, err := lipreading.Transcribe(videoBase64, a.cfg.ConfigDir())
	if err != nil {
		return map[string]interface{}{"text": "", "error": err.Error()}
	}
	return map[string]interface{}{"text": transcript, "error": ""}
}

// AuthMCPServer triggers the OAuth flow for a server that needs authentication.
func (a *App) AuthMCPServer(name string) string {
	if a.mcpManager == nil {
		return "MCP manager not running"
	}
	go func() {
		if err := a.mcpManager.AuthenticateServer(name); err != nil {
			logger.Error("MCP auth %q failed: %s", name, err.Error())
			wailsRuntime.EventsEmit(a.ctx, "mcp_auth_done", map[string]string{"name": name, "error": err.Error()})
		} else {
			mcpclient.RegisterMCPTools(a.registry, a.mcpManager, a.TrackSentMessageID)
			a.currentAgent = nil
			logger.Info("MCP server %q authenticated, %d total tools", name, len(a.registry.Definitions()))
			wailsRuntime.EventsEmit(a.ctx, "mcp_auth_done", map[string]string{"name": name})
		}
	}()
	return ""
}

// ReauthMCPServer clears saved tokens and reconnects an HTTP MCP server, triggering OAuth.
func (a *App) ReauthMCPServer(name string) string {
	cfg, found := a.cfg.SetMCPServerTokens(name, nil)
	if !found {
		return "server not found"
	}
	a.cfg.Save()

	if a.mcpManager == nil {
		return "MCP manager not running"
	}

	logger.Info("MCP server %q: clearing tokens and reconnecting", name)
	go func() {
		if err := a.mcpManager.Reconnect(cfg); err != nil {
			logger.Error("MCP server %q reconnect failed: %s", name, err.Error())
			wailsRuntime.EventsEmit(a.ctx, "mcp_auth_done", map[string]string{"name": name, "error": err.Error()})
		} else {
			mcpclient.RegisterMCPTools(a.registry, a.mcpManager, a.TrackSentMessageID)
			a.currentAgent = nil
			logger.Info("MCP server %q reconnected, %d total tools", name, len(a.registry.Definitions()))
			wailsRuntime.EventsEmit(a.ctx, "mcp_auth_done", map[string]string{"name": name})
		}
	}()
	return ""
}

// RemoveMCPServer removes an MCP server configuration by name.
func (a *App) RemoveMCPServer(name string) string {
	if !a.cfg.RemoveMCPServer(name) {
		return "server not found"
	}
	if err := a.cfg.Save(); err != nil {
		return err.Error()
	}
	logger.Info("MCP server %q removed", name)
	return ""
}
