package voice

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDriver is a scripted clock + audioSource that drives captureUtterance one
// tick at a time, fully deterministically. It is both the clock (delivering each
// tick's time on its own channel, which the loop also uses as its `now`) and the
// mic (reporting a scripted level per tick).
//
// The crux is step(): the capture loop reads mic.Level() *after* it receives a
// tick, so naively setting the next tick's level could clobber the value the
// loop is about to read for the current tick. To prevent that, Level() emits an
// ack the moment it is read, and step() blocks on that ack before returning — so
// the level for tick N is provably consumed before the test sets tick N+1's.
// That makes every timing assertion exact, with no sleeping and no races.
//
// (OnLevel is left nil in these tests so Level() is called exactly once per
// iteration; setting it would emit a second Level() read and need a second ack.)
type fakeDriver struct {
	mu       sync.Mutex
	t        time.Time
	tickCh   chan time.Time
	levelAck chan struct{}
	stopped  chan struct{}
	once     sync.Once
	level    int32
	takeN    int32
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{
		t:        time.Unix(0, 0),
		tickCh:   make(chan time.Time),
		levelAck: make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

// clock
func (d *fakeDriver) now() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.t
}
func (d *fakeDriver) newTicker(time.Duration) ticker { return d }
func (d *fakeDriver) C() <-chan time.Time            { return d.tickCh }
func (d *fakeDriver) Stop()                          { d.once.Do(func() { close(d.stopped) }) }

// audioSource
func (d *fakeDriver) Reset() {}
func (d *fakeDriver) Level() int {
	v := int(atomic.LoadInt32(&d.level))
	// Signal that the loop has consumed this tick's level (see step). Bail out if
	// the loop is being torn down so a stray read can't block forever.
	select {
	case d.levelAck <- struct{}{}:
	case <-d.stopped:
	}
	return v
}
func (d *fakeDriver) TakeWAV() []byte { atomic.AddInt32(&d.takeN, 1); return []byte{1, 2, 3} }
func (d *fakeDriver) takes() int      { return int(atomic.LoadInt32(&d.takeN)) }

// step reports `level` for one tick of duration dur and waits until the loop has
// both received the tick and read the level, then commits the advanced time.
// Returns false if the loop has already stopped (so feeding past a close can't
// hang).
func (d *fakeDriver) step(level int, dur time.Duration) bool {
	atomic.StoreInt32(&d.level, int32(level))
	d.mu.Lock()
	next := d.t.Add(dur)
	d.mu.Unlock()
	select {
	case d.tickCh <- next:
	case <-d.stopped:
		return false
	}
	select {
	case <-d.levelAck:
	case <-d.stopped:
		return false
	}
	d.mu.Lock()
	d.t = next
	d.mu.Unlock()
	return true
}

type capResult struct {
	txt string
	ok  bool
}

func runCapture(w *WakeListener, d *fakeDriver, timeout time.Duration) (<-chan capResult, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan capResult, 1)
	go func() {
		txt, ok := w.captureUtterance(ctx, d, timeout, d)
		res <- capResult{txt, ok}
	}()
	return res, cancel
}

func feed(t *testing.T, d *fakeDriver, level, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if !d.step(level, wakePoll) {
			t.Fatalf("capture returned early after %d/%d ticks at level %d", i, n, level)
		}
	}
}

func assertOpen(t *testing.T, res <-chan capResult, after string) {
	t.Helper()
	select {
	case r := <-res:
		t.Fatalf("utterance closed too early (%s): got (%q, %v)", after, r.txt, r.ok)
	default:
	}
}

func waitResult(t *testing.T, res <-chan capResult) capResult {
	t.Helper()
	select {
	case r := <-res:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("captureUtterance did not return")
		return capResult{}
	}
}

func testListener() *WakeListener {
	return &WakeListener{Transcribe: func([]byte) (string, error) { return "hola", nil }}
}

// The three cases below pin the tuned silence windows (silenceEndFor) end to end
// through the real capture loop: speech of a given length, then trailing silence
// one tick short of the window (must stay open) and the tick that reaches it
// (must close). They are the regression guard for the timing values.

func TestCaptureClosesAtNormalWindow(t *testing.T) {
	d := newFakeDriver()
	w := testListener()
	res, cancel := runCapture(w, d, wakeNoSpeech)
	defer cancel()

	feed(t, d, 40, 8) // 800ms speech -> normal command -> wakeSilenceEnd (700ms)
	feed(t, d, 0, 6)  // 600ms silence: still under the window
	assertOpen(t, res, "6 of 7 silence ticks")
	d.step(0, wakePoll) // 700ms of silence -> close

	if r := waitResult(t, res); !r.ok || r.txt != "hola" {
		t.Fatalf("got (%q, %v), want (\"hola\", true)", r.txt, r.ok)
	}
}

