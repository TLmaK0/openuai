<script>
  // Live agent map: the main agent on top and every live sub-agent below it,
  // one tile each, in a scrollable column. Arrows on the left join two agents
  // that have talked, pointing in the direction of their last message
  // (delegation goes parent -> child, the result comes back child -> parent).
  // Only the last exchange per pair is drawn. Every arrow touching a sub-agent
  // that is still running moves; the newest one also stands out; arrows of
  // finished sub-agents stop and fade.
  import { afterUpdate, onMount, onDestroy, createEventDispatcher } from 'svelte';
  import SubAgentOrb from './SubAgentOrb.svelte';

  export let agents = [];   // live sub-agents (running or just finished)
  export let links = [];    // [{ from, to, at }], one per pair of agents
  export let mainBusy = false;
  export let mainName = '';  // the assistant's name (wake word)
  export let mainAgent = ''; // agent definition the main conversation runs as
  export let mainModel = '';
  export let mainColor = '';

  const dispatch = createEventDispatcher();
  const MAIN_COLOR = '#2f9eff';
  const X0 = 30;            // x where arrows meet the tiles

  let inner;
  let pos = {};
  let height = 0;

  $: mainSa = { key: 'main', nick: mainName || mainAgent || 'Assistant', agent: mainAgent ? 'main conversation running as agent ' + mainAgent : 'main agent (normal assistant)', id: 'main', model: mainAgent ? mainModel : '', color: mainColor || MAIN_COLOR, steps: [], busy: mainBusy, status: 'running' };
  $: running = new Set(agents.filter((a) => a.status === 'running').map((a) => a.key));
  $: colors = Object.fromEntries([['main', mainColor || MAIN_COLOR], ...agents.map((a) => [a.key, a.color])]);

  function measure() {
    if (!inner) return;
    const next = {};
    inner.querySelectorAll('[data-sa-key]').forEach((el) => {
      next[el.dataset.saKey] = el.offsetTop + el.offsetHeight / 2;
    });
    const h = inner.scrollHeight;
    if (h !== height || JSON.stringify(next) !== JSON.stringify(pos)) {
      pos = next;
      height = h;
    }
  }
  afterUpdate(measure);
  onMount(() => window.addEventListener('resize', measure));
  onDestroy(() => window.removeEventListener('resize', measure));

  $: shown = links
    .filter((l) => l.from !== l.to && pos[l.from] != null && pos[l.to] != null)
    .sort((a, b) => a.at - b.at)
    .map((l, i, arr) => {
      const y1 = pos[l.from];
      const y2 = pos[l.to];
      const bend = 10 + Math.min(18, Math.abs(y2 - y1) / 10);
      const newest = i === arr.length - 1;
      // Active while the sub-agent(s) at its ends are still running.
      const active = [l.from, l.to].every((k) => k === 'main' || running.has(k));
      return {
        ...l,
        newest,
        active,
        color: colors[l.from] || MAIN_COLOR,
        op: newest ? 1 : active ? 0.85 : Math.max(0.18, 0.55 - 0.1 * (arr.length - 2 - i)),
        d: `M ${X0} ${y1} C ${X0 - bend} ${y1}, ${X0 - bend} ${y2}, ${X0} ${y2}`,
        head: `${X0},${y2} ${X0 - 6},${y2 - 4} ${X0 - 6},${y2 + 4}`,
      };
    });
</script>

<aside class="sa-column" aria-label="Running agents">
  <div class="sa-inner" bind:this={inner}>
    <svg class="sa-links" width="{X0 + 2}" height={height} aria-hidden="true">
      {#each shown as l (l.from + '>' + l.to)}
        <g style="opacity:{l.op}" class:newest={l.newest} class:active={l.active}>
          <path d={l.d} fill="none" stroke={l.color} stroke-width={l.newest ? 2 : 1.4} class="link" />
          <polygon points={l.head} fill={l.color} />
        </g>
      {/each}
    </svg>
    <SubAgentOrb sa={mainSa} main on:select={() => dispatch('select', 'main')} />
    {#each agents as sa (sa.key)}
      <SubAgentOrb {sa} on:select={() => dispatch('select', sa.key)} />
    {/each}
  </div>
</aside>

<style>
  .sa-column {
    position: fixed;
    right: 0.6rem;
    top: 4.5rem;
    bottom: 5.5rem;
    width: 136px;
    overflow-y: auto;
    overflow-x: hidden;
    z-index: 2;
    scrollbar-width: thin;
  }
  .sa-inner {
    position: relative;
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 0.6rem;
    padding: 0.3rem 0 0.3rem 30px;
  }
  .sa-links {
    position: absolute;
    left: 0;
    top: 0;
    pointer-events: none;
    overflow: visible;
  }
  g { transition: opacity 0.6s ease; }
  .active .link { stroke-dasharray: 5 4; animation: flow 0.9s linear infinite; }
  .newest:not(.active) .link { stroke-dasharray: 5 4; }
  @keyframes flow { to { stroke-dashoffset: -9; } }
  @media (prefers-reduced-motion: reduce) {
    .active .link { animation: none; }
  }
</style>
