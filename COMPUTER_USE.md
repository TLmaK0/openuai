# Computer Use (Cowork-style) — Design

Goal: replicate what Cowork actually does — the agent **controls a real desktop/browser
directly** (screenshot → reason → click/type), instead of launching a separate
Puppeteer browser. This naturally reuses the user's existing logins (the agent drives
the real, already-authenticated browser) and fills forms reliably, with no profile
locks, no `about:blank`, no fabricated data.

Per Anthropic's Cowork docs: *"Claude uses computer use to interact directly with your
screen: clicking, typing, and navigating your desktop apps … no sandbox between Claude
and your applications."*

## Model-agnostic (key design choice)

We do NOT use a provider-specific computer-use tool (Claude's `computer_20250124` or
OpenAI's `computer-use-preview`). Instead the agent gets **generic vision+tool-calling**
tools (`take_screenshot`, `click`, `double_click`, `type_text`, `press_key`, `scroll`)
and we feed screenshots back as real image inputs. ANY vision+tool model works —
including the user's current **gpt-5.4 via Codex OAuth** (no new API key needed).

Tradeoff: dedicated computer-use models are more pixel-precise; a general model is less
precise on tiny targets but self-strategizes (see validation below).

## Mechanism (validated prototype: `/tmp/cu/claude_cu.py`)

The standard Anthropic computer-use agent loop:

```
task ─► Claude (Messages API, tool: computer_20250124, beta: computer-use-2025-01-24)
          │  returns tool_use { action: left_click | type | key | scroll | screenshot … }
          ▼
     execute action on the target X display (xdotool)
          ▼
     capture screenshot (import -window root) ─► tool_result { image }
          ▼
     loop until Claude stops emitting actions
```

### Display strategy
- **Isolated dev/headless run:** `Xvfb :99` — apps run on a virtual screen, invisible to
  the user, perfect for autonomous tasks and CI. (Used to build/validate this.)
- **"Watch me" run:** target the user's real display `:0` so the user sees the work, OR
  an `Xvfb` + VNC/stream surfaced in the OpenUAI UI so the user watches inside the app.
- The agent reuses the user's real **logged-in browser** because it drives the actual
  browser window — credentials are simply whatever is already signed in. No profile copy.

### Action mapping (xdotool)
| Claude action | xdotool |
|---|---|
| screenshot | `import -window root png:-` |
| left/right/middle/double/triple_click | `xdotool mousemove --sync X Y click [--repeat N] BTN` |
| type | `xdotool type --delay 40 TEXT` |
| key | `xdotool key KEYS` |
| mouse_move | `xdotool mousemove --sync X Y` |
| scroll | `xdotool click 4/5/6/7` ×N at coordinate |
| wait | sleep |

## Validation status — WORKING end-to-end, no new API key
- ✅ Xvfb `:99` (1280×800) virtual display, isolated from the user's `:0`
- ✅ Screenshot via `import` → viewable PNG; actions via `xdotool`
- ✅ Drove **xcalc**: `6 × 7 =` → **42** (manual harness check)
- ✅ Drove **Chrome**: navigated example.com → clicked "Learn more" → iana.org
- ✅ **gpt-5.4 via Codex can SEE images** (read "42" off a calculator screenshot;
  input_tokens jumped to 1231, confirming the image was ingested)
- ✅ **gpt-5.4 drove the screen AUTONOMOUSLY** (`/tmp/cu/cu_codex.py`): asked to compute
  8×9, it found xcalc too small, switched strategy on its own, opened Google's calculator
  in Chrome, clicked `8 × 9 =` → **72** (verified by screenshot). Full loop: screenshot →
  model tool_calls → xdotool → screenshot back → model stops with correct result.

Prototypes: `/tmp/cu/cu_codex.py` (gpt-5.4/any model, **validated**),
`/tmp/cu/claude_cu.py` (Claude-specific computer_20250124 variant, optional).

## INTEGRATED into OpenUAI (done 2026-06-04)
- `internal/computeruse/`: `Controller` (display + `import` screenshot + `xdotool` actions)
  and generic tools `computer_screenshot` / `_click` / `_double_click` / `_right_click` /
  `_type` / `_key` / `_scroll`. Linux/X11 only (errors gracefully elsewhere).
- `internal/llm/openai.go`: `codexInputMessage.Content` is now `interface{}`; messages/tool
  results with images are sent as `input_text`+`input_image` parts. `llm.Message.Images`
  and `tools.Result.Images` added; agent attaches screenshots to tool-result messages and
  drops old ones in MicroCompact (token control).
- `app.go`: gated by config `computer_use_enabled` + `computer_use_display`; Wails methods
  `Get/SetComputerUseEnabled`, `Get/SetComputerUseDisplay`, `CheckComputerUseDeps`.
- `computer_screenshot` is permission-free (read-only); actions use "session" permission.

**End-to-end validation (gpt-5.4 via the running app's REST API, display :99):**
- ✅ Vision: agent called `computer_screenshot`, gpt-5.4 read the screen and accurately
  described it (Chrome, Google calculator showing "8 × 9 = 72", the language popup, the
  sandbox banner). The image reached the model (input_tokens ~16k).
- ✅ Control: agent clicked buttons and computed via the calculator, then honestly reported
  the on-screen result (no fabrication).
- ⚠️ **Precision caveat:** gpt-5.4 (a general model) is not pixel-perfect on small targets —
  in one run it hit "1" instead of "4" and got 11 instead of 9, but reported "11" truthfully.
  Dedicated computer-use models, larger/zoomed targets, or accessibility-based element
  targeting would improve click accuracy. The mechanism itself is solid.

## Possible follow-ups
1. Improve click precision: optional accessibility/DOM-assisted targeting, set-of-marks
   overlay, or a dedicated computer-use model when available.
2. UI: live screenshot stream / VNC so the user can watch; display picker (:0 vs virtual).
3. Xvfb auto-spawn for headless/autonomous runs; per-action confirm for destructive clicks.

## (Earlier) provider note
The one core change was image input: `codexInputMessage.Content`
   in `internal/llm/openai.go` is a plain string today. Make message/tool-result content
   support an array of parts (`input_text` + `input_image`) so screenshots reach the model.
   (Claude provider would get the analogous image-block support.)
2. **`internal/computeruse/`**: display manager (Xvfb spawn for headless/autonomous, or
   attach to `:0` for "watch me"), screenshot (`import`), xdotool action executor (reuse
   the platform `proc_*.go` pattern). Generic tools: screenshot/click/double_click/
   type_text/press_key/scroll — registered like any other tool, model-agnostic.
3. **UI**: a live view (screenshot stream / VNC) so the user can watch, plus Stop (reuse
   AbortAgent).
4. **Safety**: confirm destructive clicks; never fabricate form data (already in prompt).

## Why this beats the Puppeteer path for the user's ERP case
- Uses the **already-authenticated** browser session — no login juggling, no profile lock.
- No MCP-server quirks (the relaunch-to-`about:blank` bug, stray launchOptions).
- Works on **any** app, not just web (the user saw Cowork fill forms — this is how).
