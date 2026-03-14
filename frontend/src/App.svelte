<script>
  import { SendMessage, SetAPIKey, HasAPIKey, GetModels, GetDefaultModel, SetDefaultModel, ClearChat, GetProvider, SetProvider, GetProviders, OpenAILogin, OpenAIIsLoggedIn, RespondPermission } from '../wailsjs/go/main/App';
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

  $: isReady = provider === 'openai' ? openaiLoggedIn : hasKey;

  onMount(async () => {
    providers = await GetProviders();
    provider = await GetProvider();
    hasKey = await HasAPIKey();
    openaiLoggedIn = await OpenAIIsLoggedIn();
    models = await GetModels();
    selectedModel = await GetDefaultModel();
    if (!isReady) showSettings = true;

    // Listen for agent steps — group consecutive tool calls into one collapsible block
    // Tool group uses 'active' flag to keep showing the last tool until the group is finished
    EventsOn('agent_step', (step) => {
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
      <button class="icon-btn" on:click={() => showSettings = !showSettings}>Settings</button>
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
                    <pre class="tool-step-output">{step.result}</pre>
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
    <textarea
      bind:this={textareaEl}
      bind:value={input}
      on:keydown={handleKeydown}
      placeholder="Give me a task..."
      rows="1"
      disabled={loading || !isReady}
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
</style>
