<script>
  // One running sub-agent in the side column: a square tile with a living orb
  // in the sub-agent's own colour, its name and what it is doing right now.
  // Clicking the tile jumps to its card in the chat; x cancels it.
  import { createEventDispatcher } from 'svelte';
  import { CancelSubAgent } from '../wailsjs/go/main/App';
  export let sa;

  const dispatch = createEventDispatcher();
  let cancelling = false;

  $: lastTool = [...(sa.steps || [])].reverse().find((s) => s.tool && s.status === 'running');
  $: color = sa.color || '#f5b942';

  async function cancel(e) {
    e.stopPropagation();
    if (cancelling) return;
    cancelling = true;
    try { await CancelSubAgent(sa.key); } catch (_) { cancelling = false; }
  }
</script>

<button class="sa-tile" class:cancelling style="--sa-color:{color}"
  title="{sa.agent} · {sa.id}{sa.model ? ' · ' + sa.model : ''}\n{sa.description || ''}"
  on:click={() => dispatch('select')}>
  <span class="sa-x" role="button" tabindex="0" title="Cancel this sub-agent"
    on:click={cancel} on:keydown={(e) => e.key === 'Enter' && cancel(e)}>×</span>
  <span class="sa-orb" aria-hidden="true">
    <span class="blob b1"></span>
    <span class="blob b2"></span>
    <span class="blob b3"></span>
    <span class="core"></span>
  </span>
  <span class="sa-name">{sa.agent}</span>
  <span class="sa-doing">{cancelling ? 'cancelling…' : lastTool ? lastTool.tool : 'thinking…'}</span>
</button>

<style>
  .sa-tile {
    position: relative;
    flex: 0 0 auto;
    width: 96px;
    height: 96px;
    padding: 0.35rem 0.3rem 0.3rem;
    border-radius: 10px;
    border: 1px solid color-mix(in srgb, var(--sa-color) 55%, transparent);
    background: rgba(9, 14, 23, 0.88);
    box-shadow: 0 0 12px color-mix(in srgb, var(--sa-color) 25%, transparent);
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: flex-end;
    gap: 0.1rem;
    cursor: pointer;
    color: #cfe3ff;
    overflow: hidden;
  }
  .sa-tile:hover { box-shadow: 0 0 18px color-mix(in srgb, var(--sa-color) 45%, transparent); }
  .sa-tile.cancelling { opacity: 0.55; }
  .sa-orb {
    position: relative;
    width: 46px;
    height: 46px;
    margin-bottom: 0.15rem;
    color: var(--sa-color);
  }
  .blob {
    position: absolute;
    inset: 0;
    border: 1.4px solid currentColor;
    border-radius: 42% 58% 55% 45% / 48% 42% 58% 52%;
    animation: morph 6s ease-in-out infinite, spin 9s linear infinite;
  }
  .b1 { opacity: 0.9; }
  .b2 { inset: 3px; opacity: 0.65; animation-duration: 7.5s, 12s; animation-direction: alternate, reverse; }
  .b3 { inset: 6px; opacity: 0.45; animation-duration: 5s, 7s; }
  .core {
    position: absolute;
    inset: 15px;
    border-radius: 50%;
    background: radial-gradient(circle, currentColor 0%, transparent 70%);
    opacity: 0.55;
    animation: pulse 2.2s ease-in-out infinite;
  }
  .sa-name, .sa-doing {
    max-width: 100%;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    line-height: 1.15;
  }
  .sa-name { font-size: 0.72rem; font-weight: 600; color: var(--sa-color); }
  .sa-doing { font-size: 0.64rem; color: #8aa4c4; }
  .sa-x {
    position: absolute;
    top: 2px;
    right: 5px;
    font-size: 0.85rem;
    line-height: 1;
    color: #8aa4c4;
    opacity: 0;
    transition: opacity 0.15s;
  }
  .sa-tile:hover .sa-x, .sa-x:focus { opacity: 1; }
  .sa-x:hover { color: #ff8fa3; }
  @keyframes morph {
    0%, 100% { border-radius: 42% 58% 55% 45% / 48% 42% 58% 52%; }
    33% { border-radius: 58% 42% 46% 54% / 40% 60% 40% 60%; }
    66% { border-radius: 48% 52% 62% 38% / 58% 46% 54% 42%; }
  }
  @keyframes spin { to { transform: rotate(360deg); } }
  @keyframes pulse { 0%, 100% { opacity: 0.35; transform: scale(0.85); } 50% { opacity: 0.75; transform: scale(1.1); } }
  @media (prefers-reduced-motion: reduce) {
    .blob, .core { animation: none; }
  }
</style>
