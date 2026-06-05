// Package computeruse drives a real X display (the user's screen or a virtual
// Xvfb) by taking screenshots and injecting input — the model-agnostic
// "computer use" mechanism (screenshot -> model decides -> click/type).
//
// Linux/X11 only: uses `import` (ImageMagick) for screenshots and `xdotool`
// for input. On other platforms the methods return an error.
package computeruse

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Controller drives a single X display, optionally scoped to one monitor.
//
// Scoping to a single monitor matters: general vision models locate pixels
// accurately on a normal ~16:9 monitor but poorly on an ultra-wide multi-monitor
// composite. The Controller crops screenshots to the chosen monitor and offsets
// click/scroll coordinates by that monitor's origin, so the model only ever
// reasons about one screen's coordinate space.
type Controller struct {
	Display string // e.g. ":0" (the user's screen) or ":99" (virtual Xvfb)
	Monitor int    // monitor index from `xrandr --listmonitors`; <0 = whole desktop
	Profile string // chrome --user-data-dir; empty = the user's own default profile/session

	rx, ry, rw, rh int     // resolved monitor region (origin + size, real pixels)
	imgW, imgH     int     // dimensions of the (downscaled) screenshot sent to the model
	scale          float64 // real pixels per image pixel (rw/imgW)
	resolved       bool
	targetWin      string  // X window id opened by the agent (open_url); region follows it
}

// targetWidth is the width screenshots are downscaled to before sending to the
// model. Smaller images = smaller payloads, faster turns, and they sit in the
// resolution range where vision models locate pixels most accurately.
const targetWidth = 1280

// New returns a Controller for the given display (defaults to ":0") scoped to
// the given monitor index (use a negative index for the whole desktop). profile
// is the chrome user-data-dir; empty means use the user's own default profile.
func New(display string, monitor int, profile string) *Controller {
	if display == "" {
		display = ":0"
	}
	return &Controller{Display: display, Monitor: monitor, Profile: profile}
}

var monitorLineRe = regexp.MustCompile(`^\s*(\d+):\s+\S+\s+(\d+)/\d+x(\d+)/\d+\+(\d+)\+(\d+)`)

// resolveRegion figures out the pixel region (origin+size) for Monitor by
// parsing `xrandr --listmonitors`. Falls back to the full root window.
func (c *Controller) resolveRegion(ctx context.Context) error {
	// Highest priority: follow the SPECIFIC window the agent opened (open_url).
	// This is robust against focus changes and other windows (terminals, the
	// user's own browser) — we track the exact window, not "whatever is active".
	if c.targetWin != "" {
		if c.retargetToWindowID(ctx, c.targetWin) {
			return nil
		}
		c.targetWin = "" // window closed — stop following it
	}
	// Monitor == -2: follow the active window's monitor (re-detected each call).
	if c.Monitor == -2 {
		if c.retargetToActiveWindow(ctx) {
			return nil
		}
		// fall through to the static logic below as a fallback
	}
	if c.resolved {
		return nil
	}
	// Default: whole root window.
	if out, err := c.run(ctx, "xdotool", "getdisplaygeometry"); err == nil {
		fmt.Sscanf(out, "%d %d", &c.rw, &c.rh)
	}
	if c.Monitor >= 0 {
		if out, err := c.run(ctx, "xrandr", "--listmonitors"); err == nil {
			for _, line := range strings.Split(out, "\n") {
				m := monitorLineRe.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				idx, _ := strconv.Atoi(m[1])
				if idx != c.Monitor {
					continue
				}
				c.rw, _ = strconv.Atoi(m[2])
				c.rh, _ = strconv.Atoi(m[3])
				c.rx, _ = strconv.Atoi(m[4])
				c.ry, _ = strconv.Atoi(m[5])
				break
			}
		}
	}
	if c.rw == 0 || c.rh == 0 {
		return fmt.Errorf("could not resolve display/monitor geometry for %s", c.Display)
	}
	// Compute the downscaled image size the model will see, and the scale factor
	// used to map the model's coordinates back to real pixels.
	if c.rw > targetWidth {
		c.imgW = targetWidth
		c.imgH = c.rh * targetWidth / c.rw
	} else {
		c.imgW, c.imgH = c.rw, c.rh
	}
	c.scale = float64(c.rw) / float64(c.imgW)
	c.resolved = true
	return nil
}

