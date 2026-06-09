<script>
  import { SendMessage, EditMessage, AbortAgent, SetAPIKey, HasAPIKey, GetModels, GetDefaultModel, SetDefaultModel, ClearChat, GetProvider, SetProvider, GetProviders, OpenAILogin, OpenAIIsLoggedIn, RespondPermission, GetEventStats, GetMCPServers, AddMCPServer, RemoveMCPServer, ReauthMCPServer, AuthMCPServer, GetSessions, ResumeSession, DeleteSession, CallMCPTool, StartRecording, StopRecording, SpeakText, GetTTSVoice, SetTTSVoice, GetTTSVoices, PiperSupported, GetVoiceEnabled, SetVoiceEnabled, GetAudioDevices, GetAudioDevice, SetAudioDevice, GetSTTLanguage, SetSTTLanguage, GetWakeWord, SetWakeWord, GetWakeListening, SetWakeListening, SetWakePaused, GetVersion, ApplyUpdate, SkipVersion, LipReadingModelReady, DownloadLipReadingModel, StartLipRecording, StopLipRecording, GetBetaLipReading, SetBetaLipReading, GetMarketplace, GetInstalledNames, InstallMarketplace, CheckNpx, OpenPath, GetWorkDir } from '../wailsjs/go/main/App';
  import { EventsOn, BrowserOpenURL } from '../wailsjs/runtime/runtime';
  import { onMount, afterUpdate } from 'svelte';
  import { marked } from 'marked';
  import hljs from 'highlight.js';
  import 'highlight.js/styles/github-dark.css';

  // Render fenced code blocks as a styled card: syntax-highlighted body with a
  // header showing the language and a Copy button.
  const codeRenderer = new marked.Renderer();
  codeRenderer.code = function (token) {
    const code = typeof token === 'object' ? token.text : token;
    let lang = (typeof token === 'object' ? token.lang : arguments[1]) || '';
    lang = lang.trim().split(/\s+/)[0];
    let html, label;
    try {
      if (lang && hljs.getLanguage(lang)) {
        html = hljs.highlight(code, { language: lang }).value;
        label = lang;
      } else {
        const auto = hljs.highlightAuto(code);
        html = auto.value;
        label = auto.language || 'code';
      }
    } catch (e) {
      html = code.replace(/[&<>]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' }[c]));
      label = lang || 'code';
    }
    return `<div class="code-block"><div class="code-head"><span class="code-lang">${label}</span>` +
      `<button class="code-copy" type="button">Copy</button></div>` +
      `<pre class="hljs"><code>${html}</code></pre></div>`;
  };

  // Inline code that is a file name (e.g. `lista_fiesta.pdf`) becomes a clickable
  // link: clicking resolves it against the working dir and opens the document.
  const FILE_RE = /^[^\s<>|*?":]+\.(pdf|docx?|xlsx?|pptx?|csv|tsv|txt|md|rtf|odt|ods|odp|png|jpe?g|gif|svg|webp|bmp|zip|tar|gz|html?|json|xml|ics|epub|mp3|mp4|wav|m4a|mov)$/i;
  const escHtml = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  codeRenderer.codespan = function (token) {
    const text = typeof token === 'object' ? token.text : token;
    const esc = escHtml(text);
    if (FILE_RE.test(text)) {
      return `<code class="file-link" data-file="${esc}" title="Open file">${esc}</code>`;
    }
    return `<code>${esc}</code>`;
  };

  marked.setOptions({
    breaks: true,
    gfm: true,
    renderer: codeRenderer,
  });

  // Delegated chat clicks: code-copy buttons and links.
  function onChatClick(e) {
    const btn = e.target.closest && e.target.closest('.code-copy');
    if (btn) {
      const block = btn.closest('.code-block');
      const pre = block && block.querySelector('pre');
      if (!pre) return;
      navigator.clipboard && navigator.clipboard.writeText(pre.innerText);
      const prev = btn.textContent;
      btn.textContent = 'Copied';
      setTimeout(() => { btn.textContent = prev; }, 1500);
      return;
    }

    // Clickable file name (inline code the agent emitted): resolve against the
    // working dir if it's bare, then open. Silent if it doesn't exist (the
    // file-name heuristic can match a non-file mention).
    const fl = e.target.closest && e.target.closest('.file-link');
    if (fl) {
      e.preventDefault();
      openFile(fl.getAttribute('data-file') || '');
      return;
    }

    // Links: open externally instead of navigating the embedded webview away.
    // A web URL goes to the system browser; a local file (the agent's output)
    // opens directly in the OS default app for that document type.
    const a = e.target.closest && e.target.closest('a[href]');
    if (a) {
      const raw = a.getAttribute('href') || '';
      if (/^https?:\/\//i.test(raw)) {
        e.preventDefault();
        BrowserOpenURL(raw);
      } else if (raw && !/^(#|mailto:|tel:|javascript:)/i.test(raw)) {
        e.preventDefault();
        openFile(decodeURI(raw));
      }
    }
  }

  // Resolve a path (bare name → workDir/name; absolute/file:// kept) and open it
  // in the OS default app. Failure (e.g. not a real file) is ignored silently.
  function openFile(p) {
    if (!p) return;
    let path = p.replace(/^file:\/\//i, '');
    const absolute = path.startsWith('/') || path.startsWith('~') || /^[a-zA-Z]:[\\/]/.test(path);
    if (!absolute && workDir) path = workDir.replace(/\/+$/, '') + '/' + path;
    OpenPath(path);
  }

  // Organic morphing blobs for the ambient orb. Each outline is a closed
  // Catmull-Rom spline whose vertex radii are driven by several *travelling*
  // sine harmonics, so the waves continuously expand/contract and drift around
  // the ring — the shape deforms rather than a fixed silhouette merely turning.
  // Lines only (no fills/filters), morphed at ~30fps, so it stays responsive.
  const TAU = Math.PI * 2;
  const rand = (a, b) => a + Math.random() * (b - a);

  function makeBlobCfg(base, color, op) {
    const harmonics = [];
    // Distinct lobe counts (drawn without replacement) so the harmonics always
    // BEAT against each other and the outline morphs — a ring whose waves share
    // one lobe count just rigidly rotates as a symmetric shape.
    const pool = [1, 2, 3, 4, 5, 6];
    for (let i = pool.length - 1; i > 0; i--) {
      const j = Math.floor(Math.random() * (i + 1));
      [pool[i], pool[j]] = [pool[j], pool[i]];
    }
    const n = 3 + Math.floor(Math.random() * 2);   // 3-4 overlapping waves
    for (let i = 0; i < n; i++) {
      harmonics.push({
        m: pool[i],                                 // distinct lobe count
        amp: rand(3, 9),                            // wave height
        speed: rand(0.25, 0.75) * (Math.random() < 0.5 ? -1 : 1), // travel dir/rate
        phase: Math.random() * TAU,
      });
    }
    // Slow overall breathing (m=0 ⇒ uniform in angle) so the whole ring visibly
    // expands and contracts, not just its outline wobbling.
    const breathe = {
      amp: rand(4, 8),
      speed: rand(0.4, 0.8) * (Math.random() < 0.5 ? -1 : 1),
      phase: Math.random() * TAU,
    };
    // Speaking ripple = sum of several integer-frequency lobes (so the curve
    // still closes seamlessly) with RANDOM amplitude/phase/rate each, giving
    // irregular, asymmetric wave spacing and heights that bob independently.
    const ripples = [];
    const rc = 5 + Math.floor(Math.random() * 3); // 5-7 components
    for (let i = 0; i < rc; i++) {
      ripples.push({
        k: 12 + Math.floor(Math.random() * 15), // 12..26 lobes (narrow)
        amp: 0.4 + Math.random() * 0.9,
        phase: Math.random() * TAU,
        rate: 2 + Math.random() * 6,             // in-place bobbing speed
      });
    }
    return { base, color, op, harmonics, breathe, ripplePhase: Math.random() * TAU, ripples };
  }
  const blobCfgs = [
    makeBlobCfg(86, '#4aa8ff', 0.9),
    makeBlobCfg(82, '#6cbcff', 0.75),
    makeBlobCfg(78, '#3a90ff', 0.6),
    makeBlobCfg(84, '#8fd0ff', 0.55),
  ];
  const BLOB_N = 60;                                // sample points per outline (enough for very tight speaking ripples)
  let blobPaths = blobCfgs.map(() => '');

  function buildBlobPath(cfg, t, sp) {
    const cx = 120, cy = 120, pts = [];
    for (let i = 0; i < BLOB_N; i++) {
      const a = (i / BLOB_N) * TAU;
      // while speaking, damp the slow travelling wobble (so it stops rotating)
      const wob = 1 - 0.6 * _speakLevel;
      // overall breathe term (m=0): expands/contracts the whole ring over time
      let r = cfg.base + cfg.breathe.amp * wob * Math.sin(t * cfg.breathe.speed + cfg.breathe.phase);
      for (const h of cfg.harmonics) r += h.amp * wob * Math.sin(h.m * a + t * h.speed + h.phase);
      if (_speakLevel > 0.01) {
        // Tall, IN-PLACE audio waves: fixed angular lobes (no a-travel ⇒ no
        // rotation) whose height is modulated over time → bobs like an equaliser.
        // swelling envelope → louder/quieter bursts, so peaks rise higher and
        // troughs dip lower like a real speech waveform
        const env = 0.45 + 0.95 * Math.abs(Math.sin(sp * 2.3 + cfg.ripplePhase));
        let rip = 0;
        for (const c of cfg.ripples) {
          rip += c.amp * Math.sin(c.k * a + c.phase) * Math.sin(sp * c.rate + c.phase);
        }
        r += _speakLevel * _speakGate * 12 * env * rip;
      }
      pts.push([cx + r * Math.cos(a), cy + r * Math.sin(a)]);
    }
    const f = (v) => v.toFixed(1);
    let d = `M ${f(pts[0][0])} ${f(pts[0][1])} `;
    for (let i = 0; i < BLOB_N; i++) {
      const p0 = pts[(i - 1 + BLOB_N) % BLOB_N], p1 = pts[i], p2 = pts[(i + 1) % BLOB_N], p3 = pts[(i + 2) % BLOB_N];
      const c1x = p1[0] + (p2[0] - p0[0]) / 6, c1y = p1[1] + (p2[1] - p0[1]) / 6;
      const c2x = p2[0] - (p3[0] - p1[0]) / 6, c2y = p2[1] - (p3[1] - p1[1]) / 6;
      d += `C ${f(c1x)} ${f(c1y)} ${f(c2x)} ${f(c2y)} ${f(p2[0])} ${f(p2[1])} `;
    }
    return d + 'Z';
  }

  // ~30fps morph loop. The waves advance faster while thinking, and fastest
  // while speaking — where a tight high-frequency ripple makes it read like an
  // audio waveform. _speakLevel ramps the ripple in/out smoothly.
  let _blobRaf, _blobLast = null, _blobPhase = 0, _speakPhase = 0, _blobAcc = 0, _speakLevel = 0, _glowPhase = 0;
  // Glow drift offsets (px), bound to the aura/core transforms in the markup.
  let auraTX = 0, auraTY = 0, coreTX = 0, coreTY = 0;
  // Speech "gate": waves flatten during brief random pauses, like silences.
  let _speakGate = 0, _gateOpen = true, _gateLeft = 0;
  function blobTick(now) {
    _blobRaf = requestAnimationFrame(blobTick);
    if (_blobLast === null) { _blobLast = now; return; }
    const dt = (now - _blobLast) / 1000; _blobLast = now;
    // Freeze the travelling phase while speaking so the orb stops rotating;
    // the speaking ripple bobs in place instead, driven by _speakPhase.
    if (!speaking) _blobPhase += dt * (loading ? 4 : 1);
    _speakPhase += dt * (speaking ? 6 : 0);
    // Glow drifts at the same cadence as the waves' motion.
    _glowPhase += dt * (speaking ? 6 : (loading ? 4 : 1));
    _speakLevel += ((speaking ? 1 : 0) - _speakLevel) * Math.min(1, dt * 6);
    if (speaking) {
      _gateLeft -= dt;
      if (_gateLeft <= 0) {
        _gateOpen = !_gateOpen;
        // active bursts last 0.5–1.5s; pauses (silences) 0.15–0.4s
        _gateLeft = _gateOpen ? 0.5 + Math.random() * 1.0 : 0.15 + Math.random() * 0.25;
      }
      _speakGate += ((_gateOpen ? 1 : 0) - _speakGate) * Math.min(1, dt * 14);
    } else {
      _speakGate = 0; _gateOpen = true; _gateLeft = 0;
    }
    _blobAcc += dt;
    if (_blobAcc < 1 / 30) return;
    _blobAcc = 0;
    // drift the glow within the orb (Lissajous so it wanders, not a clean circle)
    const g = _glowPhase;
    coreTX = Math.cos(g * 0.5) * 14 + Math.cos(g * 0.33) * 6;
    coreTY = Math.sin(g * 0.45) * 14 + Math.sin(g * 0.39) * 6;
    auraTX = coreTX * 0.5;
    auraTY = coreTY * 0.5;
    blobPaths = blobCfgs.map((c) => buildBlobPath(c, _blobPhase, _speakPhase));
  }
  onMount(() => {
    _blobRaf = requestAnimationFrame(blobTick);
    return () => cancelAnimationFrame(_blobRaf);
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
  let mcpNewType = 'stdio';
  let mcpNewCommand = '';
  let mcpNewArgs = '';
  let mcpNewURL = '';
  let mcpNewSubscribe = '';
  let mcpAdding = false;

  // Marketplace
  let showMarketplace = false;
  let marketplaceCatalog = [];
  let installedNames = [];
  let hasNpx = true;
  let mpSecretInput = {};
  let mpInstalling = {};
  let mpFilter = 'All';

  let mcpQrDialog = false;
  let mcpQrImage = '';
  let mcpQrLoading = false;
  let mcpQrServer = '';

  // Voice
  let recording = false;
  let transcribing = false;
  let sttError = '';
  let voiceLevel = 0;
  let audioDevices = [];
  let selectedDevice = '';
  let speaking = false;
  let voiceEnabled = true;
  let autoSpeakEnabled = localStorage.getItem('autoSpeak') === '1';
  let ttsVoice = 'es_ES';
  let ttsVoices = [];          // [{code,name,language,quality,installed}] from Piper catalog
  $: voiceLanguages = [...new Set(ttsVoices.map(v => v.language))];

  // Short preview phrase per language, spoken when a voice is selected.
  const voiceSamples = {
    Spanish: 'Hola, soy OpenUAI. Así sonará mi voz.',
    English: 'Hi, I am OpenUAI. This is how I sound.',
    French: 'Bonjour, je suis OpenUAI. Voici ma voix.',
    German: 'Hallo, ich bin OpenUAI. So klinge ich.',
    Italian: 'Ciao, sono OpenUAI. Ecco la mia voce.',
    Portuguese: 'Olá, eu sou OpenUAI. Esta é a minha voz.',
    Dutch: 'Hallo, ik ben OpenUAI. Zo klink ik.',
    Catalan: 'Hola, sóc OpenUAI. Aquesta és la meva veu.',
    Russian: 'Привет, я OpenUAI. Вот как я звучу.',
  };
  function voiceSample(lang) {
    return voiceSamples[lang] || 'Hello, I am OpenUAI. This is a voice sample.';
  }
  let piperSupported = true;
  let voiceDownloading = false;
  let voiceDownloadError = '';
  let sttLanguage = 'auto';
  let wakeWord = '';            // name that triggers hands-free listening
  let wakeListening = false;    // continuous wake-word listening on/off (session only)
  let wakeAwaitingCmd = false;  // wake word fired, capturing the command utterance
  let wakeStatus = '';          // wake model download / status message
  let wakeHeardTimer;           // dismiss timer for the transient "heard (no wake word)" bubble
  let wakeSession = false;      // conversation window open: capturing without the wake word (mic blinks)
  let workDir = '';             // dir the agent saves files into; resolves bare file names for click-to-open
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

  // Beta features
  let betaLipReading = false;

  // Lip reading
  let lipRecording = false;
  let lipTranscribing = false;
  let lipModelReady = false;
  let showLipDownloadDialog = false;
  let lipDownloading = false;
  let lipDownloadProgress = 0;

  // Update dialog
  let showUpdateDialog = false;
  let updateInfo = null;
  let updating = false;
  let updateError = '';
  let appVersion = 'dev';

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
    piperSupported = await PiperSupported();
    ttsVoices = await GetTTSVoices();
    sttLanguage = await GetSTTLanguage();
    wakeWord = await GetWakeWord();
    audioDevices = await GetAudioDevices() || [];
    selectedDevice = await GetAudioDevice();
    appVersion = await GetVersion();
    betaLipReading = await GetBetaLipReading();
    lipModelReady = await LipReadingModelReady();
    workDir = await GetWorkDir();
    if (!isReady) showSettings = true;

    // Listen for MCP auth completion
    EventsOn('mcp_auth_done', async (result) => {
      if (result.error) alert('Auth failed: ' + result.error);
      await refreshMCPServers();
    });

    // Listen for lip reading download progress
    EventsOn('lipreading_download_progress', (downloaded) => {
      lipDownloadProgress = Math.round(downloaded / 1024 / 1024);
    });

    // Listen for update availability
    EventsOn('update_available', (info) => {
      updateInfo = info;
      showUpdateDialog = true;
    });

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
          queueSpeak(step.content);
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

    // Wake model download / status updates.
    EventsOn('wake_status', (s) => { wakeStatus = s || ''; });

    // Conversation window opened/closed — capturing without the wake word.
    // Drives the blinking mic so it's clear it's still listening for follow-ups.
    EventsOn('wake_session', (active) => { wakeSession = !!active; });

    // Wake word fired; the listener is now capturing the spoken command.
    EventsOn('wake_listening', () => {
      wakeAwaitingCmd = true;
      setTimeout(() => { wakeAwaitingCmd = false; }, 6000);
    });

    // An utterance was captured and is being transcribed — show a "[...]"
    // placeholder bubble so it's visible that the mic picked something up.
    EventsOn('wake_capturing', () => {
      if (messages.some((m) => m.wakePending)) return;
      messages = [...messages, { role: 'user', content: '[...]', wakePending: true }];
      stickToBottom = true;
    });

    // The captured utterance was dropped (no wake word, empty, or STT error).
    // If we have a transcript, show it briefly so it's clear transcription
    // happened but "<wakeWord>" wasn't detected; otherwise just drop the [...].
    EventsOn('wake_discard', (heard) => {
      messages = messages.filter((m) => !m.wakePending && !m.wakeHeard);
      const txt = (heard || '').trim();
      if (!txt) return;
      messages = [...messages, { role: 'user', content: txt, wakeHeard: true }];
      clearTimeout(wakeHeardTimer);
      wakeHeardTimer = setTimeout(() => { messages = messages.filter((m) => !m.wakeHeard); }, 3500);
    });

    // Wake-word listener heard "<name>, <message>" — auto-send the message.
    EventsOn('wake_message', (text) => {
      wakeAwaitingCmd = false;
      messages = messages.filter((m) => !m.wakePending && !m.wakeHeard);
      if (loading || !text) return;
      input = text;
      send();
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

    // If a wake word is already configured, start hands-free listening
    // automatically so the selector reflects that it's active on launch.
    // Done last, after the wake_* handlers above are registered, so no early
    // capture/discard event is missed.
    if (wakeWord && wakeWord.trim()) {
      wakeListening = true;
      await toggleWakeListening();
    }
  });

  // Auto-scroll to the bottom only when the user is already there. If they've
  // scrolled up to read history, frequent updates (streaming, tool steps, voice
  // levels) must NOT yank them back down.
  let stickToBottom = true;
  function onChatScroll() {
    if (!chatEl) return;
    const dist = chatEl.scrollHeight - chatEl.scrollTop - chatEl.clientHeight;
    stickToBottom = dist < 80;
  }
  afterUpdate(() => {
    if (chatEl && stickToBottom) chatEl.scrollTop = chatEl.scrollHeight;
  });

  function respondPerm(level, approved) {
    showPermDialog = false;
    RespondPermission(level, approved);
  }

  async function doUpdate() {
    updating = true;
    updateError = '';
    const err = await ApplyUpdate(updateInfo.download_url);
    if (err) {
      updateError = err;
      updating = false;
    } else {
      updateError = '';
      showUpdateDialog = false;
      // Show restart notice — the binary has been replaced
      messages = [...messages, { role: 'assistant', content: `Updated to **${updateInfo.new_version}**. Please restart OpenUAI to use the new version.` }];
      updating = false;
    }
  }

  // --- Lip reading ---

  async function lipBtnDown() {
    if (lipRecording || lipTranscribing || loading) return;
    if (!lipModelReady) {
      showLipDownloadDialog = true;
      return;
    }
    const err = await StartLipRecording();
    if (err) {
      alert('Camera error: ' + err);
      return;
    }
    lipRecording = true;
  }

  async function lipBtnUp() {
    if (!lipRecording) return;
    lipRecording = false;
    lipTranscribing = true;
    const result = await StopLipRecording();
    lipTranscribing = false;
    if (result.error && result.error !== '') {
      alert('Lip reading error: ' + result.error);
      return;
    }
    if (result.text) {
      input = result.text;
      await send();
    }
  }

  async function downloadLipModel() {
    lipDownloading = true;
    lipDownloadProgress = 0;
    const err = await DownloadLipReadingModel();
    lipDownloading = false;
    if (err) {
      alert('Download failed: ' + err);
      return;
    }
    lipModelReady = true;
    showLipDownloadDialog = false;
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
    stickToBottom = true; // sending a message jumps back to the latest
    loading = true;

    const resp = await SendMessage(content);
    loading = false;
    aborting = false;

    totalCost = resp.cost_usd || 0;
    totalTokens = (resp.input_tokens || 0) + (resp.output_tokens || 0);
  }

  // Editing a previous user message and continuing from there.
  let editingIndex = -1;
  let editText = '';
  function startEdit(i) {
    if (loading) return;
    editingIndex = i;
    editText = messages[i].content;
  }
  function cancelEdit() {
    editingIndex = -1;
    editText = '';
  }
  async function saveEdit(i) {
    const content = editText.trim();
    if (!content || loading) return;
    // ordinal of this message among user messages (what the agent rewinds to)
    let n = 0;
    for (let j = 0; j < i; j++) if (messages[j].role === 'user') n++;
    // drop this message and everything after, then re-add the edited question
    messages = [...messages.slice(0, i), { role: 'user', content }];
    editingIndex = -1;
    editText = '';
    loading = true;
    const resp = await EditMessage(n, content);
    loading = false;
    aborting = false;
    totalCost = resp.cost_usd || 0;
    totalTokens = (resp.input_tokens || 0) + (resp.output_tokens || 0);
  }
  function editKeydown(e, i) {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); saveEdit(i); }
    else if (e.key === 'Escape') { e.preventDefault(); cancelEdit(); }
  }

  let aborting = false;
  async function abort() {
    if (!loading) return;
    aborting = true;
    await AbortAgent();
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
    if (!mcpNewName) return;
    const args = mcpNewArgs ? mcpNewArgs.split(' ').filter(Boolean) : [];
    const subscribe = mcpNewSubscribe ? mcpNewSubscribe.split(',').map(s => s.trim()).filter(Boolean) : [];
    const url = mcpNewType === 'http' ? mcpNewURL : '';
    const command = mcpNewType === 'stdio' ? mcpNewCommand : '';
    const err = await AddMCPServer(mcpNewName, command, args, {}, true, subscribe, url);
    if (err) {
      alert('Failed to add MCP server: ' + err);
    } else {
      mcpNewName = '';
      mcpNewType = 'stdio';
      mcpNewCommand = '';
      mcpNewArgs = '';
      mcpNewURL = '';
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
    installedNames = (await GetInstalledNames()) || [];
  }

  async function openMarketplace() {
    showMarketplace = !showMarketplace;
    if (showMarketplace) {
      marketplaceCatalog = await GetMarketplace();
      installedNames = (await GetInstalledNames()) || [];
      hasNpx = await CheckNpx();
    }
  }

  async function installFromMarketplace(name) {
    const entry = marketplaceCatalog.find(e => e.name === name);
    if (!entry) return;
    mpInstalling[name] = true;
    mpInstalling = mpInstalling;
    const secret = mpSecretInput[name] || '';
    const err = await InstallMarketplace(name, secret);
    mpInstalling[name] = false;
    mpInstalling = mpInstalling;
    if (err) {
      alert('Install failed: ' + err);
      return;
    }
    installedNames = [...installedNames, name];
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
    if (result.error) {
      sttError = result.error;
      setTimeout(() => { sttError = ''; }, 4000);
      return;
    }
    if (result.text) {
      input = result.text;
      await send();
    }
  }

  // Month names for spelling out dates so TTS reads them as dates, not digits.
  const SPEECH_MONTHS = {
    es: ['enero', 'febrero', 'marzo', 'abril', 'mayo', 'junio', 'julio', 'agosto', 'septiembre', 'octubre', 'noviembre', 'diciembre'],
    en: ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'],
  };

  // Rewrite numeric dates into spoken words so e.g. "2026-06-09" is read as
  // "9 de junio de 2026" instead of "2026 guion 06 guion 09". Handles ISO
  // (YYYY-MM-DD) and day-first slash dates (DD/MM/YYYY). Language follows the
  // selected TTS voice (Spanish by default).
  function spellDates(text) {
    const lang = (ttsVoice || '').toLowerCase().startsWith('en') ? 'en' : 'es';
    const M = SPEECH_MONTHS[lang];
    const fmt = (day, mi, year) =>
      lang === 'en' ? `${M[mi]} ${day}, ${year}` : `${day} de ${M[mi]} de ${year}`;
    return text
      .replace(/\b(\d{4})-(\d{1,2})-(\d{1,2})\b/g, (m, y, mo, d) => {
        const mi = +mo - 1;
        return mi >= 0 && mi < 12 && +d >= 1 && +d <= 31 ? fmt(+d, mi, y) : m;
      })
      .replace(/\b(\d{1,2})\/(\d{1,2})\/(\d{4})\b/g, (m, d, mo, y) => {
        const mi = +mo - 1;
        return mi >= 0 && mi < 12 && +d >= 1 && +d <= 31 ? fmt(+d, mi, y) : m;
      });
  }

  // Prepare text for TTS: drop code blocks and emojis (they shouldn't be read
  // aloud), strip markdown, and — crucially — process line by line so each list
  // item / line becomes its own spoken sentence. Lines without terminal
  // punctuation get a period so the TTS pauses between items instead of running
  // them together.
  function cleanForSpeech(text) {
    const stripped = text
      .replace(/```[\s\S]*?```/g, '\n')  // fenced code blocks
      .replace(/`[^`]*`/g, ' ')          // inline code
      .replace(/!\[[^\]]*\]\([^)]*\)/g, ' ')   // images: drop entirely (don't read photos/URLs)
      .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')  // links: keep text, drop URL
      .replace(/https?:\/\/\S+/g, ' ')   // bare URLs
      // emoji / pictographs / symbols / flags / variation selectors
      .replace(/[\u{1F000}-\u{1FAFF}\u{2600}-\u{27BF}\u{2B00}-\u{2BFF}\u{2190}-\u{21FF}\u{1F1E6}-\u{1F1FF}\u{FE00}-\u{FE0F}\u{200D}\u{20E3}]/gu, '');

    const sentences = stripped
      .split('\n')
      .map((line) => line
        .replace(/^\s*[-*+•]\s+/, '')       // bullet list marker
        .replace(/^\s*\d+[.)]\s+/, '')      // numbered list marker
        .replace(/[#*_`~\[\]()>|]/g, '')    // leftover markdown
        .replace(/[ \t]+/g, ' ')
        .trim())
      .filter(Boolean)
      // Capitalize the first letter and add terminal punctuation so each line/
      // item reads as its own sentence — Piper only pauses at a period when the
      // next word is capitalized, so lowercase list fragments otherwise run on.
      .map((line) => line.charAt(0).toUpperCase() + line.slice(1))
      .map((line) => /[.!?:;…,]$/.test(line) ? line : line + '.');

    // Join into a SINGLE line separated by the periods we just added: Piper
    // (and espeak) split sentences on punctuation and pause between them, but
    // process stdin line-by-line — so keeping it one line avoids losing items.
    return spellDates(sentences.join(' '));
  }

  // Synthesize + play one utterance, resolving when playback finishes.
  async function playTTS(clean) {
    const result = await SpeakText(clean);
    if (result.error) throw new Error(result.error);
    const fmt = result.format || 'wav';
    const audio = new Audio('data:audio/' + fmt + ';base64,' + result.audio_base64);
    await new Promise((resolve) => {
      audio.onended = resolve;
      audio.onerror = resolve;
      audio.play().catch(resolve);
    });
  }

  // Manual: triggered by the speaker button on a message.
  async function speakMessage(text) {
    if (speaking) return;
    const clean = cleanForSpeech(text);
    if (!clean) return;
    speaking = true;
    try {
      await playTTS(clean);
    } catch (e) {
      alert('TTS failed: ' + e.message);
    }
    speaking = false;
  }

  // Auto-speak: queue assistant responses and read them in order.
  let speakQueue = [];
  async function queueSpeak(text) {
    if (!autoSpeakEnabled) return;
    const clean = cleanForSpeech(text);
    if (!clean) return;
    speakQueue.push(clean);
    if (speaking) return;        // a drain loop is already running
    speaking = true;
    while (speakQueue.length) {
      try { await playTTS(speakQueue.shift()); } catch (e) { /* skip on error */ }
    }
    speaking = false;
  }

  async function changeTTSVoice() {
    voiceDownloading = true;
    voiceDownloadError = '';
    const err = await SetTTSVoice(ttsVoice);
    voiceDownloading = false;
    if (err) {
      voiceDownloadError = err;
      return;
    }
    ttsVoices = await GetTTSVoices();
    // play a short sample in the selected voice's language
    const v = ttsVoices.find((x) => x.code === ttsVoice);
    speakMessage(voiceSample(v && v.language));
  }

  async function changeSTTLanguage() {
    await SetSTTLanguage(sttLanguage);
  }

  async function saveWakeWord() {
    await SetWakeWord(wakeWord);
  }

  // Toggle the continuous wake-word listener. On failure (e.g. no wake word set)
  // revert the switch and surface the reason.
  async function toggleWakeListening() {
    if (!wakeListening) wakeSession = false; // closing the listener ends any open conversation
    const err = await SetWakeListening(wakeListening);
    if (err) {
      wakeListening = false;
      wakeSession = false;
      sttError = err;
      setTimeout(() => { if (sttError === err) sttError = ''; }, 4000);
    }
  }

  // Pause listening whenever the app is otherwise using audio or busy, so the
  // wake listener never transcribes the assistant's own TTS or a push-to-talk.
  $: if (wakeListening) SetWakePaused(loading || speaking || transcribing || recording);

  function handleGlobalKeydown(e) {
    if (e.key === 'Escape' && loading && !aborting) {
      e.preventDefault();
      abort();
    }
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

<svelte:window on:keydown={handleGlobalKeydown} />

<div class="orb-bg" class:active={loading || speaking}>
  <div class="orb-aura" style="transform: translate({auraTX}px, {auraTY}px)"></div>
  <div class="orb-core" style="transform: translate({coreTX}px, {coreTY}px)"></div>
  {#each blobCfgs as c, i}
    <svg class="orb-ring" viewBox="0 0 240 240" aria-hidden="true"
         style="color:{c.color}; opacity:{c.op};">
      <path class="glow" d={blobPaths[i]} fill="none" stroke="currentColor"/>
      <path class="line" d={blobPaths[i]} fill="none" stroke="currentColor"/>
    </svg>
  {/each}
</div>

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
      <div class="settings-group">
        <div class="settings-group-title">LLM Provider</div>
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

      <div class="settings-group">
        <div class="settings-group-title">Voice</div>
        <div class="setting-row">
          <label>Auto-speak</label>
          <label class="switch-label">
            <input type="checkbox" bind:checked={autoSpeakEnabled}
                   on:change={() => localStorage.setItem('autoSpeak', autoSpeakEnabled ? '1' : '0')} />
            <span>Read responses aloud automatically</span>
          </label>
        </div>
        <div class="setting-row">
          <label>Voice</label>
          <select bind:value={ttsVoice} on:change={changeTTSVoice} disabled={voiceDownloading}>
            {#each voiceLanguages as lang}
              <optgroup label={lang}>
                {#each ttsVoices.filter(v => v.language === lang) as v}
                  <option value={v.code}>{v.name} · {v.quality}{v.installed ? ' ✓' : ''}</option>
                {/each}
              </optgroup>
            {/each}
          </select>
        </div>
        {#if voiceDownloading}
          <div class="setting-row"><label></label><span class="voice-status">Downloading voice… (first use only)</span></div>
        {:else if voiceDownloadError}
          <div class="setting-row"><label></label><span class="voice-status voice-status-err">{voiceDownloadError}</span></div>
        {:else if !piperSupported}
          <div class="setting-row"><label></label><span class="voice-status">Neural voice unavailable here — using espeak-ng</span></div>
        {/if}
        <div class="setting-row">
          <label>STT Language</label>
          <select bind:value={sttLanguage} on:change={changeSTTLanguage}>
            {#each sttLanguages as lang}
              <option value={lang.code}>{lang.label}</option>
            {/each}
          </select>
        </div>
        <div class="setting-row">
          <label>Wake word</label>
          <input type="text" class="wake-input" bind:value={wakeWord} on:change={saveWakeWord}
                 placeholder="e.g. Pepito" />
        </div>
        <div class="setting-row"><label></label><span class="voice-status">Turn on the listen toggle next to the mic, then say "{wakeWord || 'name'}, …" to send hands-free.</span></div>
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
      </div>

      <div class="beta-section">
        <div class="beta-header">Beta Features</div>
        <label class="beta-toggle">
          <input type="checkbox" bind:checked={betaLipReading} on:change={() => SetBetaLipReading(betaLipReading)} />
          <span>Lip Reading <span class="beta-badge">BETA</span></span>
          <span class="beta-desc">Visual speech recognition from camera (English only, ~1 GB model)</span>
        </label>
      </div>

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
              <label>Type</label>
              <select bind:value={mcpNewType}>
                <option value="stdio">STDIO (local command)</option>
                <option value="http">HTTP (remote URL)</option>
              </select>
            </div>
            {#if mcpNewType === 'stdio'}
              <div class="setting-row">
                <label>Command</label>
                <input bind:value={mcpNewCommand} placeholder="uv" />
              </div>
              <div class="setting-row">
                <label>Args</label>
                <input bind:value={mcpNewArgs} placeholder="run main.py (space-separated)" />
              </div>
            {:else}
              <div class="setting-row">
                <label>URL</label>
                <input bind:value={mcpNewURL} placeholder="https://mcp.example.com/sse" />
              </div>
            {/if}
            <div class="setting-row">
              <label>Subscribe</label>
              <input bind:value={mcpNewSubscribe} placeholder="whatsapp://messages/inbox (optional)" />
            </div>
            <div class="setting-row">
              <label></label>
              <button on:click={addMCPServer} disabled={!mcpNewName || (mcpNewType === 'stdio' ? !mcpNewCommand : !mcpNewURL)}>Save</button>
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
            <span class="mcp-srv-detail">{srv.url || srv.command}</span>
            {#if srv.connected}
              <span class="mcp-srv-stats">{srv.tools}T {srv.resources}R</span>
            {/if}
            {#if srv.needs_auth}
              <button class="mcp-auth-btn" on:click={() => AuthMCPServer(srv.name)}>Auth</button>
            {:else if srv.url && srv.connected}
              <button class="mcp-link-btn" on:click={() => ReauthMCPServer(srv.name)}>Reauth</button>
            {:else if srv.has_auth}
              <button class="mcp-link-btn" on:click={() => linkMCPServer(srv.name)}>Link</button>
            {/if}
            <button class="mcp-remove-btn" on:click={() => removeMCPServer(srv.name)}>x</button>
          </div>
        {/each}
      </div>

      <div class="marketplace-section">
        <div class="mcp-settings-header">
          <span class="mcp-settings-title">Marketplace</span>
          <button class="mcp-add-btn" on:click={openMarketplace}>{showMarketplace ? 'Hide' : 'Browse'}</button>
        </div>
        {#if showMarketplace}
          {#if !hasNpx}
            <div class="mp-banner">Node.js is required for marketplace servers. Install from <a href="https://nodejs.org" target="_blank">nodejs.org</a></div>
          {/if}
          <div class="mp-filters">
            {#each ['All', 'Office', 'Dev', 'Search', 'Utilities'] as cat}
              <button class="mp-filter-btn" class:mp-filter-active={mpFilter === cat} on:click={() => mpFilter = cat}>{cat}</button>
            {/each}
          </div>
          <div class="mp-grid">
            {#each marketplaceCatalog.filter(e => mpFilter === 'All' || e.category === mpFilter) as entry}
              {@const installed = installedNames.includes(entry.name)}
              <div class="mp-card" class:mp-card-installed={installed}>
                <div class="mp-card-header">
                  <span class="mp-card-icon">{entry.icon}</span>
                  <span class="mp-card-name">{entry.name}</span>
                  <span class="mp-card-cat">{entry.category}</span>
                </div>
                <div class="mp-card-desc">{entry.description}</div>
                {#if installed}
                  <div class="mp-card-status">Installed</div>
                {:else}
                  {#if entry.auth_type === 'api_key'}
                    <div class="mp-card-auth">
                      <input type="password" placeholder={entry.auth_label} bind:value={mpSecretInput[entry.name]} />
                    </div>
                  {/if}
                  <button class="mp-install-btn" disabled={mpInstalling[entry.name] || (entry.auth_type === 'api_key' && !mpSecretInput[entry.name]) || !hasNpx} on:click={() => installFromMarketplace(entry.name)}>
                    {mpInstalling[entry.name] ? 'Installing...' : 'Install'}
                  </button>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
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

  {#if showUpdateDialog && updateInfo}
    <div class="perm-overlay">
      <div class="perm-dialog update-dialog">
        <h3>Update Available</h3>
        <p class="update-versions">{updateInfo.current_version} &rarr; <strong>{updateInfo.new_version}</strong></p>
        {#if updateInfo.release_notes}
          <div class="update-notes markdown">{@html marked(updateInfo.release_notes)}</div>
        {/if}
        {#if updateError}
          <p class="update-error">{updateError}</p>
        {/if}
        <div class="perm-actions">
          <button class="perm-deny" on:click={() => { showUpdateDialog = false; }} disabled={updating}>Later</button>
          <button class="perm-session" on:click={async () => { SkipVersion(updateInfo.new_version); showUpdateDialog = false; }} disabled={updating}>Skip this version</button>
          <button class="perm-forever" on:click={doUpdate} disabled={updating}>
            {#if updating}Updating...{:else}Update now{/if}
          </button>
        </div>
      </div>
    </div>
  {/if}

  {#if showLipDownloadDialog}
    <div class="perm-overlay">
      <div class="perm-dialog update-dialog">
        <h3>Lip Reading</h3>
        <p>This feature reads lips from your camera to transcribe speech <strong>in English only</strong>.</p>
        <p>It requires downloading a ~1 GB AI model. The model is stored locally and only downloaded once.</p>
        {#if lipDownloading}
          <p class="lip-progress">Downloading... {lipDownloadProgress} MB</p>
          <div class="lip-progress-bar"><div class="lip-progress-fill" style="width: {Math.min(lipDownloadProgress / 1000 * 100, 100)}%"></div></div>
        {/if}
        <div class="perm-actions">
          <button class="perm-deny" on:click={() => { showLipDownloadDialog = false; }} disabled={lipDownloading}>Cancel</button>
          <button class="perm-forever" on:click={downloadLipModel} disabled={lipDownloading}>
            {#if lipDownloading}Downloading...{:else}Download model{/if}
          </button>
        </div>
      </div>
    </div>
  {/if}

  <div class="chat" bind:this={chatEl} on:click={onChatClick} on:scroll={onChatScroll}>
    {#each messages as msg, i}
      {#if msg.role === 'user'}
        <div class="message user" class:wake-pending={msg.wakePending} class:wake-heard={msg.wakeHeard} title={msg.wakeHeard ? `Oído, pero sin la palabra de activación ("${wakeWord}")` : ''}>
          {#if editingIndex === i}
            <textarea class="edit-area" bind:value={editText} on:keydown={(e) => editKeydown(e, i)} rows="2"></textarea>
            <div class="edit-actions">
              <button class="edit-cancel" on:click={cancelEdit}>Cancel</button>
              <button class="edit-save" on:click={() => saveEdit(i)} disabled={!editText.trim()}>Save & resend</button>
            </div>
          {:else}
            <div class="message-content">{msg.content}</div>
            {#if !msg.wakePending && !msg.wakeHeard}
              <button class="edit-btn" on:click={() => startEdit(i)} disabled={loading} title="Edit & continue from here" aria-label="Edit message">
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg>
              </button>
            {/if}
          {/if}
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
  </div>

  <div class="input-area">
    {#if betaLipReading}
    <button class="lip-btn" class:lip-recording={lipRecording} class:lip-transcribing={lipTranscribing} on:mousedown={lipBtnDown} on:mouseup={lipBtnUp} on:mouseleave={lipBtnUp} on:touchstart|preventDefault={lipBtnDown} on:touchend|preventDefault={lipBtnUp} disabled={loading || lipTranscribing} title={lipRecording ? 'Release to read lips' : lipTranscribing ? 'Reading lips...' : 'Hold to lip-read (English)'}>
      {#if lipRecording}
        <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><rect x="3" y="3" width="10" height="10" rx="1"/></svg>
      {:else if lipTranscribing}
        ...
      {:else}
        <svg width="18" height="16" viewBox="0 0 24 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M2 10c0 0 3-6 10-6s10 6 10 6"/><path d="M2 10c0 0 3 6 10 6s10-6 10-6"/><circle cx="12" cy="10" r="1.5" fill="currentColor" stroke="none"/></svg>
      {/if}
    </button>
    {/if}
    <label class="wake-toggle" class:wake-on={wakeListening} title={wakeWord ? `Hands-free: say "${wakeWord}, …"` : 'Set a wake word in Settings first'}>
      <input type="checkbox" bind:checked={wakeListening} on:change={toggleWakeListening} />
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M2 10c0 0 3-6 10-6s10 6 10 6"/><path d="M2 10c0 0 3 6 10 6s10-6 10-6"/><circle cx="12" cy="10" r="1.5" fill="currentColor" stroke="none"/></svg>
      <span class="wake-track"><span class="wake-knob"></span></span>
    </label>
    <button class="mic-btn" class:mic-recording={recording} class:mic-transcribing={transcribing} class:mic-session={wakeSession && !recording && !transcribing} on:mousedown={startRecording} on:mouseup={stopRecordingAndSend} on:mouseleave={stopRecordingAndSend} on:touchstart|preventDefault={startRecording} on:touchend|preventDefault={stopRecordingAndSend} disabled={loading || transcribing} title={wakeSession ? 'Conversation open — talk without the wake word' : recording ? 'Release to send' : transcribing ? 'Transcribing...' : 'Hold to talk'}>
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
    {:else if wakeStatus}
      <div class="transcribing-indicator wake-listening-ind">{wakeStatus}</div>
    {:else if wakeAwaitingCmd}
      <div class="transcribing-indicator wake-listening-ind">🎙 Listening…</div>
    {:else if sttError}
      <div class="transcribing-indicator stt-error">{sttError}</div>
    {/if}
    <textarea
      bind:this={textareaEl}
      bind:value={input}
      on:keydown={handleKeydown}
      placeholder={transcribing ? 'Transcribing...' : 'Give me a task...'}
      rows="1"
      disabled={loading || !isReady || transcribing}
    ></textarea>
    {#if loading}
      <button class="stop-btn" on:click={abort} disabled={aborting} title="Stop the running task (Esc)">
        {#if aborting}Stopping...{:else}Stop{/if}
      </button>
    {:else}
      <button on:click={send} disabled={!isReady || !input.trim()}>Send</button>
    {/if}
    <button class="clear-btn" on:click={clearChat}>Clear</button>
  </div>
</main>

<style>
  :global(select) {
    background: #070b12 !important;
    color: #eee !important;
    -webkit-text-fill-color: #eee !important;
    color-scheme: dark;
    opacity: 1 !important;
  }
  :global(select option) {
    background: #070b12;
    color: #eee;
  }
  :global(body) {
    margin: 0;
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
    background:
      radial-gradient(1100px 700px at 50% 38%, rgba(33,120,220,0.12), transparent 70%),
      radial-gradient(900px 600px at 85% 110%, rgba(20,90,180,0.10), transparent 70%),
      #04070d;
    color: #dce6f2;
  }

  main {
    display: flex;
    flex-direction: column;
    height: 100vh;
    position: relative;
    z-index: 1;            /* sits above the ambient orb (z-index:0) */
  }

  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 0.5rem 1rem;
    background: rgba(9,14,23,0.9);
    border-bottom: 1px solid #163050;
  }

  header h1 {
    margin: 0;
    font-size: 1.2rem;
    color: #5bb6ff;
    letter-spacing: 1.5px;
    font-weight: 700;
    text-shadow: 0 0 14px rgba(47,158,255,0.55);
  }

  .header-right { display: flex; align-items: center; gap: 0.5rem; }

  .cost-badge {
    background: #163050;
    padding: 0.25rem 0.5rem;
    border-radius: 4px;
    font-size: 0.8rem;
    color: #3ad8ff;
  }

  .icon-btn {
    background: none;
    border: 1px solid #163050;
    color: #eee;
    padding: 0.25rem 0.5rem;
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.85rem;
  }

  .icon-btn:hover { background: #163050; }
  .icon-btn-active { background: #163050; color: #3ad8ff !important; }

  .settings {
    padding: 0.75rem 1rem;
    background: #0c121d;
    border-bottom: 1px solid #163050;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }
  .settings-group {
    background: #070b12;
    border-radius: 6px;
    border: 1px solid #15202f;
    padding: 0.6rem 0.75rem;
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
  }
  .settings-group-title {
    font-size: 0.75rem;
    font-weight: bold;
    color: #888;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    margin-bottom: 0.15rem;
  }

  .setting-row { display: flex; align-items: center; gap: 0.5rem; }

  .setting-row label { min-width: 90px; font-size: 0.85rem; text-align: left; }

  .setting-row input, .setting-row select {
    flex: 1;
    padding: 0.4rem;
    background: #070b12;
    border: 1px solid #163050;
    color: #eee;
    -webkit-text-fill-color: #eee;
    border-radius: 4px;
  }

  .switch-label {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex: 1;
    font-size: 0.85rem;
    cursor: pointer;
  }
  .switch-label input {
    flex: none;
    width: auto;
    padding: 0;
    accent-color: #2f9eff;
  }
  .voice-status { flex: 1; font-size: 0.78rem; color: #6cbcff; }
  .voice-status-err { color: #ff5c7a; }

  .setting-row button {
    padding: 0.4rem 0.75rem;
    background: #2f9eff;
    border: none;
    color: white;
    border-radius: 4px;
    cursor: pointer;
  }

  .setting-row button:disabled { opacity: 0.5; cursor: not-allowed; }

  .status-ok { color: #3ad8ff; font-size: 0.85rem; }

  /* MCP Settings */
  .mcp-settings {
    border-top: 1px solid #163050;
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
    background: #163050;
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

  .mcp-srv-dot-on { background: #3ad8ff; }

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
    border: 1px solid #163050;
    color: #3ad8ff;
    border-radius: 3px;
    cursor: pointer;
    font-size: 0.7rem;
    line-height: 1.2;
    flex-shrink: 0;
  }

  .mcp-link-btn:hover { background: #163050; }
  .mcp-auth-btn {
    background: #e9a045;
    color: #000;
    border: none;
    border-radius: 3px;
    padding: 2px 8px;
    font-size: 0.75rem;
    cursor: pointer;
    font-weight: bold;
  }
  .mcp-auth-btn:hover { background: #d4903a; }

  .mcp-remove-btn:hover { color: #2f9eff; border-color: #2f9eff; }

  .qr-dialog { text-align: center; }
  .qr-hint { color: #aaa; font-size: 0.85rem; margin: 0.5rem 0; }
  .qr-loading { color: #888; font-style: italic; }
  .qr-image { max-width: 256px; margin: 0.5rem auto; display: block; border-radius: 8px; background: white; padding: 8px; }

  /* Sessions panel */
  .sessions-panel {
    background: #0c121d;
    border-bottom: 1px solid #163050;
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

  .session-resume:hover { background: #070b12; }

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

  .session-delete:hover { color: #2f9eff; }

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
    background: #0c121d;
    border: 1px solid #163050;
    border-radius: 8px;
    padding: 1.5rem;
    max-width: 500px;
    width: 90%;
  }

  .perm-dialog h3 { margin: 0 0 0.5rem; color: #2f9eff; }

  .perm-tool { font-weight: bold; margin: 0.25rem 0; }

  .perm-command {
    background: #070b12;
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
  .perm-allow { background: #2f9eff; }
  .perm-session { background: #163050; }
  .perm-forever { background: #3ad8ff; color: #000 !important; }

  /* Update dialog */
  .update-dialog { max-width: 550px; }
  .update-dialog h3 { color: #3ad8ff; }
  .update-versions { font-size: 1.1rem; margin: 0.5rem 0; }
  .update-notes {
    background: #070b12;
    padding: 0.75rem;
    border-radius: 4px;
    font-size: 0.85rem;
    max-height: 250px;
    overflow-y: auto;
    margin: 0.75rem 0;
    line-height: 1.4;
  }
  .update-notes :global(h1), .update-notes :global(h2), .update-notes :global(h3) {
    font-size: 1rem;
    margin: 0.5rem 0 0.25rem;
  }
  .update-error { color: #2f9eff; font-size: 0.85rem; margin: 0.5rem 0 0; }

  /* Chat */
  /* ── 3-column workspace ────────────────────────────────────── */
  .chat {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 1rem 1.25rem;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  /* ── Animated orb (fixed ambient centrepiece) ──────────────── */
  .orb-bg {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    z-index: 0;             /* above page background, below main content */
    pointer-events: none;
    width: 300px;
    height: 300px;
    opacity: 0.85;
    transition: opacity 0.8s ease;
  }
  /* intensify while the agent is thinking */
  .orb-bg.active { opacity: 1; }

  /* line-ring orb: stroked SVG only — no fills/blur/shadow, so animating it
     is pure GPU compositing (transform) with zero per-frame repaint. */
  .orb-ring {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
  }
  /* soft ambient glow behind the line-orb (gradient → no blur filter, cheap;
     animates only opacity/transform so it stays GPU-composited) */
  .orb-aura {
    position: absolute;
    inset: -22%;
    border-radius: 50%;
    background: radial-gradient(circle,
      rgba(60,150,255,0.28) 0%, rgba(33,110,210,0.12) 35%, transparent 62%);
    filter: blur(16px);   /* feather the circular edge into the background */
    animation: aura-breathe 5.5s ease-in-out infinite;
  }
  .orb-bg.active .orb-aura {
    background: radial-gradient(circle,
      rgba(90,180,255,0.42) 0%, rgba(45,130,235,0.2) 38%, transparent 64%);
  }
  /* smaller, lighter inner core glowing in the orb's hollow centre */
  .orb-core {
    position: absolute;
    inset: 26%;
    border-radius: 50%;
    background: radial-gradient(circle,
      rgba(200,230,255,0.5) 0%, rgba(110,190,255,0.22) 42%, transparent 70%);
    filter: blur(12px);
    animation: aura-breathe 4s ease-in-out infinite;
  }
  .orb-bg.active .orb-core {
    background: radial-gradient(circle,
      rgba(225,242,255,0.68) 0%, rgba(130,200,255,0.36) 44%, transparent 72%);
  }
  /* opacity-only breathing → the blurred layers stay cached (no repaint) */
  @keyframes aura-breathe {
    0%, 100% { opacity: 0.6; }
    50%      { opacity: 1; }
  }

  .orb-ring .glow { stroke-width: 5; stroke-opacity: 0.16; stroke-linejoin: round; }
  .orb-ring .line { stroke-width: 1.3; stroke-linejoin: round; }

  @media (prefers-reduced-motion: reduce) {
    .orb-ring { animation: none; }
  }

  .message {
    padding: 0.5rem 0.75rem;
    border-radius: 8px;
    font-size: 13px;
    line-height: 1.55;
    word-break: break-word;
    text-align: left;
    /* wide panels: span almost the full width, leaving a gap on the far side */
    max-width: calc(100% - 4rem);
  }

  /* user questions hug the right edge — line + transparent fill */
  .message.user {
    align-self: flex-end;
    background: transparent;
    border: 1px solid rgba(47,158,255,0.35);
    box-shadow: none;
    white-space: pre-wrap;
    position: relative;
  }
  .edit-btn {
    position: absolute;
    left: -1.6rem;
    top: 50%;
    transform: translateY(-50%);
    opacity: 0;
    background: none;
    border: none;
    color: #6f86a3;
    cursor: pointer;
    padding: 0.2rem;
    display: flex;
    transition: opacity 0.15s, color 0.15s;
  }
  .message.user:hover .edit-btn { opacity: 1; }
  .edit-btn:hover { color: #3ad8ff; }
  .edit-btn:disabled { opacity: 0; cursor: default; }
  .edit-area {
    width: 16rem;
    max-width: 100%;
    background: rgba(7,11,18,0.85);
    border: 1px solid #2f9eff;
    color: #dce6f2;
    border-radius: 10px;
    padding: 0.5rem;
    font-family: inherit;
    font-size: 13px;
    resize: vertical;
    box-sizing: border-box;
  }
  .edit-actions { display: flex; gap: 0.4rem; justify-content: flex-end; margin-top: 0.4rem; }
  .edit-save, .edit-cancel {
    font-size: 12px;
    padding: 0.25rem 0.6rem;
    border-radius: 8px;
    cursor: pointer;
    border: none;
    font-family: inherit;
  }
  .edit-save { background: linear-gradient(135deg, #3aa6ff, #1f7ce0); color: #fff; }
  .edit-save:disabled { opacity: 0.5; cursor: not-allowed; }
  .edit-cancel { background: #11243c; color: #9fc6ef; border: 1px solid #1c3a5e; }

  /* OpenUAI answers / activity hug the left edge — line + transparent fill */
  .message.assistant {
    align-self: flex-start;
    background: transparent;
    border: 1px solid #163050;
  }

  /* Collapsible tool group */
  .tool-group {
    align-self: flex-start;
    max-width: calc(50% - 200px);
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
    color: #3ad8ff;
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

  .tool-step-error { color: #2f9eff; }

  .tool-step-icon {
    font-size: 9px;
    color: #3ad8ff;
    flex-shrink: 0;
  }

  .tool-step-error .tool-step-icon { color: #2f9eff; }

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
    align-self: flex-start;
    background: #2d1515;
    border: 1px solid #5c2020;
    color: #ff5c7a;
  }

  .input-area {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.75rem 1rem;
    background: rgba(9,14,23,0.9);
    border-top: 1px solid #163050;
  }

  textarea {
    flex: 1;
    padding: 0.65rem 0.85rem;
    background: rgba(7,11,18,0.8);
    border: 1px solid #1c3a5e;
    color: #dce6f2;
    border-radius: 14px;
    resize: none;
    font-family: inherit;
    font-size: 0.9rem;
    transition: border-color 0.2s, box-shadow 0.2s;
  }

  textarea:focus {
    outline: none;
    border-color: #2f9eff;
    box-shadow: 0 0 0 1px rgba(47,158,255,0.4), 0 0 18px rgba(47,158,255,0.25);
  }

  textarea:disabled { opacity: 0.5; }

  .input-area button {
    padding: 0.5rem 1.1rem;
    background: linear-gradient(135deg, #3aa6ff, #1f7ce0);
    border: none;
    color: white;
    border-radius: 12px;
    cursor: pointer;
    font-size: 0.9rem;
    font-weight: 600;
    box-shadow: 0 0 16px rgba(47,158,255,0.4);
    transition: box-shadow 0.2s, filter 0.2s;
  }

  .input-area button:hover:not(:disabled) {
    box-shadow: 0 0 24px rgba(47,158,255,0.6);
    filter: brightness(1.08);
  }

  .input-area button:disabled { opacity: 0.5; cursor: not-allowed; box-shadow: none; }

  .clear-btn { background: #11243c !important; box-shadow: none !important; }
  .stop-btn { background: linear-gradient(135deg, #ff4d6d, #c0392b) !important; box-shadow: 0 0 16px rgba(255,77,109,0.45) !important; }
  .stop-btn:hover:not(:disabled) { filter: brightness(1.1); }

  /* Beta features */
  .beta-section {
    margin: 0.75rem 0;
    padding: 0.75rem;
    background: #070b12;
    border-radius: 6px;
    border: 1px solid #15202f;
  }
  .beta-header {
    font-size: 0.75rem;
    font-weight: bold;
    color: #888;
    margin-bottom: 0.5rem;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    text-align: left;
  }
  .beta-toggle {
    display: flex;
    align-items: flex-start;
    gap: 0.5rem;
    cursor: pointer;
    flex-wrap: wrap;
  }
  .beta-toggle input { margin-top: 3px; }
  .beta-toggle span { font-size: 0.9rem; }
  .beta-badge {
    background: #e9a045;
    color: #000;
    font-size: 0.65rem;
    font-weight: bold;
    padding: 1px 5px;
    border-radius: 3px;
    vertical-align: middle;
  }
  .beta-desc {
    display: block;
    width: 100%;
    font-size: 0.75rem;
    color: #666;
    margin-left: 1.2rem;
  }

  /* Lip reading */
  .lip-btn {
    padding: 0.5rem;
    background: #3d0f60;
    border: none;
    color: #eee;
    border-radius: 4px;
    cursor: pointer;
    font-size: 1.1rem;
    min-width: 38px;
    transition: background 0.2s;
  }
  .lip-btn:hover { background: #5a1a8a; }
  .lip-recording {
    background: #ff4d6d !important;
    animation: pulse-red 1s infinite;
  }
  .lip-transcribing {
    background: #e9a045 !important;
    cursor: wait;
  }
  .lip-progress { font-size: 0.9rem; margin: 0.5rem 0; }
  .lip-progress-bar {
    width: 100%;
    height: 6px;
    background: #070b12;
    border-radius: 3px;
    overflow: hidden;
    margin: 0.5rem 0;
  }
  .lip-progress-fill {
    height: 100%;
    background: #3ad8ff;
    transition: width 0.3s;
  }

  /* Voice */
  /* Hands-free wake-word listen toggle (slider) */
  .wake-toggle {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    cursor: pointer;
    color: #6f8aa8;
    user-select: none;
    transition: color 0.2s;
  }
  .wake-toggle input { display: none; }
  .wake-track {
    position: relative;
    width: 30px;
    height: 16px;
    background: #1c3a5e;
    border-radius: 999px;
    transition: background 0.2s;
  }
  .wake-knob {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 12px;
    height: 12px;
    background: #9fc6ef;
    border-radius: 50%;
    transition: left 0.2s, background 0.2s;
  }
  .wake-on { color: #3ad8ff; }
  .wake-on .wake-track { background: #1d5bb0; box-shadow: 0 0 10px rgba(47,158,255,0.5); }
  .wake-on .wake-knob { left: 16px; background: #eaf6ff; }

  .wake-input {
    flex: 1;
    padding: 0.4rem 0.6rem;
    background: #0d1b2e;
    border: 1px solid #1c3a5e;
    border-radius: 6px;
    color: #e6f0fa;
    font-size: 13px;
  }

  .mic-btn {
    padding: 0.5rem;
    background: #11243c;
    border: 1px solid #1c3a5e;
    color: #9fc6ef;
    border-radius: 50%;
    cursor: pointer;
    font-size: 1.1rem;
    min-width: 40px;
    height: 40px;
    display: flex;
    align-items: center;
    justify-content: center;
    transition: background 0.2s, box-shadow 0.2s;
  }
  .mic-btn:hover { background: #1d5bb0; box-shadow: 0 0 14px rgba(47,158,255,0.4); }
  .mic-recording {
    background: #ff4d6d !important;
    color: #fff;
    box-shadow: 0 0 16px rgba(255,77,109,0.55);
    animation: pulse-red 1s infinite;
  }
  .mic-transcribing {
    background: #e9a045 !important;
    cursor: wait;
  }
  /* Conversation window open: blink the mic so it's clear it keeps listening. */
  .mic-session {
    background: #1d5bb0 !important;
    color: #eaf6ff;
    animation: mic-blink 1.1s ease-in-out infinite;
  }
  @keyframes mic-blink {
    0%, 100% { box-shadow: 0 0 4px rgba(58,216,255,0.35); opacity: 0.65; }
    50% { box-shadow: 0 0 16px rgba(58,216,255,0.85); opacity: 1; }
  }
  @keyframes pulse-red {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.6; }
  }

  .voice-meter {
    width: 60px;
    height: 28px;
    background: #0a0f18;
    border-radius: 4px;
    overflow: hidden;
    display: flex;
    align-items: flex-end;
    border: 1px solid #163050;
  }
  .voice-meter-bar {
    height: 100%;
    background: linear-gradient(to right, #3ad8ff, #e9a045, #2f9eff);
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
  .wake-listening-ind { color: #3ad8ff; }
  /* Placeholder bubble shown while a captured utterance is being transcribed. */
  .message.user.wake-pending { opacity: 0.6; }
  .message.user.wake-pending .message-content {
    color: #3ad8ff;
    letter-spacing: 2px;
    animation: pulse-transcribing 1s ease-in-out infinite;
  }
  /* Transient bubble: what was heard when no wake word was detected. */
  .message.user.wake-heard {
    opacity: 0.45;
    font-style: italic;
    animation: wake-heard-fade 3.5s ease forwards;
  }
  @keyframes wake-heard-fade {
    0% { opacity: 0; }
    12% { opacity: 0.5; }
    75% { opacity: 0.45; }
    100% { opacity: 0; }
  }
  .stt-error {
    color: #ff5c7a;
    animation: none;
    white-space: normal;
    max-width: 260px;
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
  .speak-btn:hover { color: #3ad8ff; background: rgba(255,255,255,0.05); }
  .speak-btn:disabled { cursor: wait; opacity: 0.5 !important; }

  /* Events panel */
  .events-panel {
    background: #0c121d;
    border-bottom: 1px solid #163050;
    max-height: 250px;
    display: flex;
    flex-direction: column;
  }

  .events-toolbar {
    display: flex;
    gap: 0.5rem;
    padding: 0.5rem 1rem;
    border-bottom: 1px solid #163050;
  }

  .events-toolbar button {
    padding: 0.3rem 0.6rem;
    border: none;
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.8rem;
    color: white;
  }

  .events-refresh-btn { background: #163050; }
  .events-clear-btn { background: #333; }

  .events-stats {
    display: flex;
    gap: 1rem;
    padding: 0.35rem 1rem;
    font-size: 0.75rem;
    color: #888;
    border-bottom: 1px solid #163050;
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

  .event-entry:hover { background: #070b12; }

  .event-source {
    background: #163050;
    padding: 0.1rem 0.4rem;
    border-radius: 3px;
    font-weight: 500;
    color: #3ad8ff;
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
  .event-msg-dir.in  { color: #3ad8ff; }

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
    border-bottom: 1px solid #163050;
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
    color: #2f9eff;
    font-size: 0.7rem;
  }

  .mcp-connected {
    color: #3ad8ff;
  }

  .mcp-server-info {
    color: #666;
    font-size: 0.7rem;
  }

  /* Marketplace */
  .marketplace-section {
    margin-top: 0.5rem;
  }
  .mp-banner {
    background: #070b12;
    border: 1px solid #2f9eff;
    border-radius: 6px;
    padding: 0.5rem 0.75rem;
    font-size: 0.75rem;
    color: #2f9eff;
    margin-bottom: 0.5rem;
  }
  .mp-banner a { color: #3ad8ff; }
  .mp-filters {
    display: flex;
    gap: 0.25rem;
    margin-bottom: 0.5rem;
    flex-wrap: wrap;
  }
  .mp-filter-btn {
    background: #0c121d;
    border: 1px solid #163050;
    color: #888;
    padding: 0.2rem 0.6rem;
    border-radius: 4px;
    font-size: 0.7rem;
    cursor: pointer;
  }
  .mp-filter-btn:hover { border-color: #2f9eff; color: #ccc; }
  .mp-filter-active { background: #163050; color: #fff; border-color: #2f9eff; }
  .mp-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.5rem;
  }
  .mp-card {
    background: #0c121d;
    border: 1px solid #163050;
    border-radius: 8px;
    padding: 0.6rem;
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
  }
  .mp-card-installed { opacity: 0.6; }
  .mp-card-header {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }
  .mp-card-icon {
    font-size: 0.7rem;
    font-weight: 700;
    color: #2f9eff;
    background: #070b12;
    padding: 0.15rem 0.3rem;
    border-radius: 4px;
    font-family: monospace;
  }
  .mp-card-name {
    font-size: 0.8rem;
    font-weight: 600;
    color: #eee;
  }
  .mp-card-cat {
    margin-left: auto;
    font-size: 0.6rem;
    color: #666;
    background: #0a0f18;
    padding: 0.1rem 0.4rem;
    border-radius: 3px;
  }
  .mp-card-desc {
    font-size: 0.7rem;
    color: #999;
  }
  .mp-card-auth input {
    width: 100%;
    background: #0a0f18;
    border: 1px solid #163050;
    color: #eee;
    padding: 0.25rem 0.4rem;
    border-radius: 4px;
    font-size: 0.7rem;
    box-sizing: border-box;
  }
  .mp-card-status {
    font-size: 0.7rem;
    color: #3ad8ff;
    font-weight: 500;
  }
  .mp-install-btn {
    background: #2f9eff;
    border: none;
    color: #fff;
    padding: 0.3rem 0.6rem;
    border-radius: 4px;
    font-size: 0.7rem;
    cursor: pointer;
    align-self: flex-start;
  }
  .mp-install-btn:hover { background: #c73652; }
  .mp-install-btn:disabled { background: #333; color: #666; cursor: not-allowed; }

  /* Markdown rendered content */
  .markdown :global(p) { margin: 0.25rem 0; }
  .markdown :global(p:first-child) { margin-top: 0; }
  .markdown :global(p:last-child) { margin-bottom: 0; }
  .markdown :global(ul), .markdown :global(ol) { margin: 0.25rem 0; padding-left: 1.4rem; }
  .markdown :global(li) { margin: 0.1rem 0; }
  .markdown :global(h1), .markdown :global(h2), .markdown :global(h3) {
    margin: 0.4rem 0 0.2rem;
    font-size: 14px;
    color: #2f9eff;
  }
  .markdown :global(h1) { font-size: 15px; }
  .markdown :global(code) {
    background: #0a0f18;
    padding: 0.1rem 0.3rem;
    border-radius: 3px;
    font-size: 12px;
  }
  .markdown :global(pre) {
    background: #0a0f18;
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
  /* fenced code blocks rendered as a styled card */
  .markdown :global(.code-block) {
    margin: 0.5rem 0;
    border: 1px solid #1c3a5e;
    border-radius: 8px;
    overflow: hidden;
    background: #0d1117;
  }
  .markdown :global(.code-head) {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.25rem 0.3rem 0.25rem 0.6rem;
    background: #0a0f18;
    border-bottom: 1px solid #15233a;
  }
  .markdown :global(.code-lang) {
    font-size: 10px;
    letter-spacing: 0.6px;
    text-transform: uppercase;
    color: #6f86a3;
  }
  .markdown :global(.code-copy) {
    background: #11243c;
    border: 1px solid #1c3a5e;
    color: #9fc6ef;
    font-size: 10px;
    padding: 0.12rem 0.5rem;
    border-radius: 5px;
    cursor: pointer;
    font-family: inherit;
  }
  .markdown :global(.code-copy:hover) { background: #1d5bb0; color: #fff; }
  .markdown :global(.code-block pre) {
    margin: 0;
    border-radius: 0;
    padding: 0.6rem 0.75rem;
    background: transparent;
    font-size: 12px;
  }
  .markdown :global(strong) { color: #fff; }
  .markdown :global(a) { color: #3ad8ff; }
  .markdown :global(code.file-link) {
    color: #3ad8ff;
    cursor: pointer;
    text-decoration: underline dotted;
    text-underline-offset: 2px;
  }
  .markdown :global(code.file-link:hover) {
    background: #1d5bb0;
    color: #eaf6ff;
    text-decoration: underline;
  }
  .markdown :global(blockquote) {
    border-left: 3px solid #163050;
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
