<script>
  import { SendMessage, SetAPIKey, HasAPIKey, GetModels, GetDefaultModel, SetDefaultModel, ClearChat, GetProvider, SetProvider, GetProviders, OpenAILogin, OpenAIIsLoggedIn, RespondPermission, GetEventStats, GetMCPServers, AddMCPServer, RemoveMCPServer, GetSessions, ResumeSession, DeleteSession, CallMCPTool, StartRecording, StopRecording, SpeakText, GetTTSVoice, SetTTSVoice, GetVoiceEnabled, SetVoiceEnabled, GetAudioDevices, GetAudioDevice, SetAudioDevice, GetSTTLanguage, SetSTTLanguage } from '../wailsjs/go/main/App';
  import { EventsOn } from '../wailsjs/runtime/runtime';
  import { onMount, afterUpdate } from 'svelte';
  import { marked } from 'marked';

  marked.setOptions({
    breaks: true,
    gfm: true,
  });

  let messages = [];
  let input = '';
  let loading = false;
  let inputHistory = JSON.parse(localStorage.getItem('inputHistory') || '[]');
  let historyIndex = -1;
  let savedInput = '';
  let loggingIn = false;
  let apiKey = '';
  let hasKey = false;
  let showSettings = false;
  let models = [];
  let selectedModel = '';
  let totalCost = 0;
  let totalTokens = 0;
  let chatEl;
  let textareaEl;
  let provider = 'openai';
  let providers = [];
  let openaiLoggedIn = false;

  // Permission dialog
  let showPermDialog = false;
  let permTool = '';
  let permCommand = '';

  // Events panel
  let showEvents = false;
  let eventLog = [];
  let eventStats = null;

  // MCP servers
  let mcpServers = [];

  // Sessions
  let showSessions = false;
  let sessions = [];
  let mcpNewName = '';
  let mcpNewCommand = '';
  let mcpNewArgs = '';
  let mcpNewSubscribe = '';
  let mcpAdding = false;
  let mcpQrDialog = false;
  let mcpQrImage = '';
  let mcpQrLoading = false;
  let mcpQrServer = '';

  // Voice
  let recording = false;
  let transcribing = false;
  let voiceLevel = 0;
  let audioDevices = [];
  let selectedDevice = '';
  let speaking = false;
  let voiceEnabled = true;
  let ttsVoice = 'es';
  const ttsVoices = ['es', 'en', 'fr', 'de', 'it', 'pt', 'ja', 'zh'];
  let sttLanguage = 'auto';
  const sttLanguages = [
    { code: 'auto', label: 'Auto-detect' },
    { code: 'es', label: 'Español' },
    { code: 'en', label: 'English' },
    { code: 'fr', label: 'Français' },
    { code: 'de', label: 'Deutsch' },
    { code: 'it', label: 'Italiano' },
    { code: 'pt', label: 'Português' },
    { code: 'ja', label: '日本語' },
    { code: 'zh', label: '中文' },
    { code: 'ko', label: '한국어' },
    { code: 'ar', label: 'العربية' },
    { code: 'ru', label: 'Русский' },
    { code: 'nl', label: 'Nederlands' },
    { code: 'ca', label: 'Català' },
  ];

  $: isReady = provider === 'openai' ? openaiLoggedIn : hasKey;

  onMount(async () => {
    providers = await GetProviders();
    provider = await GetProvider();
    hasKey = await HasAPIKey();
    openaiLoggedIn = await OpenAIIsLoggedIn();
    models = await GetModels();
    selectedModel = await GetDefaultModel();
    voiceEnabled = await GetVoiceEnabled();
    ttsVoice = await GetTTSVoice();
    sttLanguage = await GetSTTLanguage();
    audioDevices = await GetAudioDevices() || [];
    selectedDevice = await GetAudioDevice();
    if (!isReady) showSettings = true;

    // Listen for agent steps — group consecutive tool calls into one collapsible block
    // Tool group uses 'active' flag to keep showing the last tool until the group is finished
    EventsOn('agent_step', (step) => {
      if (step.type === 'event') {
        // Silent event injection — don't show in chat
        return;
      }
      if (step.type === 'text' || step.type === 'done') {
        // Finalize any active tool group
        const last = messages[messages.length - 1];
        if (last && last.role === 'tools' && last.active) {
          last.active = false;
          messages = [...messages.slice(0, -1), { ...last }];
        }
        if (step.content) {
          messages = [...messages, { role: 'assistant', content: step.content }];
        }
      } else if (step.type === 'tool_call') {
        const last = messages[messages.length - 1];
        const entry = { tool: step.tool_name, command: step.content, status: 'running' };
        if (last && last.role === 'tools') {
          last.steps = [...last.steps, entry];
          last.active = true;
          messages = [...messages.slice(0, -1), { ...last }];
        } else {
          messages = [...messages, { role: 'tools', steps: [entry], expanded: false, active: true }];
        }
      } else if (step.type === 'tool_result') {
        const last = messages[messages.length - 1];
        if (last && last.role === 'tools' && last.steps.length > 0) {
          const current = last.steps[last.steps.length - 1];
          let output = step.content.replace(/^Tool \S+ result:\n/, '');
          const lines = output.split('\n');
          const preview = lines.slice(0, 10).join('\n') + (lines.length > 10 ? '\n... (' + lines.length + ' lines)' : '');
          current.result = preview;
          current.status = 'done';
          // Keep active — still show this tool until a new tool_call or text/done arrives
          messages = [...messages.slice(0, -1), { ...last }];
        }
      } else if (step.type === 'error') {
        const last = messages[messages.length - 1];
        if (last && last.role === 'tools' && last.steps.length > 0) {
          const current = last.steps[last.steps.length - 1];
          if (current.status === 'running') {
            current.status = 'error';
            current.result = step.content;
            messages = [...messages.slice(0, -1), { ...last }];
            return;
          }
        }
        messages = [...messages, { role: 'error', content: step.content }];
      }
    });

    // Listen for event bus events
    EventsOn('event_received', (event) => {
      eventLog = [...eventLog.slice(-99), event];
    });

    // Listen for voice level updates
    EventsOn('voice_level', (level) => {
      voiceLevel = level;
    });

    // Listen for permission requests
    EventsOn('permission_request', (req) => {
      permTool = req.tool;
      permCommand = req.command;
      showPermDialog = true;
    });

    // Focus textarea when window gains focus
    window.addEventListener('focus', () => {
      if (textareaEl && !showPermDialog) textareaEl.focus();
    });
  });

  afterUpdate(() => {
    if (chatEl) chatEl.scrollTop = chatEl.scrollHeight;
  });

  function respondPerm(level, approved) {
    showPermDialog = false;
    RespondPermission(level, approved);
  }

  async function changeProvider() {
    await SetProvider(provider);
    models = await GetModels();
    selectedModel = models[0] || '';
    await SetDefaultModel(selectedModel);
  }

  async function loginOpenAI() {
    loggingIn = true;
    const err = await OpenAILogin();
    loggingIn = false;
    if (err) {
      alert('Login failed: ' + err);
    } else {
      openaiLoggedIn = true;
      showSettings = false;
    }
  }

  async function saveApiKey() {
    if (!apiKey) return;
    await SetAPIKey(apiKey);
    hasKey = true;
    apiKey = '';
    showSettings = false;
  }

  async function changeModel() {
    await SetDefaultModel(selectedModel);
  }

  async function send() {
    if (!input.trim() || loading) return;
    const content = input.trim();
    inputHistory = [...inputHistory, content];
    // Keep last 100 entries
    if (inputHistory.length > 100) inputHistory = inputHistory.slice(-100);
    localStorage.setItem('inputHistory', JSON.stringify(inputHistory));
    historyIndex = -1;
    savedInput = '';
    input = '';
    messages = [...messages, { role: 'user', content }];
    loading = true;

    const resp = await SendMessage(content);
    loading = false;

    totalCost = resp.cost_usd || 0;
    totalTokens = (resp.input_tokens || 0) + (resp.output_tokens || 0);
  }

  async function clearChat() {
    await ClearChat();
    messages = [];
    totalCost = 0;
    totalTokens = 0;
  }

  async function refreshEventStats() {
    eventStats = await GetEventStats();
  }

  async function toggleEvents() {
    showEvents = !showEvents;
    if (showEvents) {
      await refreshEventStats();
      mcpServers = await GetMCPServers();
    }
  }

  async function toggleSessions() {
    showSessions = !showSessions;
    if (showSessions) sessions = await GetSessions();
  }

  async function resumeSession(id) {
    const err = await ResumeSession(id);
    if (err) {
      alert('Failed to resume: ' + err);
      return;
    }
    messages = [{ role: 'assistant', content: '*Session restored. Continue the conversation.*' }];
    showSessions = false;
    totalCost = 0;
    totalTokens = 0;
  }

  async function deleteSession(id) {
    await DeleteSession(id);
    sessions = await GetSessions();
  }

  async function refreshMCPServers() {
    mcpServers = await GetMCPServers();
  }

  async function addMCPServer() {
    if (!mcpNewName || !mcpNewCommand) return;
    const args = mcpNewArgs ? mcpNewArgs.split(' ').filter(Boolean) : [];
    const subscribe = mcpNewSubscribe ? mcpNewSubscribe.split(',').map(s => s.trim()).filter(Boolean) : [];
    const err = await AddMCPServer(mcpNewName, mcpNewCommand, args, {}, true, subscribe);
    if (err) {
      alert('Failed to add MCP server: ' + err);
    } else {
      mcpNewName = '';
      mcpNewCommand = '';
      mcpNewArgs = '';
      mcpNewSubscribe = '';
      mcpAdding = false;
      await refreshMCPServers();
    }
  }

  async function linkMCPServer(name) {
    mcpQrServer = name;
    mcpQrLoading = true;
    mcpQrImage = '';
    mcpQrDialog = true;
    try {
      // Generic auth convention: try get_auth_status first, fall back to get_qr_code
      let result = await CallMCPTool(name, 'get_auth_status', {});
      let parsed;
      try { parsed = JSON.parse(result); } catch(e) { parsed = null; }

      mcpQrLoading = false;
      if (parsed && parsed.status) {
        if (parsed.status === 'paired') {
          mcpQrDialog = false;
          alert('Already paired!');
          await refreshMCPServers();
        } else if (parsed.status === 'error') {
          mcpQrDialog = false;
          alert(parsed.message || 'Auth error');
        } else if (parsed.data) {
          // QR image data (base64 PNG)
          mcpQrImage = parsed.data;
        } else {
          mcpQrDialog = false;
          alert('Status: ' + parsed.status + '. Scan QR in the bridge terminal.');
        }
      } else {
        // Fallback: try legacy get_qr_code tool
        result = await CallMCPTool(name, 'get_qr_code', {});
        mcpQrLoading = false;
        const match = result.match(/data:image\/[^;]+;base64,([A-Za-z0-9+/=]+)/);
        if (match) {
          mcpQrImage = match[0];
        } else if (result.includes('Already paired')) {
          mcpQrDialog = false;
          alert('Already paired!');
          await refreshMCPServers();
        } else {
          mcpQrDialog = false;
          alert('This server does not support pairing via UI.');
        }
      }
    } catch (e) {
      mcpQrLoading = false;
      mcpQrDialog = false;
      alert('This server does not support pairing via UI.');
    }
  }

  async function removeMCPServer(name) {
    const err = await RemoveMCPServer(name);
    if (err) alert('Failed to remove: ' + err);
    await refreshMCPServers();
  }

  async function startRecording() {
    if (recording || transcribing || loading) return;
    const err = await StartRecording();
    if (err) {
      alert('Recording error: ' + err);
      return;
    }
    recording = true;
  }

  async function stopRecordingAndSend() {
    if (!recording) return;
    recording = false;
    voiceLevel = 0;
    // Keep recording 1s more to capture trailing words
    await new Promise(r => setTimeout(r, 1000));
    transcribing = true;
    const result = await StopRecording();
    transcribing = false;
    if (result.error) return;
    if (result.text) {
      input = result.text;
      await send();
    }
  }

  async function speakMessage(text) {
    if (speaking) return;
    // Strip markdown for cleaner speech
    const clean = text.replace(/[#*_`~\[\]()>|]/g, '').replace(/\n+/g, ' ').trim();
    if (!clean) return;

    speaking = true;
    const result = await SpeakText(clean);
    speaking = false;

    if (result.error) {
      alert('TTS failed: ' + result.error);
      return;
    }

    const fmt = result.format || 'wav';
    const audio = new Audio('data:audio/' + fmt + ';base64,' + result.audio_base64);
    audio.play();
  }

  async function changeTTSVoice() {
    await SetTTSVoice(ttsVoice);
  }

  async function changeSTTLanguage() {
    await SetSTTLanguage(sttLanguage);
  }

  function handleKeydown(e) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      send();
    } else if (e.key === 'ArrowUp' && inputHistory.length > 0) {
      e.preventDefault();
      if (historyIndex === -1) {
        savedInput = input;
        historyIndex = inputHistory.length - 1;
      } else if (historyIndex > 0) {
        historyIndex--;
      }
      input = inputHistory[historyIndex];
    } else if (e.key === 'ArrowDown' && historyIndex !== -1) {
      e.preventDefault();
      if (historyIndex < inputHistory.length - 1) {
        historyIndex++;
        input = inputHistory[historyIndex];
      } else {
        historyIndex = -1;
        input = savedInput;
      }
    }
  }