// env returns the parent environment with DISPLAY overridden to this controller's display.
func (c *Controller) env() []string {
	out := []string{"DISPLAY=" + c.Display}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "DISPLAY=") {
			out = append(out, e)
		}
	}
	return out
}

// run executes a command on the controlled display.
func (c *Controller) run(ctx context.Context, name string, args ...string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("computer use is only supported on Linux/X11 (current: %s)", runtime.GOOS)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = c.env()
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %v: %s", name, err, errb.String())
	}
	return out.String(), nil
}

// captureRegion grabs the current monitor region with ffmpeg's x11grab. Unlike
// ImageMagick's `import`, x11grab reads the framebuffer WITHOUT XGrabServer, so
// it does not freeze the user's screen (import grabs the X server for each
// capture, which — called repeatedly — makes the whole display feel locked).
// outW/outH > 0 scales the output; format is "png" or "rawvideo" (rgb24).
func (c *Controller) captureRegion(ctx context.Context, outW, outH int, format string) ([]byte, error) {
	return c.captureGeom(ctx, c.rx, c.ry, c.rw, c.rh, outW, outH, format)
}

// captureGeom grabs an arbitrary screen rectangle with ffmpeg x11grab.
func (c *Controller) captureGeom(ctx context.Context, x, y, w, h, outW, outH int, format string) ([]byte, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("computer use is only supported on Linux/X11 (current: %s)", runtime.GOOS)
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "x11grab", "-draw_mouse", "1",
		"-video_size", fmt.Sprintf("%dx%d", w, h),
		"-i", fmt.Sprintf("%s+%d,%d", c.Display, x, y), "-frames:v", "1"}
	if outW > 0 && outH > 0 {
		args = append(args, "-vf", fmt.Sprintf("scale=%d:%d", outW, outH))
	}
	if format == "rawvideo" {
		args = append(args, "-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1")
	} else {
		args = append(args, "-c:v", "png", "-f", "image2pipe", "pipe:1")
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Env = c.env()
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg x11grab failed (DISPLAY %s): %v: %s", c.Display, err, errb.String())
	}
	return out.Bytes(), nil
}

// WindowContext returns a short "where am I" anchor for the model: the focused
// window title (which app/page) and, for browsers, the URL read from the
// address-bar strip. Grounds the agent so it can orient (e.g. recognise Odoo).
func (c *Controller) WindowContext(ctx context.Context) string {
	if err := c.resolveRegion(ctx); err != nil {
		return ""
	}
	win := c.targetWin
	if win == "" {
		if out, err := c.run(ctx, "xdotool", "getactivewindow"); err == nil {
			win = strings.TrimSpace(out)
		}
	}
	var title string
	if win != "" {
		if out, err := c.run(ctx, "xdotool", "getwindowname", win); err == nil {
			title = strings.TrimSpace(out)
		}
	}
	parts := []string{}
	if title != "" {
		parts = append(parts, "Window: "+title)
	}
	if url := c.addressBarURL(ctx); url != "" {
		parts = append(parts, "URL: "+url)
	}
	return strings.Join(parts, " | ")
}

// Screenshot captures the controlled monitor as base64-encoded PNG.
func (c *Controller) Screenshot(ctx context.Context) (string, error) {
	if err := c.resolveRegion(ctx); err != nil {
		return "", err
	}
	png, err := c.captureRegion(ctx, c.imgW, c.imgH, "png")
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(png), nil
}

// Size returns the controlled monitor dimensions in pixels.
func (c *Controller) Size(ctx context.Context) (w, h int, err error) {
	if err := c.resolveRegion(ctx); err != nil {
		return 0, 0, err
	}
	return c.rw, c.rh, nil
}

