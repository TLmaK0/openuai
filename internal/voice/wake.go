package voice

import (
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"openuai/internal/logger"
)

// VAD / capture tuning. Levels are 0-100 from calculateLevel (RMS → dB scale).
const (
	wakeSpeechOn    = 28                      // level above which we consider speech present
	wakeSilenceEnd  = 1300 * time.Millisecond // trailing silence that ends an utterance (tolerates a brief pause after the name)
	wakeNoSpeech    = 6 * time.Second         // give up waiting for speech to start
	wakeCmdNoSpeech = 4 * time.Second         // shorter wait when already listening for the command after the wake word
	wakeMaxUtter    = 18 * time.Second        // hard cap on a single utterance
	wakePoll        = 100 * time.Millisecond  // level sampling interval
	wakeSessionWin  = 60 * time.Second        // conversation window after the wake word: keep capturing without it
)

// WakeListener runs a continuous listen loop: it captures each spoken utterance
// via simple voice-activity detection, transcribes it, and when the transcript
// begins with the configured wake word it hands the full transcript (name
// included) to onMessage. After a wake-word hit it opens a conversation window
// (wakeSessionWin) during which every utterance is forwarded without needing the
// word again; the window refreshes on each exchange and after the assistant
// finishes replying, and closes once it goes idle — then the word is required
// again. It is paused while the agent is busy or TTS is playing to avoid
// transcribing the assistant's own voice.
type WakeListener struct {
	WakeWord    func() string // current wake word (e.g. "Pepito")
	Device      func() string // mic device name (see ListDevices), "" = default
	Transcribe  func(wav []byte) (string, error)
	OnMessage   func(text string) // called with the full transcript (wake word kept)
	OnListening func()            // called when the wake word fired and we're awaiting the command
	OnCapture   func()            // called the instant speech begins (immediate UI feedback), before the utterance completes
	OnDiscard   func(heard string) // called when a captured utterance is dropped; heard is the transcript (or "" on STT error)
	OnSession   func(active bool)  // called when the conversation window opens/closes (UI hint: blink the mic)

	running int32 // atomic: 1 while the loop goroutine is active
	paused  int32 // atomic: 1 while listening should be suspended
	cancel  context.CancelFunc
	done    chan struct{} // closed when the loop goroutine fully exits
	mu      sync.Mutex
}

// SetPaused suspends/resumes listening (used while the agent runs or TTS plays).
func (w *WakeListener) SetPaused(p bool) {
	if p {
		atomic.StoreInt32(&w.paused, 1)
	} else {
		atomic.StoreInt32(&w.paused, 0)
	}
}

// Running reports whether the listen loop is active.
func (w *WakeListener) Running() bool { return atomic.LoadInt32(&w.running) == 1 }

// discard notifies the UI that a captured utterance was dropped, passing what
// was heard (so the UI can briefly show it instead of silently dropping the
// "[...]" placeholder — making it visible that transcription did happen).
func (w *WakeListener) discard(heard string) {
	if w.OnDiscard != nil {
		w.OnDiscard(heard)
	}
}

// Start launches the listen loop. No-op if already running. Holds the lock for
// the whole call so it can never race with a concurrent Stop and spawn an
// overlapping goroutine (which would open a second capture device on the mic).
func (w *WakeListener) Start() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if atomic.LoadInt32(&w.running) == 1 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan struct{})
	atomic.StoreInt32(&w.running, 1)
	atomic.StoreInt32(&w.paused, 0)
	go w.loop(ctx, w.done)
	logger.Info("Wake listener: started")
}

// Stop ends the listen loop and blocks until the goroutine has fully exited
// (releasing the capture device), so a following Start can't overlap with it.
func (w *WakeListener) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if atomic.LoadInt32(&w.running) == 0 {
		return
	}
	atomic.StoreInt32(&w.running, 0)
	if w.cancel != nil {
		w.cancel()
	}
	if w.done != nil {
		<-w.done // the loop never takes w.mu, so this can't deadlock
	}
	logger.Info("Wake listener: stopped")
}

