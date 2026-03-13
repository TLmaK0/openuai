<script>
  import { SendMessage, SetAPIKey, HasAPIKey, GetModels, GetDefaultModel, SetDefaultModel, ClearChat, GetProvider, SetProvider, GetProviders, OpenAILogin, OpenAIIsLoggedIn } from '../wailsjs/go/main/App';
  import { onMount, afterUpdate } from 'svelte';

  let messages = [];
  let input = '';
  let loading = false;
  let loggingIn = false;
  let apiKey = '';
  let hasKey = false;
  let showSettings = false;
  let models = [];
  let selectedModel = '';
  let totalCost = 0;
  let totalTokens = 0;
  let chatEl;
  let provider = 'openai';
  let providers = [];
  let openaiLoggedIn = false;

  $: isReady = provider === 'openai' ? openaiLoggedIn : hasKey;

  onMount(async () => {
    providers = await GetProviders();
    provider = await GetProvider();
    hasKey = await HasAPIKey();
    openaiLoggedIn = await OpenAIIsLoggedIn();
    models = await GetModels();
    selectedModel = await GetDefaultModel();
    if (!isReady) showSettings = true;
  });

  afterUpdate(() => {
    if (chatEl) chatEl.scrollTop = chatEl.scrollHeight;
  });

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
    input = '';
    messages = [...messages, { role: 'user', content }];
    loading = true;

    const resp = await SendMessage(content);
    loading = false;

    if (resp.error) {
      messages = [...messages, { role: 'assistant', content: 'Error: ' + resp.error }];
    } else {
      messages = [...messages, {
        role: 'assistant',
        content: resp.content,
        tokens: resp.input_tokens + resp.output_tokens,
        cost: resp.cost_usd
      }];
      totalCost += resp.cost_usd;
      totalTokens += resp.input_tokens + resp.output_tokens;
    }
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

  <div class="chat" bind:this={chatEl}>
    {#if messages.length === 0 && !loading}
      <div class="empty">Start a conversation</div>
    {/if}
    {#each messages as msg}
      <div class="message {msg.role}">
        <div class="message-content">{msg.content}</div>
        {#if msg.tokens}
          <div class="message-meta">{msg.tokens} tokens | ${msg.cost.toFixed(4)}</div>
        {/if}
      </div>
    {/each}
    {#if loading}
      <div class="message assistant">
        <div class="message-content loading">Thinking...</div>
      </div>
    {/if}
  </div>

  <div class="input-area">
    <textarea
      bind:value={input}
      on:keydown={handleKeydown}
      placeholder="Type a message..."
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

  header h1 {
    margin: 0;
    font-size: 1.2rem;
    color: #e94560;
  }

  .header-right {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

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

  .icon-btn:hover {
    background: #0f3460;
  }

  .settings {
    padding: 0.75rem 1rem;
    background: #16213e;
    border-bottom: 1px solid #0f3460;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .setting-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  .setting-row label {
    min-width: 60px;
    font-size: 0.85rem;
  }

  .setting-row input,
  .setting-row select {
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

  .setting-row button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .status-ok {
    color: #53d769;
    font-size: 0.85rem;
  }

  .chat {
    flex: 1;
    overflow-y: auto;
    padding: 1rem;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .empty {
    margin: auto;
    color: #555;
    font-size: 0.9rem;
  }

  .message {
    max-width: 80%;
    padding: 0.6rem 0.8rem;
    border-radius: 8px;
    font-size: 0.9rem;
    line-height: 1.5;
    white-space: pre-wrap;
    word-break: break-word;
  }

  .message.user {
    align-self: flex-end;
    background: #0f3460;
  }

  .message.assistant {
    align-self: flex-start;
    background: #16213e;
    border: 1px solid #0f3460;
  }

  .message-meta {
    font-size: 0.7rem;
    color: #666;
    margin-top: 0.3rem;
  }

  .loading {
    color: #888;
    font-style: italic;
  }

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

  textarea:disabled {
    opacity: 0.5;
  }

  .input-area button {
    padding: 0.5rem 1rem;
    background: #e94560;
    border: none;
    color: white;
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.9rem;
  }

  .input-area button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .clear-btn {
    background: #0f3460 !important;
  }
</style>