type monitorRegion struct{ x, y, w, h int }

// monitors returns the geometry of every monitor on the display.
func (c *Controller) monitors(ctx context.Context) []monitorRegion {
	out, err := c.run(ctx, "xrandr", "--listmonitors")
	if err != nil {
		return nil
	}
	var ms []monitorRegion
	for _, line := range strings.Split(out, "\n") {
		m := monitorLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		w, _ := strconv.Atoi(m[2])
		h, _ := strconv.Atoi(m[3])
		x, _ := strconv.Atoi(m[4])
		y, _ := strconv.Atoi(m[5])
		ms = append(ms, monitorRegion{x, y, w, h})
	}
	return ms
}

// setRegion points the controller at a specific monitor region and recomputes
// the downscale factor used to map model coordinates back to real pixels.
func (c *Controller) setRegion(x, y, w, h int) {
	c.rx, c.ry, c.rw, c.rh = x, y, w, h
	if c.rw > targetWidth {
		c.imgW = targetWidth
		c.imgH = c.rh * targetWidth / c.rw
	} else {
		c.imgW, c.imgH = c.rw, c.rh
	}
	c.scale = float64(c.rw) / float64(c.imgW)
	c.resolved = true
}

// focusBrowserWindow activates (focuses + raises) the most recently opened
// Chromium window so it becomes the active window. Browsers launched in the
// background often don't grab focus, which would leave the agent acting on
// whatever else was focused (a terminal, the OpenUAI window, etc.).
func (c *Controller) focusBrowserWindow(ctx context.Context) {
	out, err := c.run(ctx, "xdotool", "search", "--onlyvisible", "--class", "chrom")
	if err != nil {
		return
	}
	ids := strings.Fields(out)
	if len(ids) == 0 {
		return
	}
	win := ids[len(ids)-1] // most recently created
	c.run(ctx, "xdotool", "windowactivate", "--sync", win)
	c.run(ctx, "xdotool", "windowraise", win)
	time.Sleep(300 * time.Millisecond)
}

// retargetToActiveWindow detects which monitor the currently-focused window is
// on and switches the controller to that monitor. Called after opening an app,
// so the agent looks at wherever the new window actually landed (multi-monitor).
// regionFromGeometry parses xdotool `getwindowgeometry --shell` output and
// points the controller at the monitor containing that window's center.
func (c *Controller) regionFromGeometry(ctx context.Context, geo string) bool {
	var X, Y, W, H int
	for _, line := range strings.Split(geo, "\n") {
		kv := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(kv) != 2 {
			continue
		}
		v, _ := strconv.Atoi(kv[1])
		switch kv[0] {
		case "X":
			X = v
		case "Y":
			Y = v
		case "WIDTH":
			W = v
		case "HEIGHT":
			H = v
		}
	}
	if W == 0 || H == 0 {
		return false
	}
	cx, cy := X+W/2, Y+H/2
	for _, m := range c.monitors(ctx) {
		if cx >= m.x && cx < m.x+m.w && cy >= m.y && cy < m.y+m.h {
			c.setRegion(m.x, m.y, m.w, m.h)
			return true
		}
	}
	return false
}

func (c *Controller) retargetToActiveWindow(ctx context.Context) bool {
	geo, err := c.run(ctx, "xdotool", "getactivewindow", "getwindowgeometry", "--shell")
	if err != nil {
		return false
	}
	return c.regionFromGeometry(ctx, geo)
}

// retargetToWindowID points the controller at the monitor of a specific window.
func (c *Controller) retargetToWindowID(ctx context.Context, win string) bool {
	geo, err := c.run(ctx, "xdotool", "getwindowgeometry", "--shell", win)
	if err != nil {
		return false
	}
	return c.regionFromGeometry(ctx, geo)
}

// browserWindowIDs returns the set of currently-visible Chromium window ids.
func (c *Controller) browserWindowIDs(ctx context.Context) map[string]bool {
	out, err := c.run(ctx, "xdotool", "search", "--onlyvisible", "--class", "chrom")
	if err != nil {
		return nil
	}
	ids := map[string]bool{}
	for _, id := range strings.Fields(out) {
		ids[id] = true
	}
	return ids
}