func (w *WakeListener) loop(ctx context.Context, done chan struct{}) {
	// Conversation-session state (loop-local; only this goroutine touches it).
	var sessionUntil time.Time
	sessionActive := false
	wasPaused := false
	// setSession toggles the window and notifies the UI only on a real change,
	// so refreshing the deadline mid-session doesn't spam the event.
	setSession := func(active bool) {
		if active != sessionActive {
			sessionActive = active
			if w.OnSession != nil {
				w.OnSession(active)
			}
		}
	}

	// Clear running here too (not only in Stop) so that if the mic can't be
	// opened the listener resets and a later Start can retry.
	defer func() {
		setSession(false) // ensure the UI stops indicating an open conversation
		atomic.StoreInt32(&w.running, 0)
		close(done)
	}()

	// One capture device for the whole session: opening miniaudio per utterance
	// would add device warm-up latency and miss speech onsets.
	mic, err := OpenMic(w.Device())
	if err != nil {
		logger.Error("Wake listener: cannot open microphone: %s", err.Error())
		return
	}
	defer mic.Close()

	for {
		if ctx.Err() != nil {
			return
		}
		if atomic.LoadInt32(&w.paused) == 1 {
			mic.Reset() // drop audio captured while paused (e.g. the assistant's TTS)
			wasPaused = true
			time.Sleep(200 * time.Millisecond)
			continue
		}
		// Resuming after a pause (the agent replied + TTS played): give the user a
		// fresh window to follow up once the assistant has finished speaking.
		if wasPaused {
			wasPaused = false
			if sessionActive {
				sessionUntil = time.Now().Add(wakeSessionWin)
			}
		}
		// Conversation window went idle → require the wake word again.
		if sessionActive && time.Now().After(sessionUntil) {
			logger.Info("Wake listener: conversation window closed (idle)")
			setSession(false)
		}

		wav, hasSpeech := w.captureUtterance(ctx, mic, wakeNoSpeech)
		if ctx.Err() != nil {
			return
		}
		if !hasSpeech || len(wav) == 0 {
			continue
		}
		// (The "[...]" placeholder was already shown at speech onset inside
		// captureUtterance, so the UI reacted the moment it heard us.)
		transcript, err := w.Transcribe(wav)
		if err != nil || transcript == "" {
			w.discard("")
			continue
		}
		msg, ok := stripWakeWord(transcript, w.WakeWord())
		inSession := sessionActive && time.Now().Before(sessionUntil)

		if inSession {
			// Conversation mode: forward everything without the wake word. Ignore a
			// bare repeat of the name (nothing to act on).
			if ok && msg == "" {
				w.discard(transcript)
				continue
			}
		} else {
			if !ok {
				logger.Debug("Wake listener: heard %q (no wake word)", transcript)
				w.discard(transcript)
				continue
			}
			if msg == "" {
				// Wake word alone (no command in the same phrase). Say the whole thing
				// in one breath: "Pepito, qué hora es".
				logger.Info("Wake listener: wake word only, no command — say it in one phrase")
				w.discard(transcript)
				continue
			}
		}

		// Hand over the full transcript (name included): the wake word is only used
		// to detect activation, not to be removed from what the agent receives.
		if inSession {
			logger.Info("Wake listener: follow-up → %q", transcript)
		} else {
			logger.Info("Wake listener: triggered → %q", transcript)
		}
		if w.OnMessage != nil {
			w.OnMessage(transcript)
		}
		// (Re)open the conversation window so the next utterance needs no wake word.
		sessionUntil = time.Now().Add(wakeSessionWin)
		setSession(true)
		// Pause briefly so the frontend can mark itself busy (send + TTS) before
		// we resume capturing — prevents picking up the assistant's own reply.
		time.Sleep(1200 * time.Millisecond)
	}
}

// captureUtterance records one utterance from the shared mic using voice-
// activity detection: it waits for speech to start, then for a trailing pause,
// and returns the captured WAV plus whether any speech was detected. It calls
// OnCapture the instant speech begins (immediate UI feedback) and, if it bails
// out mid-capture, clears that placeholder via discard. The mic buffer is reset
// on entry so each utterance is transcribed in isolation.
func (w *WakeListener) captureUtterance(ctx context.Context, mic *Mic, noSpeechTimeout time.Duration) ([]byte, bool) {
	mic.Reset()

	ticker := time.NewTicker(wakePoll)
	defer ticker.Stop()

	start := time.Now()
	speechStarted := false
	lastSpeech := time.Now()

	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-ticker.C:
		}

		// Bail out immediately if we've been paused mid-capture (e.g. the user
		// pressed push-to-talk, or the agent started replying).
		if atomic.LoadInt32(&w.paused) == 1 {
			if speechStarted {
				w.discard("") // clear the "[...]" placeholder we already showed
			}
			return nil, false
		}

		now := time.Now()
		if mic.Level() >= wakeSpeechOn {
			if !speechStarted && w.OnCapture != nil {
				// Show the "[...]" placeholder the instant we hear speech, not after
				// the whole utterance + trailing silence — otherwise it feels laggy.
				w.OnCapture()
			}
			speechStarted = true
			lastSpeech = now
		}

		switch {
		case speechStarted && now.Sub(lastSpeech) >= wakeSilenceEnd:
			return mic.WAV(), true
		case !speechStarted && now.Sub(start) >= noSpeechTimeout:
			return nil, false
		case now.Sub(start) >= wakeMaxUtter:
			return mic.WAV(), speechStarted
		}
	}
}

// TranscribeWAV is a convenience that base64-encodes a WAV and reuses the STT
// path. prompt biases recognition toward the wake word.
func TranscribeWAV(wav []byte, model, language, prompt, configDir string) TranscribeResult {
	return Transcribe(base64.StdEncoding.EncodeToString(wav), model, language, prompt, configDir)
}

