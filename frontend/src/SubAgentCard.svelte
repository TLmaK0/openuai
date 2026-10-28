<script>
  // One sub-agent in its own view: what it was asked, what it ran, what it
  // produced and what it cost. It can be cancelled while it runs.
  import { CancelSubAgent } from '../wailsjs/go/main/App';
  export let sa;

  let expanded = false;
  let cancelling = false;

  $: running = sa.status === 'running';
  $: lastTool = [...(sa.steps || [])].reverse().find((s) => s.tool);
  $: toolCount = (sa.steps || []).filter((s) => s.tool).length;

  async function cancel(e) {
    e.stopPropagation();
    cancelling = true;
    try { await CancelSubAgent(sa.key); } catch (_) { cancelling = false; }
  }

  function statusLabel(s) {
    if (s.status === 'running') return cancelling ? 'cancelling…' : 'running';
    if (s.cancelled) return 'cancelled';
    if (s.error) return 'error';
    if (s.partial) return 'partial';
    return 'done';
  }
</script>

<div class="sa-card" data-sa-key={sa.key} style={sa.color ? `border-left-color:${sa.color}` : ''} class:sa-running={running} class:sa-error={!running && sa.error && !sa.cancelled}>
  <button class="sa-header" on:click={() => (expanded = !expanded)}>
    <span class="sa-arrow">{expanded ? '▼' : '▶'}</span>
    <span class="sa-agent">{sa.agent}</span>
    <span class="sa-id">{sa.id}</span>
    <span class="sa-status sa-status-{statusLabel(sa).replace('…', '')}">{statusLabel(sa)}</span>
    {#if running && lastTool}
      <span class="sa-current">{lastTool.tool}</span>
    {/if}
    <span class="sa-meta">
      {toolCount} tool{toolCount === 1 ? '' : 's'}
      {#if !running}· ${(sa.cost_usd || 0).toFixed(4)} · {sa.input_tokens || 0}/{sa.output_tokens || 0} tok{/if}
    </span>
    {#if running}
      <span class="sa-cancel" role="button" tabindex="0" title="Cancel this sub-agent"
        on:click={cancel} on:keydown={(e) => e.key === 'Enter' && cancel(e)}
        class:disabled={cancelling}>Cancel</span>
    {/if}
  </button>
  {#if expanded}
    <div class="sa-body">
      <div class="sa-section"><span class="sa-label">Task</span><pre>{sa.description}</pre></div>
      {#if sa.model}<div class="sa-section"><span class="sa-label">Model</span> {sa.model}</div>{/if}
      {#if sa.notice}<div class="sa-section sa-notice">{sa.notice}</div>{/if}
      {#if sa.steps.length}
        <div class="sa-section"><span class="sa-label">Activity</span>
          {#each sa.steps as st}
            {#if st.tool}
              <div class="sa-step">
                <span class="sa-step-icon">{st.status === 'done' ? '✓' : st.status === 'error' ? '✗' : '⋯'}</span>
                <span class="sa-step-name">{st.tool}</span>
                {#if st.command}<span class="sa-step-cmd">{st.command}</span>{/if}
              </div>
              {#if st.result}<pre class="sa-step-out">{st.result}</pre>{/if}
            {:else if st.text}
              <div class="sa-text">{st.text}</div>
            {/if}
          {/each}
        </div>
      {/if}
      {#if sa.error}<div class="sa-section sa-err"><span class="sa-label">Error</span> {sa.error}</div>{/if}
      {#if !running && sa.output}
        <div class="sa-section"><span class="sa-label">Output</span><pre>{sa.output}</pre></div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .sa-card { border: 1px solid #22344d; border-left: 3px solid #2f9eff; border-radius: 6px; margin: 0.3rem 0; background: rgba(20, 30, 45, 0.5); font-size: 0.82rem; }
  .sa-running { border-left-color: #f5b942; }
  .sa-error { border-left-color: #ff6b81; }
  .sa-header { display: flex; align-items: center; gap: 0.5rem; width: 100%; background: none; border: none; color: #bbb; padding: 0.4rem 0.6rem; cursor: pointer; text-align: left; font: inherit; }
  .sa-arrow { font-size: 0.7rem; color: #777; }
  .sa-agent { color: #2f9eff; font-weight: 600; }
  .sa-id { color: #888; }
  .sa-status { font-size: 0.72rem; padding: 0 0.4rem; border-radius: 8px; background: #1d2a3b; }
  .sa-status-running, .sa-status-cancelling { color: #f5b942; }
  .sa-status-done { color: #6fd18f; }
  .sa-status-error { color: #ff6b81; }
  .sa-status-partial, .sa-status-cancelled { color: #c9a0ff; }
  .sa-current { color: #999; font-family: monospace; }
  .sa-meta { margin-left: auto; color: #777; font-size: 0.75rem; }
  .sa-cancel { color: #ff8fa3; border: 1px solid #5e1c2e; border-radius: 4px; padding: 0 0.4rem; font-size: 0.75rem; }
  .sa-cancel:hover { background: #5e1c2e; }
  .sa-cancel.disabled { opacity: 0.5; pointer-events: none; }
  .sa-body { padding: 0.2rem 0.8rem 0.6rem 1.6rem; }
  .sa-section { margin: 0.35rem 0; color: #aaa; }
  .sa-label { color: #777; font-size: 0.72rem; text-transform: uppercase; margin-right: 0.4rem; }
  .sa-section pre, .sa-step-out { white-space: pre-wrap; word-break: break-word; margin: 0.2rem 0; color: #ccc; font-size: 0.78rem; max-height: 16rem; overflow: auto; }
  .sa-step { display: flex; gap: 0.4rem; color: #aaa; }
  .sa-step-name { color: #ddd; }
  .sa-step-cmd { color: #888; font-family: monospace; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .sa-step-out { color: #888; margin-left: 1.2rem; }
  .sa-text { color: #bbb; margin: 0.2rem 0; white-space: pre-wrap; }
  .sa-err { color: #ff6b81; }
  .sa-notice { color: #c9a0ff; }
</style>