func TestCaptureClosesAtNameWindow(t *testing.T) {
	d := newFakeDriver()
	w := testListener()
	res, cancel := runCapture(w, d, wakeNoSpeech)
	defer cancel()

	feed(t, d, 40, 5) // 500ms speech -> "barely any" -> wakeSilenceName (800ms)
	feed(t, d, 0, 7)  // 700ms silence: the old normal window would have closed here
	assertOpen(t, res, "7 of 8 silence ticks (name window must wait longer)")
	d.step(0, wakePoll) // 800ms of silence -> close

	if r := waitResult(t, res); !r.ok || r.txt != "hola" {
		t.Fatalf("got (%q, %v), want (\"hola\", true)", r.txt, r.ok)
	}
}

func TestCaptureClosesAtDictationWindow(t *testing.T) {
	d := newFakeDriver()
	w := testListener()
	res, cancel := runCapture(w, d, wakeNoSpeech)
	defer cancel()

	feed(t, d, 40, 80) // 8s speech -> dictation -> wakeSilenceLong (2s)
	feed(t, d, 0, 19)  // 1.9s silence: still under the patient window
	assertOpen(t, res, "19 of 20 silence ticks")
	d.step(0, wakePoll) // 2s of silence -> close

	if r := waitResult(t, res); !r.ok {
		t.Fatalf("got ok=%v, want true", r.ok)
	}
}

func TestCaptureFiresOnCaptureAtSpeechOnset(t *testing.T) {
	d := newFakeDriver()
	var captured int32
	w := testListener()
	w.OnCapture = func() { atomic.AddInt32(&captured, 1) }
	res, cancel := runCapture(w, d, wakeNoSpeech)
	defer cancel()

	feed(t, d, 10, 3) // below wakeSpeechOn (28): not speech (can never increment)
	if got := atomic.LoadInt32(&captured); got != 0 {
		t.Fatalf("OnCapture fired %d times during sub-threshold audio, want 0", got)
	}
	// Onset plus sustained speech. The onset tick fires OnCapture; the later ticks
	// flush its processing (so the read isn't racing the loop) and must not
	// re-fire it, since speechStarted is now set.
	feed(t, d, 40, 5)
	if got := atomic.LoadInt32(&captured); got != 1 {
		t.Fatalf("OnCapture fired %d times across speech onset, want exactly 1", got)
	}
	_ = res
}

func TestCaptureGivesUpWhenNoSpeech(t *testing.T) {
	d := newFakeDriver()
	w := testListener()
	res, cancel := runCapture(w, d, 350*time.Millisecond)
	defer cancel()

	feed(t, d, 0, 3) // 300ms of silence: under the no-speech timeout
	assertOpen(t, res, "before the no-speech timeout")
	d.step(0, wakePoll) // 400ms >= 350ms with no speech -> give up

	if r := waitResult(t, res); r.ok || r.txt != "" {
		t.Fatalf("got (%q, %v), want (\"\", false)", r.txt, r.ok)
	}
}

// TestCaptureChunksAndJoins exercises the long-utterance path: a phrase, a dip
// long enough to cut a chunk, then more speech. The two chunks are transcribed
// separately and joined, and the mid-utterance cut shows up as an extra TakeWAV.
func TestCaptureChunksAndJoins(t *testing.T) {
	d := newFakeDriver()
	w := testListener()
	res, cancel := runCapture(w, d, wakeNoSpeech)
	defer cancel()

	feed(t, d, 40, 20) // 2s speech (>= wakeChunkMin)
	feed(t, d, 0, 4)   // 400ms dip (>= wakeChunkDip) -> chunk cut
	feed(t, d, 40, 5)  // more speech -> second chunk
	feed(t, d, 0, 6)   // 600ms: under the 700ms close window
	assertOpen(t, res, "before the trailing window after resumed speech")
	d.step(0, wakePoll) // 700ms silence -> close

	r := waitResult(t, res)
	if !r.ok || r.txt != "hola hola" {
		t.Fatalf("got (%q, %v), want (\"hola hola\", true)", r.txt, r.ok)
	}
	if d.takes() < 2 {
		t.Fatalf("TakeWAV called %d times; expected a mid-utterance chunk cut plus the tail", d.takes())
	}
}
