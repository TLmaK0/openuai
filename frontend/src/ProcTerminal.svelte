<script>
  import { onMount, onDestroy } from 'svelte';
  import { Terminal } from '@xterm/xterm';
  import { FitAddon } from '@xterm/addon-fit';
  import '@xterm/xterm/css/xterm.css';
  import { GetProcSnapshot, ProcInput, ProcResize, KillProc } from '../wailsjs/go/main/App';
  import { EventsOn } from '../wailsjs/runtime/runtime';

  // Initial process info (id, command, status...) from the proc_start event.
  export let info;

  let host;
  let term;
  let fit;
  let resizeObs;
  let collapsed = false;
  let offs = [];
  let written = 0; // output bytes already shown (offset of the next byte)
  let ready = false;
  let queued = [];

  $: status = info.status;
  $: label = status === 'exited'
    ? `exited (${info.exit_code})`
    : status === 'waiting_for_input' ? 'waiting for input' : 'running';

  const b64 = (s) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));

  // Output can arrive both through the snapshot and as live events; offsets
  // tell what was already shown.
  function feed(offset, bytes) {
    const end = offset + bytes.length;
    if (end <= written) return;
    term.write(offset < written ? bytes.subarray(written - offset) : bytes);
    written = end;
  }

  onMount(async () => {
    term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      rows: 14,
      scrollback: 5000,
      convertEol: false,
      theme: { background: '#0d1117', foreground: '#e6edf3' },
    });
    fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host);
    // What the user types goes to the process only, never to the model.
    term.onData((d) => { ProcInput(info.id, d); });
    term.onResize(({ cols, rows }) => { ProcResize(info.id, cols, rows); });
    resizeObs = new ResizeObserver(() => { if (!collapsed) { try { fit.fit(); } catch (_) {} } });
    resizeObs.observe(host);
    try { fit.fit(); } catch (_) {}

    offs.push(EventsOn('proc_output', (e) => {
      if (e.id !== info.id) return;
      if (!ready) { queued.push(e); return; }
      feed(e.offset, b64(e.data));
    }));
    offs.push(EventsOn('proc_status', (i) => { if (i.id === info.id) info = i; }));
    offs.push(EventsOn('proc_exit', (i) => { if (i.id === info.id) info = i; }));

    const snap = await GetProcSnapshot(info.id);
    if (snap && snap.data) {
      term.write(b64(snap.data));
      written = snap.total;
    }
    ready = true;
    for (const e of queued) feed(e.offset, b64(e.data));
    queued = [];
    if (info.status === 'running' || info.status === 'waiting_for_input') term.focus();
  });

  onDestroy(() => {
    offs.forEach((off) => { if (typeof off === 'function') off(); });
    if (resizeObs) resizeObs.disconnect();
    if (term) term.dispose();
  });

  function toggle() {
    collapsed = !collapsed;
    if (!collapsed) setTimeout(() => { try { fit.fit(); } catch (_) {} }, 0);
  }
</script>

<div class="proc-card" class:waiting={status === 'waiting_for_input'} class:exited={status === 'exited'}>
  <div class="proc-head">
    <span class="proc-badge {status}">{label}</span>
    <span class="proc-cmd" title={info.command}>{info.command}</span>
    {#if status !== 'exited'}
      <button class="proc-btn kill" on:click={() => KillProc(info.id)} title="Stop this process">Kill</button>
    {/if}
    <button class="proc-btn" on:click={toggle}>{collapsed ? 'Expand' : 'Collapse'}</button>
  </div>
  <div class="proc-term" class:hidden={collapsed} bind:this={host}></div>
</div>

<style>
  .proc-card { border: 1px solid #30363d; border-radius: 8px; background: #0d1117; margin: 8px 0; overflow: hidden; }
  .proc-card.waiting { border-color: #d29922; }
  .proc-head { display: flex; align-items: center; gap: 8px; padding: 6px 10px; background: #161b22; font-size: 12px; }
  .proc-cmd { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: monospace; color: #e6edf3; }
  .proc-badge { padding: 1px 8px; border-radius: 10px; font-size: 11px; background: #1f6feb33; color: #58a6ff; }
  .proc-badge.waiting_for_input { background: #d2992233; color: #e3b341; }
  .proc-badge.exited { background: #6e768133; color: #8b949e; }
  .proc-btn { background: transparent; border: 1px solid #30363d; color: #c9d1d9; border-radius: 4px; padding: 1px 8px; font-size: 11px; cursor: pointer; }
  .proc-btn:hover { background: #21262d; }
  .proc-btn.kill { color: #f85149; }
  .proc-term { padding: 6px 8px; }
  .proc-term.hidden { display: none; }
</style>