// captureSmall grabs a tiny fixed-size RGB thumbnail of the controlled monitor
// for cheap frame-to-frame comparison (used by waitStable).
func (c *Controller) captureSmall(ctx context.Context) ([]byte, error) {
	if err := c.resolveRegion(ctx); err != nil {
		return nil, err
	}
	return c.captureRegion(ctx, 32, 32, "rawvideo")
}

func avgByteDiff(a, b []byte) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 255
	}
	var sum int64
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		sum += int64(d)
	}
	return float64(sum) / float64(len(a))
}

// WaitStable polls the screen until it stops changing (the universal "ready"
// signal that works for ANY app, since we only have the pixels): it compares
// successive thumbnails and returns once two consecutive frames are nearly
// identical, or when maxWait elapses. Tiny changes (cursor blink) are tolerated
// by the threshold; things that never settle (video) are bounded by maxWait.
func (c *Controller) WaitStable(ctx context.Context, maxWait time.Duration) {
	if runtime.GOOS != "linux" {
		return
	}
	const poll = 300 * time.Millisecond
	const threshold = 2.0 // avg per-byte (0-255) difference considered "unchanged"
	prev, err := c.captureSmall(ctx)
	if err != nil {
		return
	}
	start := time.Now()
	stable := 0
	for time.Since(start) < maxWait {
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
		cur, err := c.captureSmall(ctx)
		if err != nil {
			return
		}
		if avgByteDiff(prev, cur) < threshold {
			if stable++; stable >= 2 {
				return
			}
		} else {
			stable = 0
		}
		prev = cur
	}
}

// abs converts image-space coordinates (as the model sees them in the
// downscaled, cropped screenshot) to absolute desktop pixels for xdotool:
// scale up by the downscale factor, then offset by the monitor origin.
func (c *Controller) abs(x, y int) (int, int) {
	return c.rx + int(float64(x)*c.scale), c.ry + int(float64(y)*c.scale)
}

// OpenURL opens a URL in a browser window on the controlled display, so the
// agent can navigate without clicking through the UI. It launches a Chromium
// browser with a dedicated profile and a new window, so it reliably appears on
// THIS display and is not hijacked by (or locked against) the user's own
// running browser. Falls back to xdg-open.
func (c *Controller) OpenURL(ctx context.Context, url string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("computer use is only supported on Linux/X11 (current: %s)", runtime.GOOS)
	}
	for _, br := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		path, err := exec.LookPath(br)
		if err != nil {
			continue
		}
		// Default: open in the user's own profile/session (a new window in their
		// running Chrome) so logins/cookies are theirs. Only when a dedicated
		// Profile is set (e.g. an isolated virtual display) do we use a separate
		// user-data-dir with the Xvfb-friendly sandbox/gpu flags.
		args := []string{"--new-window", "--no-first-run", "--no-default-browser-check", "--start-maximized"}
		if c.Profile != "" {
			args = append(args, "--no-sandbox", "--disable-setuid-sandbox", "--disable-gpu", "--user-data-dir="+c.Profile)
		}
		args = append(args, url)
		before := c.browserWindowIDs(ctx) // snapshot to detect the new window
		cmd := exec.Command(path, args...)
		cmd.Env = c.env()
		if err := cmd.Start(); err != nil {
			continue
		}
		// Find the window this launch created and lock onto it.
		for i := 0; i < 25; i++ {
			time.Sleep(300 * time.Millisecond)
			for id := range c.browserWindowIDs(ctx) {
				if !before[id] {
					c.targetWin = id
					break
				}
			}
			if c.targetWin != "" {
				break
			}
		}
		if c.targetWin != "" {
			c.run(ctx, "xdotool", "windowactivate", c.targetWin)
			c.run(ctx, "xdotool", "windowraise", c.targetWin)
			c.retargetToWindowID(ctx, c.targetWin)
		} else {
			c.retargetToActiveWindow(ctx)
		}
		c.WaitStable(ctx, 10*time.Second)
		return nil
	}
	cmd := exec.Command("xdg-open", url)
	cmd.Env = c.env()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open url: %w", err)
	}
	time.Sleep(800 * time.Millisecond)
	c.WaitStable(ctx, 10*time.Second)
	return nil
}

