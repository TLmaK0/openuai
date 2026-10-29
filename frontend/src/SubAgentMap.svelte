<script>
  // Live agent map: the main agent on top and every live sub-agent below it,
  // one tile each, in a scrollable column. Arrows on the left join two agents
  // that have talked, pointing in the direction of their last message
  // (delegation goes parent -> child, the result comes back child -> parent).
  // Only the last exchange per pair is drawn; the newest stands out and older
  // ones fade.
  import { afterUpdate, onMount, onDestroy, createEventDispatcher } from 'svelte';
  import SubAgentOrb from './SubAgentOrb.svelte';

  export let agents = [];   // live sub-agents (running or just finished)
  export let links = [];    // [{ from, to, at }], one per pair of agents
  export let mainBusy = false;

  const dispatch = createEventDispatcher();
  const MAIN_COLOR = '#2f9eff';
  const X0 = 30;            // x where arrows meet the tiles

  let inner;
  let pos = {};
  let height = 0;

  $: mainSa = { key: 'main', nick: 'Principal', agent: 'main agent', id: 'main', color: MAIN_COLOR, steps: [], busy: mainBusy, status: 'running' };
  $: colors = Object.fromEntries([['main', MAIN_COLOR], ...agents.map((a) => [a.key, a.color])]);

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
      return {
        ...l,
        newest,
        color: colors[l.from] || MAIN_COLOR,
        op: newest ? 1 : Math.max(0.18, 0.55 - 0.1 * (arr.length - 2 - i)),
        d: `M ${X0} ${y1} C ${X0 - bend} ${y1}, ${X0 - bend} ${y2}, ${X0} ${y2}`,
        head: `${X0},${y2} ${X0 - 6},${y2 - 4} ${X0 - 6},${y2 + 4}`,
      };
    });
</script>

<aside class="sa-column" aria-label="Running agents">
  <div class="sa-inner" bind:this={inner}>
    <svg class="sa-links" width="{X0 + 2}" height={height} aria-hidden="true">
      {#each shown as l (l.from + '>' + l.to)}
        <g style="opacity:{l.op}" class:newest={l.newest}>
          <path d={l.d} fill="none" stroke={l.color} stroke-width={l.newest ? 2 : 1.4} class="link" />
          <polygon points={l.head} fill={l.color} />
        </g>
      {/each}
    </svg>
    <SubAgentOrb sa={mainSa} main />
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
  .newest .link { stroke-dasharray: 5 4; animation: flow 0.9s linear infinite; }
  @keyframes flow { to { stroke-dashoffset: -9; } }
  @media (prefers-reduced-motion: reduce) {
    .newest .link { animation: none; }
  }
</style>