// wakeMaxLeadTokens is how far into the transcript the wake word may appear
// (lets natural fillers like "Oye"/"Hola" precede it).
const wakeMaxLeadTokens = 2

type wakeTok struct {
	byteEnd int    // byte index in the original string just past this token
	folded  string // lowercased, accent-stripped word
}

// stripWakeWord checks whether the transcript opens with the wake word and, if
// so, returns the rest of the message (wake word + leading fillers removed).
// Matching is fuzzy (accent/case-insensitive + small edit distance) because
// Whisper often mis-hears a short isolated name ("Pepito" → "Papito") and may
// emit it more than once or wrap it in markdown ("*Papito*"). It also tolerates
// a filler word before the name and consumes repeated wake words.
func stripWakeWord(transcript, wake string) (string, bool) {
	wakeF := alnumFold(wake) // e.g. "Pepito" -> "pepito"
	if wakeF == "" {
		return "", false
	}
	toks := tokenizeFolded(transcript)
	if len(toks) == 0 {
		return "", false
	}
	// Find the wake word within the first few tokens.
	matchIdx := -1
	for i := 0; i < len(toks) && i <= wakeMaxLeadTokens-1; i++ {
		if wakeMatches(toks[i].folded, wakeF) {
			matchIdx = i
			break
		}
	}
	if matchIdx < 0 {
		return "", false
	}
	// Consume any immediately-following repeats of the wake word.
	last := matchIdx
	for last+1 < len(toks) && wakeMatches(toks[last+1].folded, wakeF) {
		last++
	}
	rest := transcript[toks[last].byteEnd:]
	rest = strings.TrimLeft(rest, " ,.:;!?¡¿-—'\"*\t\n")
	return strings.TrimSpace(rest), true
}

// tokenizeFolded splits a string into alnum word tokens, recording where each
// ends (byte offset into the original) and its folded form.
func tokenizeFolded(s string) []wakeTok {
	var toks []wakeTok
	var cur strings.Builder
	for i, r := range s {
		f := foldRune(r)
		if isAlnum(f) {
			cur.WriteRune(f)
			continue
		}
		if cur.Len() > 0 {
			toks = append(toks, wakeTok{byteEnd: i, folded: cur.String()})
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		toks = append(toks, wakeTok{byteEnd: len(s), folded: cur.String()})
	}
	return toks
}

// wakeMatches reports whether a transcribed word is close enough to the wake
// word. Whisper mangles a short isolated name badly ("Pepito" → "Pepe", "Pepi",
// "Papito"), so besides a length-scaled edit distance we also accept a word that
// shares a solid common prefix with the wake word — the prefix is what survives
// truncation. Both signals are deliberately lenient because in wake mode the
// first word is *meant* to be the name.
func wakeMatches(word, wake string) bool {
	if word == wake {
		return true
	}
	wl := len([]rune(wake))
	thresh := 1
	if wl >= 5 {
		thresh = 2
	}
	if levenshtein(word, wake) <= thresh {
		return true
	}
	// Common-prefix fallback: e.g. "pepe"/"pepi" vs "pepito" share "pep".
	cp := commonPrefixLen(word, wake)
	need := wl / 2
	if need < 3 {
		need = 3
	}
	if cp >= need && len([]rune(word)) >= 3 && levenshtein(word, wake) <= 4 {
		return true
	}
	return false
}

// commonPrefixLen returns the number of leading runes a and b share.
func commonPrefixLen(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	n := 0
	for n < len(ra) && n < len(rb) && ra[n] == rb[n] {
		n++
	}
	return n
}

// levenshtein computes the edit distance between two strings (rune-based).
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// alnumFold returns the lowercased, accent-stripped letters/digits of s
// (punctuation and spaces dropped).
func alnumFold(s string) string {
	var b strings.Builder
	for _, r := range s {
		f := foldRune(r)
		if isAlnum(f) {
			b.WriteRune(f)
		}
	}
	return b.String()
}

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// foldRune lowercases a rune and strips common Latin accents (keeping a 1:1
// rune mapping). Non-letters pass through lowercased.
func foldRune(r rune) rune {
	r = unicode.ToLower(r)
	switch r {
	case 'á', 'à', 'ä', 'â', 'ã', 'å':
		return 'a'
	case 'é', 'è', 'ë', 'ê':
		return 'e'
	case 'í', 'ì', 'ï', 'î':
		return 'i'
	case 'ó', 'ò', 'ö', 'ô', 'õ':
		return 'o'
	case 'ú', 'ù', 'ü', 'û':
		return 'u'
	case 'ñ':
		return 'n'
	case 'ç':
		return 'c'
	case 'ý', 'ÿ':
		return 'y'
	}
	return r
}