func itoa(n int) string { return strconv.Itoa(n) }

// Move moves the pointer to monitor-local (x,y).
func (c *Controller) Move(ctx context.Context, x, y int) error {
	if err := c.resolveRegion(ctx); err != nil {
		return err
	}
	ax, ay := c.abs(x, y)
	_, err := c.run(ctx, "xdotool", "mousemove", "--sync", itoa(ax), itoa(ay))
	return err
}

// Click moves to monitor-local (x,y) and clicks the given button (1=left,2=middle,3=right), repeated `repeat` times.
func (c *Controller) Click(ctx context.Context, x, y, button, repeat int) error {
	if err := c.resolveRegion(ctx); err != nil {
		return err
	}
	ax, ay := c.abs(x, y)
	if _, err := c.run(ctx, "xdotool", "mousemove", "--sync", itoa(ax), itoa(ay)); err != nil {
		return err
	}
	args := []string{"click"}
	if repeat > 1 {
		args = append(args, "--repeat", itoa(repeat))
	}
	args = append(args, itoa(button))
	_, err := c.run(ctx, "xdotool", args...)
	time.Sleep(150 * time.Millisecond)
	return err
}

// TypeText types literal text at the focused element. It normalizes the
// keyboard to the US layout while typing: xdotool maps characters through the
// ACTIVE X layout, so on non-US layouts (e.g. "es") symbols are mistyped (':'
// came out as 'Ñ'). The user's layout is restored afterward.
func (c *Controller) TypeText(ctx context.Context, text string) error {
	restore := c.forceUSLayout(ctx)
	defer restore()
	_, err := c.run(ctx, "xdotool", "type", "--clearmodifiers", "--delay", "12", text)
	time.Sleep(120 * time.Millisecond)
	return err
}

// forceUSLayout switches the X keyboard layout to US and returns a function that
// restores the previous layout. No-op if setxkbmap is unavailable.
func (c *Controller) forceUSLayout(ctx context.Context) func() {
	if _, err := exec.LookPath("setxkbmap"); err != nil {
		return func() {}
	}
	prev := ""
	if out, err := c.run(ctx, "setxkbmap", "-query"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "layout:") {
				prev = strings.TrimSpace(strings.TrimPrefix(line, "layout:"))
			}
		}
	}
	c.run(ctx, "setxkbmap", "us")
	return func() {
		if prev != "" && prev != "us" {
			c.run(ctx, "setxkbmap", prev)
		}
	}
}

// Key presses a key combo using xdotool keysyms (e.g. "Return", "ctrl+a").
func (c *Controller) Key(ctx context.Context, keys string) error {
	_, err := c.run(ctx, "xdotool", "key", keys)
	time.Sleep(200 * time.Millisecond)
	return err
}

// Scroll scrolls at (x,y): direction up/down/left/right, `amount` clicks.
func (c *Controller) Scroll(ctx context.Context, x, y int, direction string, amount int) error {
	btn := map[string]string{"up": "4", "down": "5", "left": "6", "right": "7"}[direction]
	if btn == "" {
		return fmt.Errorf("invalid scroll direction %q", direction)
	}
	if err := c.resolveRegion(ctx); err != nil {
		return err
	}
	ax, ay := c.abs(x, y)
	if _, err := c.run(ctx, "xdotool", "mousemove", "--sync", itoa(ax), itoa(ay)); err != nil {
		return err
	}
	if amount < 1 {
		amount = 3
	}
	for i := 0; i < amount; i++ {
		if _, err := c.run(ctx, "xdotool", "click", btn); err != nil {
			return err
		}
	}
	return nil
}