</script>

<main>
  <header>
    <h1>OpenUAI</h1>
    <div class="header-right">
      <span class="cost-badge" title="Total tokens: {totalTokens}">
        ${totalCost.toFixed(4)}
      </span>
      <button class="icon-btn" class:icon-btn-active={showSessions} on:click={toggleSessions}>Sessions</button>
      <button class="icon-btn" class:icon-btn-active={showEvents} on:click={toggleEvents}>Events{eventLog.length > 0 ? ` (${eventLog.length})` : ''}</button>
      <button class="icon-btn" on:click={async () => { showSettings = !showSettings; if (showSettings) await refreshMCPServers(); }}>Settings</button>
    </div>
  </header>

  {#if showSettings}
    <div class="settings">
      <div class="setting-row">
        <label>Provider</label>
        <select bind:value={provider} on:change={changeProvider}>
          {#each providers as p}
            <option value={p}>{p === 'openai' ? 'OpenAI (ChatGPT subscription)' : 'Claude (API key)'}</option>
          {/each}
        </select>
      </div>

      {#if provider === 'openai'}
        <div class="setting-row">
          <label>Account</label>
          {#if openaiLoggedIn}
            <span class="status-ok">Logged in</span>
          {:else}
            <button on:click={loginOpenAI} disabled={loggingIn}>
              {loggingIn ? 'Opening browser...' : 'Login with ChatGPT'}
            </button>
          {/if}
        </div>
      {:else}
        <div class="setting-row">
          <label>API Key</label>
          <input type="password" bind:value={apiKey} placeholder={hasKey ? 'Key saved' : 'sk-ant-...'} />
          <button on:click={saveApiKey}>Save</button>
        </div>
      {/if}

      <div class="setting-row">
        <label>Model</label>
        <select bind:value={selectedModel} on:change={changeModel}>
          {#each models as model}
            <option value={model}>{model}</option>
          {/each}
        </select>
      </div>

      <div class="setting-row">
        <label>Voice</label>
        <select bind:value={ttsVoice} on:change={changeTTSVoice}>
          {#each ttsVoices as v}
            <option value={v}>{v}</option>
          {/each}
        </select>
      </div>

      <div class="setting-row">
        <label>STT Language</label>
        <select bind:value={sttLanguage} on:change={changeSTTLanguage}>
          {#each sttLanguages as lang}
            <option value={lang.code}>{lang.label}</option>
          {/each}
        </select>
      </div>

      {#if audioDevices.length > 0}
        <div class="setting-row">
          <label>Mic</label>
          <select bind:value={selectedDevice} on:change={() => SetAudioDevice(selectedDevice)}>
            <option value="">Default</option>
            {#each audioDevices as dev}
              <option value={dev.id}>{dev.name}</option>
            {/each}
          </select>
        </div>
      {/if}

      <div class="mcp-settings">
        <div class="mcp-settings-header">
          <span class="mcp-settings-title">MCP Servers</span>
          <button class="mcp-add-btn" on:click={() => mcpAdding = !mcpAdding}>{mcpAdding ? 'Cancel' : '+ Add'}</button>
        </div>
        {#if mcpAdding}
          <div class="mcp-add-form">
            <div class="setting-row">
              <label>Name</label>
              <input bind:value={mcpNewName} placeholder="whatsapp" />
            </div>
            <div class="setting-row">
              <label>Command</label>
              <input bind:value={mcpNewCommand} placeholder="uv" />
            </div>
            <div class="setting-row">
              <label>Args</label>
              <input bind:value={mcpNewArgs} placeholder="run main.py (space-separated)" />
            </div>
            <div class="setting-row">
              <label>Subscribe</label>
              <input bind:value={mcpNewSubscribe} placeholder="whatsapp://messages/inbox (optional)" />
            </div>
            <div class="setting-row">
              <label></label>
              <button on:click={addMCPServer} disabled={!mcpNewName || !mcpNewCommand}>Save</button>
            </div>
          </div>
        {/if}
        {#if mcpServers.length === 0 && !mcpAdding}
          <div class="mcp-empty">No MCP servers configured</div>
        {/if}
        {#each mcpServers as srv}
          <div class="mcp-settings-row">
            <span class="mcp-srv-dot" class:mcp-srv-dot-on={srv.connected}></span>
            <span class="mcp-srv-name">{srv.name}</span>
            <span class="mcp-srv-detail">{srv.command}</span>
            {#if srv.connected}
              <span class="mcp-srv-stats">{srv.tools}T {srv.resources}R</span>
            {/if}
            <button class="mcp-link-btn" on:click={() => linkMCPServer(srv.name)}>Link</button>
            <button class="mcp-remove-btn" on:click={() => removeMCPServer(srv.name)}>x</button>
          </div>
        {/each}
      </div>
    </div>
  {/if}

  {#if showEvents}
    <div class="events-panel">
      <div class="events-toolbar">
        <button class="events-refresh-btn" on:click={refreshEventStats}>Refresh Stats</button>
        <button class="events-clear-btn" on:click={() => { eventLog = []; }}>Clear Log</button>
      </div>
      {#if eventStats}
        <div class="events-stats">
          <span>Received: <b>{eventStats.events_received}</b></span>
          <span>Handled: <b>{eventStats.events_handled}</b></span>
          <span>Dropped: <b>{eventStats.events_dropped}</b></span>
          <span>Errors: <b>{eventStats.errors}</b></span>
        </div>
      {/if}
      {#if mcpServers.length > 0}
        <div class="mcp-servers">
          <div class="mcp-header">MCP Servers</div>
          {#each mcpServers as srv}
            <div class="mcp-server-entry">
              <span class="mcp-server-name">{srv.name}</span>
              <span class="mcp-server-status" class:mcp-connected={srv.connected}>{srv.connected ? 'connected' : 'disconnected'}</span>
              <span class="mcp-server-info">{srv.tools} tools, {srv.resources} resources</span>
            </div>
          {/each}
        </div>
      {/if}
      <div class="events-log">
        {#if eventLog.length === 0}
          <div class="events-empty">No events yet. Events from connectors will appear here.</div>
        {/if}
        {#each eventLog as evt}
          {#if evt.type === 'message'}
            {@const fromMe = evt.metadata && evt.metadata['is_from_me'] === 'true'}
            {@const sender = evt.metadata && (evt.metadata['sender_name'] || evt.metadata['sender'] || '')}
            {@const chat = evt.metadata && evt.metadata['chat_jid'] || ''}
            <div class="event-msg" class:event-msg-out={fromMe} class:event-msg-in={!fromMe}>
              <div class="event-msg-meta">
                <span class="event-source">{evt.source}</span>
                {#if fromMe}
                  <span class="event-msg-dir out">→ sent</span>
                {:else}
                  <span class="event-msg-dir in">← {sender || chat}</span>
                {/if}
                <span class="event-time">{new Date(evt.timestamp).toLocaleTimeString()}</span>
              </div>
              <div class="event-msg-body">{evt.payload}</div>
            </div>
          {:else}
            <div class="event-entry">
              <span class="event-source">{evt.source}</span>
              <span class="event-type">{evt.type}</span>
              <span class="event-payload">{evt.payload}</span>
              <span class="event-time">{new Date(evt.timestamp).toLocaleTimeString()}</span>
            </div>
          {/if}
        {/each}
      </div>
    </div>
  {/if}

  {#if showSessions}
    <div class="sessions-panel">
      {#if sessions.length === 0}
        <div class="sessions-empty">No previous sessions</div>
      {/if}
      {#each sessions as sess}
        <div class="session-entry">
          <button class="session-resume" on:click={() => resumeSession(sess.id)}>
            <span class="session-title">{sess.title}</span>
            <span class="session-meta">{sess.messages} msgs · {sess.model} · {new Date(sess.updated_at).toLocaleDateString()}</span>
          </button>
          <button class="session-delete" on:click={() => deleteSession(sess.id)}>x</button>
        </div>
      {/each}
    </div>
  {/if}

  {#if mcpQrDialog}
    <div class="perm-overlay">
      <div class="perm-dialog qr-dialog">
        <h3>Link {mcpQrServer}</h3>
        {#if mcpQrLoading}
          <p class="qr-loading">Loading QR code...</p>
        {:else if mcpQrImage}
          <p class="qr-hint">Scan with WhatsApp: Linked Devices > Link a Device</p>
          <img class="qr-image" src={mcpQrImage} alt="QR Code" />
        {/if}
        <div class="perm-actions">
          <button class="perm-deny" on:click={() => { mcpQrDialog = false; }}>Close</button>
          {#if !mcpQrLoading}
            <button class="perm-session" on:click={() => linkMCPServer(mcpQrServer)}>Refresh QR</button>
          {/if}
        </div>
      </div>
    </div>
  {/if}

  {#if showPermDialog}
    <div class="perm-overlay">
      <div class="perm-dialog">
        <h3>Permission Required</h3>
        <p class="perm-tool">{permTool}</p>
        <pre class="perm-command">{permCommand}</pre>
        <div class="perm-actions">
          <button class="perm-deny" on:click={() => respondPerm('', false)}>Deny</button>
          <button class="perm-allow" on:click={() => respondPerm('', true)}>Allow once</button>
          <button class="perm-session" on:click={() => respondPerm('session', true)}>Allow for session</button>
          <button class="perm-forever" on:click={() => respondPerm('forever', true)}>Allow forever</button>
        </div>
      </div>
    </div>
  {/if}

  <div class="chat" bind:this={chatEl}>
    {#if messages.length === 0 && !loading}
      <div class="empty">Start a conversation — I can execute commands, edit files, and manage git repos</div>
    {/if}
    {#each messages as msg, i}
      {#if msg.role === 'user'}
        <div class="message user">
          <div class="message-content">{msg.content}</div>
        </div>
      {:else if msg.role === 'assistant'}
        <div class="message assistant">
          <div class="message-content markdown">{@html marked(msg.content)}</div>
          <button class="speak-btn" on:click={() => speakMessage(msg.content)} disabled={speaking} title="Read aloud">
            {#if speaking}
              ...
            {:else}
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><path d="M19.07 4.93a10 10 0 0 1 0 14.14"/><path d="M15.54 8.46a5 5 0 0 1 0 7.07"/></svg>
            {/if}
          </button>
        </div>
      {:else if msg.role === 'tools'}
        <div class="tool-group" class:expanded={msg.expanded}>
          <button class="tool-group-header" on:click={() => { msg.expanded = !msg.expanded; messages = messages; }}>
            <span class="tool-group-arrow">{msg.expanded ? '▼' : '▶'}</span>
            <span class="tool-group-summary">
              {#if msg.active}
                {#if msg.steps.length > 1}
                  <span class="tool-group-count">{msg.steps.filter(s => s.status === 'done').length}/{msg.steps.length}</span>
                {/if}
                <span class="tool-group-running" class:tool-group-done={msg.steps[msg.steps.length - 1].status === 'done'}>{msg.steps[msg.steps.length - 1].tool}</span>
                {#if msg.steps[msg.steps.length - 1].command}
                  <span class="tool-group-cmd">{msg.steps[msg.steps.length - 1].command}</span>
                {/if}
              {:else}
                {msg.steps.length} tool{msg.steps.length > 1 ? 's' : ''}: {msg.steps.map(s => s.tool).join(', ')}
              {/if}
            </span>
          </button>
          {#if msg.expanded}
            <div class="tool-group-details">
              {#each msg.steps as step}
                <div class="tool-step" class:tool-step-error={step.status === 'error'}>
                  <div class="tool-step-header">
                    <span class="tool-step-icon">{step.status === 'done' ? '✓' : step.status === 'error' ? '✗' : '⋯'}</span>
                    <span class="tool-step-name">{step.tool}</span>
                    {#if step.command}
                      <span class="tool-step-cmd">{step.command}</span>
                    {/if}
                  </div>
                  {#if step.result}
                    {#if step.result.includes('![image](data:')}
                      <div class="tool-step-output markdown">{@html marked(step.result)}</div>
                    {:else}
                      <pre class="tool-step-output">{step.result}</pre>
                    {/if}
                  {/if}
                </div>
              {/each}
            </div>
          {/if}
        </div>
      {:else if msg.role === 'error'}
        <div class="message error-msg">
          <div class="message-content">{msg.content}</div>
        </div>
      {/if}
    {/each}
    {#if loading}
      <div class="message assistant">
        <div class="message-content loading">Working...</div>
      </div>
    {/if}
  </div>

  <div class="input-area">
    <button class="mic-btn" class:mic-recording={recording} class:mic-transcribing={transcribing} on:mousedown={startRecording} on:mouseup={stopRecordingAndSend} on:mouseleave={stopRecordingAndSend} on:touchstart|preventDefault={startRecording} on:touchend|preventDefault={stopRecordingAndSend} disabled={loading || transcribing} title={recording ? 'Release to send' : transcribing ? 'Transcribing...' : 'Hold to talk'}>
      {#if recording}
        <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><rect x="3" y="3" width="10" height="10" rx="1"/></svg>
      {:else if transcribing}
        ...
      {:else}
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="1" width="6" height="12" rx="3"/><path d="M19 10v1a7 7 0 0 1-14 0v-1"/><line x1="12" y1="19" x2="12" y2="23"/><line x1="8" y1="23" x2="16" y2="23"/></svg>
      {/if}
    </button>
    {#if recording}
      <div class="voice-meter">
        <div class="voice-meter-bar" style="width: {voiceLevel}%"></div>
      </div>
    {/if}
    {#if transcribing}
      <div class="transcribing-indicator">Transcribing...</div>
    {/if}
    <textarea
      bind:this={textareaEl}
      bind:value={input}
      on:keydown={handleKeydown}
      placeholder={transcribing ? 'Transcribing...' : 'Give me a task...'}
      rows="1"
      disabled={loading || !isReady || transcribing}
    ></textarea>
    <button on:click={send} disabled={loading || !isReady || !input.trim()}>Send</button>
    <button class="clear-btn" on:click={clearChat}>Clear</button>
  </div>
</main>

<style>
  :global(body) {
    margin: 0;
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
    background: #1a1a2e;
    color: #eee;
  }

  main {
    display: flex;
    flex-direction: column;
    height: 100vh;
  }

  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 0.5rem 1rem;
    background: #16213e;
    border-bottom: 1px solid #0f3460;
  }

  header h1 { margin: 0; font-size: 1.2rem; color: #e94560; }

  .header-right { display: flex; align-items: center; gap: 0.5rem; }

  .cost-badge {
    background: #0f3460;
    padding: 0.25rem 0.5rem;
    border-radius: 4px;
    font-size: 0.8rem;
    color: #53d769;
  }

  .icon-btn {
    background: none;
    border: 1px solid #0f3460;
    color: #eee;
    padding: 0.25rem 0.5rem;
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.85rem;
  }

  .icon-btn:hover { background: #0f3460; }
  .icon-btn-active { background: #0f3460; color: #53d769 !important; }

  .settings {
    padding: 0.75rem 1rem;
    background: #16213e;
    border-bottom: 1px solid #0f3460;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .setting-row { display: flex; align-items: center; gap: 0.5rem; }

  .setting-row label { min-width: 60px; font-size: 0.85rem; }

  .setting-row input, .setting-row select {
    flex: 1;
    padding: 0.4rem;
    background: #1a1a2e;
    border: 1px solid #0f3460;
    color: #eee;
    border-radius: 4px;
  }

  .setting-row button {
    padding: 0.4rem 0.75rem;
    background: #e94560;
    border: none;
    color: white;
    border-radius: 4px;
    cursor: pointer;
  }

  .setting-row button:disabled { opacity: 0.5; cursor: not-allowed; }

  .status-ok { color: #53d769; font-size: 0.85rem; }

  /* MCP Settings */
  .mcp-settings {
    border-top: 1px solid #0f3460;
    padding-top: 0.5rem;
    margin-top: 0.25rem;
  }

  .mcp-settings-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.35rem;
  }

  .mcp-settings-title {
    font-size: 0.85rem;
    font-weight: 600;
    color: #aaa;
  }

  .mcp-add-btn {
    padding: 0.2rem 0.5rem;
    background: #0f3460;
    border: none;
    color: #eee;
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.75rem;
  }

  .mcp-add-form {
    margin-bottom: 0.5rem;
  }

  .mcp-empty {
    color: #555;
    font-size: 0.8rem;
    padding: 0.25rem 0;
  }

  .mcp-settings-row {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0.2rem 0;
    font-size: 0.8rem;
  }

  .mcp-srv-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: #555;
    flex-shrink: 0;
  }

  .mcp-srv-dot-on { background: #53d769; }

  .mcp-srv-name {
    font-weight: 500;
    color: #eee;
    flex-shrink: 0;
  }

  .mcp-srv-detail {
    color: #666;
    font-size: 0.7rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    flex: 1;
  }

  .mcp-srv-stats {
    color: #888;
    font-size: 0.7rem;
    flex-shrink: 0;
  }

  .mcp-remove-btn {
    padding: 0 0.35rem;
    background: none;
    border: 1px solid #333;
    color: #666;
    border-radius: 3px;
    cursor: pointer;
    font-size: 0.7rem;
    line-height: 1.2;
    flex-shrink: 0;
  }

  .mcp-link-btn {
    padding: 0 0.35rem;
    background: none;
    border: 1px solid #0f3460;
    color: #53d769;
    border-radius: 3px;
    cursor: pointer;
    font-size: 0.7rem;
    line-height: 1.2;
    flex-shrink: 0;
  }

  .mcp-link-btn:hover { background: #0f3460; }

  .mcp-remove-btn:hover { color: #e94560; border-color: #e94560; }

  .qr-dialog { text-align: center; }
  .qr-hint { color: #aaa; font-size: 0.85rem; margin: 0.5rem 0; }
  .qr-loading { color: #888; font-style: italic; }
  .qr-image { max-width: 256px; margin: 0.5rem auto; display: block; border-radius: 8px; background: white; padding: 8px; }

  /* Sessions panel */
  .sessions-panel {
    background: #16213e;
    border-bottom: 1px solid #0f3460;
    max-height: 200px;
    overflow-y: auto;
    padding: 0.25rem 0;
  }

  .sessions-empty {
    color: #555;
    font-size: 0.8rem;
    padding: 0.75rem 1rem;
    text-align: center;
  }

  .session-entry {
    display: flex;
    align-items: center;
    border-bottom: 1px solid #111;
  }

  .session-resume {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 0.1rem;
    padding: 0.4rem 1rem;
    background: none;
    border: none;
    color: #eee;
    cursor: pointer;
    text-align: left;
    font-family: inherit;
  }

  .session-resume:hover { background: #1a1a2e; }

  .session-title {
    font-size: 0.8rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .session-meta {
    font-size: 0.65rem;
    color: #666;
  }

  .session-delete {
    padding: 0.3rem 0.6rem;
    background: none;
    border: none;
    color: #555;
    cursor: pointer;
    font-size: 0.75rem;
    flex-shrink: 0;
  }

  .session-delete:hover { color: #e94560; }

  /* Permission dialog */
  .perm-overlay {
    position: fixed;
    top: 0; left: 0; right: 0; bottom: 0;
    background: rgba(0,0,0,0.7);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 100;
  }

  .perm-dialog {
    background: #16213e;
    border: 1px solid #0f3460;
    border-radius: 8px;
    padding: 1.5rem;
    max-width: 500px;
    width: 90%;
  }

  .perm-dialog h3 { margin: 0 0 0.5rem; color: #e94560; }

  .perm-tool { font-weight: bold; margin: 0.25rem 0; }

  .perm-command {
    background: #1a1a2e;
    padding: 0.5rem;
    border-radius: 4px;
    font-size: 0.8rem;
    overflow-x: auto;
    margin: 0.5rem 0;
    white-space: pre-wrap;
    word-break: break-all;
  }

  .perm-actions {
    display: flex;
    gap: 0.5rem;
    margin-top: 1rem;
    flex-wrap: wrap;
  }

  .perm-actions button {
    padding: 0.4rem 0.75rem;
    border: none;
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.85rem;
    color: white;
  }

  .perm-deny { background: #666; }
  .perm-allow { background: #e94560; }
  .perm-session { background: #0f3460; }
  .perm-forever { background: #53d769; color: #000 !important; }

  /* Chat */
  .chat {
    flex: 1;
    overflow-y: auto;
    padding: 1rem;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .empty { margin: auto; color: #555; font-size: 0.9rem; text-align: center; }

  .message {
    padding: 0.5rem 0.75rem;
    border-radius: 8px;
    font-size: 13px;
    line-height: 1.55;
    word-break: break-word;
    text-align: left;
  }

  .message.user { align-self: flex-end; max-width: 85%; background: #0f3460; white-space: pre-wrap; }

  .message.assistant {
    align-self: stretch;
    background: #16213e;
    border: 1px solid #0f3460;
  }

  /* Collapsible tool group */
  .tool-group {
    align-self: stretch;
    text-align: left;
    border-left: 2px solid #1b2838;
    margin: 0.1rem 0;
  }

  .tool-group-header {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    background: none;
    border: none;
    color: #666;
    font-size: 11px;
    cursor: pointer;
    padding: 0.2rem 0.5rem;
    width: 100%;
    text-align: left;
    font-family: inherit;
  }

  .tool-group-header:hover { color: #999; }

  .tool-group-arrow {
    font-size: 8px;
    width: 10px;
    flex-shrink: 0;
  }

  .tool-group-summary {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }

  .tool-group-count {
    color: #555;
    font-size: 10px;
    flex-shrink: 0;
  }

  .tool-group-count::after {
    content: '·';
    margin-left: 0.4rem;
    color: #444;
  }

  .tool-group-running {
    color: #e9a560;
    font-weight: 500;
    flex-shrink: 0;
  }

  .tool-group-done {
    color: #53d769;
  }

  .tool-group-cmd {
    color: #555;
    font-family: monospace;
    font-size: 10px;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .tool-group-details {
    padding: 0.1rem 0 0.1rem 0.75rem;
  }

  .tool-step {
    padding: 0.15rem 0;
    font-size: 11px;
    color: #777;
  }

  .tool-step-header {
    display: flex;
    align-items: baseline;
    gap: 0.3rem;
    flex-wrap: wrap;
  }

  .tool-step-error { color: #e94560; }

  .tool-step-icon {
    font-size: 9px;
    color: #53d769;
    flex-shrink: 0;
  }

  .tool-step-error .tool-step-icon { color: #e94560; }

  .tool-step-name {
    color: #888;
    font-weight: 500;
    flex-shrink: 0;
  }

  .tool-step-cmd {
    color: #555;
    font-size: 10px;
    font-family: monospace;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 100%;
  }

  .tool-step-output {
    background: #111827;
    padding: 0.3rem 0.4rem;
    border-radius: 3px;
    margin: 0.15rem 0 0.25rem;
    font-size: 10px;
    max-height: 150px;
    overflow-y: auto;
    color: #777;
    white-space: pre-wrap;
    word-break: break-all;
  }

  .message.error-msg {
    align-self: stretch;
    background: #2d1515;
    border: 1px solid #5c2020;
    color: #e94560;
  }

  .loading { color: #888; font-style: italic; }

  .input-area {
    display: flex;
    gap: 0.5rem;
    padding: 0.75rem 1rem;
    background: #16213e;
    border-top: 1px solid #0f3460;
  }

  textarea {
    flex: 1;
    padding: 0.5rem;
    background: #1a1a2e;
    border: 1px solid #0f3460;
    color: #eee;
    border-radius: 4px;
    resize: none;
    font-family: inherit;
    font-size: 0.9rem;
  }

  textarea:disabled { opacity: 0.5; }

  .input-area button {
    padding: 0.5rem 1rem;
    background: #e94560;
    border: none;
    color: white;
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.9rem;
  }

  .input-area button:disabled { opacity: 0.5; cursor: not-allowed; }

  .clear-btn { background: #0f3460 !important; }

  /* Voice */
  .mic-btn {
    padding: 0.5rem;
    background: #0f3460;
    border: none;
    color: #eee;
    border-radius: 4px;
    cursor: pointer;
    font-size: 1.1rem;
    min-width: 38px;
    transition: background 0.2s;
  }
  .mic-btn:hover { background: #1a4a8a; }
  .mic-recording {
    background: #e94560 !important;
    animation: pulse-red 1s infinite;
  }
  .mic-transcribing {
    background: #e9a045 !important;
    cursor: wait;
  }
  @keyframes pulse-red {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.6; }
  }

  .voice-meter {
    width: 60px;
    height: 28px;
    background: #0d1b2a;
    border-radius: 4px;
    overflow: hidden;
    display: flex;
    align-items: flex-end;
    border: 1px solid #0f3460;
  }
  .voice-meter-bar {
    height: 100%;
    background: linear-gradient(to right, #53d769, #e9a045, #e94560);
    transition: width 0.1s ease;
    border-radius: 3px;
    min-width: 2px;
  }

  .transcribing-indicator {
    font-size: 0.8em;
    color: #e9a045;
    animation: pulse-transcribing 1.2s ease-in-out infinite;
    white-space: nowrap;
  }
  @keyframes pulse-transcribing {
    0%, 100% { opacity: 0.5; }
    50% { opacity: 1; }
  }

  .speak-btn {
    position: absolute;
    top: 4px;
    right: 4px;
    background: none;
    border: none;
    color: #888;
    cursor: pointer;
    font-size: 0.85rem;
    padding: 2px 5px;
    border-radius: 3px;
    opacity: 0;
    transition: opacity 0.2s;
  }
  .message.assistant { position: relative; }
  .message.assistant:hover .speak-btn { opacity: 1; }
  .speak-btn:hover { color: #53d769; background: rgba(255,255,255,0.05); }
  .speak-btn:disabled { cursor: wait; opacity: 0.5 !important; }

  /* Events panel */
  .events-panel {
    background: #16213e;
    border-bottom: 1px solid #0f3460;
    max-height: 250px;
    display: flex;
    flex-direction: column;
  }

  .events-toolbar {
    display: flex;
    gap: 0.5rem;
    padding: 0.5rem 1rem;
    border-bottom: 1px solid #0f3460;
  }

  .events-toolbar button {
    padding: 0.3rem 0.6rem;
    border: none;
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.8rem;
    color: white;
  }

  .events-refresh-btn { background: #0f3460; }
  .events-clear-btn { background: #333; }

  .events-stats {
    display: flex;
    gap: 1rem;
    padding: 0.35rem 1rem;
    font-size: 0.75rem;
    color: #888;
    border-bottom: 1px solid #0f3460;
  }

  .events-stats b { color: #eee; }

  .events-log {
    flex: 1;
    overflow-y: auto;
    padding: 0.25rem 0;
  }

  .events-empty {
    color: #555;
    font-size: 0.8rem;
    padding: 1rem;
    text-align: center;
  }

  .event-entry {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.25rem 1rem;
    font-size: 0.75rem;
    border-bottom: 1px solid #111;
  }

  .event-entry:hover { background: #1a1a2e; }

  .event-source {
    background: #0f3460;
    padding: 0.1rem 0.4rem;
    border-radius: 3px;
    font-weight: 500;
    color: #53d769;
    flex-shrink: 0;
  }

  .event-type {
    color: #e9a560;
    flex-shrink: 0;
  }

  .event-payload {
    color: #aaa;
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .event-time {
    color: #555;
    flex-shrink: 0;
    font-family: monospace;
    font-size: 0.7rem;
  }

  /* Message-type events: show as mini conversation bubbles */
  .event-msg {
    padding: 0.3rem 1rem;
    border-bottom: 1px solid #111;
  }

  .event-msg-meta {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    margin-bottom: 0.15rem;
  }

  .event-msg-dir {
    font-size: 0.7rem;
    font-weight: 500;
  }

  .event-msg-dir.out { color: #888; }
  .event-msg-dir.in  { color: #53d769; }

  .event-msg-body {
    font-size: 0.8rem;
    padding: 0.25rem 0.5rem;
    border-radius: 6px;
    max-width: 90%;
    white-space: pre-wrap;
    word-break: break-word;
  }

  .event-msg-out .event-msg-body {
    background: #1e2a3a;
    color: #aaa;
    margin-left: auto;
    text-align: right;
  }

  .event-msg-in .event-msg-body {
    background: #16302a;
    color: #d4edd8;
  }

  /* MCP Servers */
  .mcp-servers {
    border-bottom: 1px solid #0f3460;
    padding: 0.35rem 1rem;
  }

  .mcp-header {
    font-size: 0.75rem;
    color: #888;
    font-weight: 600;
    margin-bottom: 0.25rem;
  }

  .mcp-server-entry {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.75rem;
    padding: 0.15rem 0;
  }

  .mcp-server-name {
    font-weight: 500;
    color: #eee;
  }

  .mcp-server-status {
    color: #e94560;
    font-size: 0.7rem;
  }

  .mcp-connected {
    color: #53d769;
  }

  .mcp-server-info {
    color: #666;
    font-size: 0.7rem;
  }

  /* Markdown rendered content */
  .markdown :global(p) { margin: 0.25rem 0; }
  .markdown :global(p:first-child) { margin-top: 0; }
  .markdown :global(p:last-child) { margin-bottom: 0; }
  .markdown :global(ul), .markdown :global(ol) { margin: 0.25rem 0; padding-left: 1.4rem; }
  .markdown :global(li) { margin: 0.1rem 0; }
  .markdown :global(h1), .markdown :global(h2), .markdown :global(h3) {
    margin: 0.4rem 0 0.2rem;
    font-size: 14px;
    color: #e94560;
  }
  .markdown :global(h1) { font-size: 15px; }
  .markdown :global(code) {
    background: #0d1b2a;
    padding: 0.1rem 0.3rem;
    border-radius: 3px;
    font-size: 12px;
  }
  .markdown :global(pre) {
    background: #0d1b2a;
    padding: 0.5rem;
    border-radius: 6px;
    overflow-x: auto;
    margin: 0.35rem 0;
    font-size: 12px;
  }
  .markdown :global(pre code) {
    background: none;
    padding: 0;
  }
  .markdown :global(strong) { color: #fff; }
  .markdown :global(a) { color: #53d769; }
  .markdown :global(blockquote) {
    border-left: 3px solid #0f3460;
    margin: 0.4rem 0;
    padding: 0.2rem 0.6rem;
    color: #aaa;
  }
  .markdown :global(img) {
    max-width: 100%;
    max-height: 300px;
    border-radius: 6px;
    margin: 0.35rem 0;
    display: block;
  }
</style>
