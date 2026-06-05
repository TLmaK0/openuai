package computeruse

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

var urlRe = regexp.MustCompile(`[a-zA-Z0-9][a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}(/[^\s]*)?`)

// addressBarURL OCRs the top toolbar strip and returns a URL-looking token, so
// the agent knows the current address (the URL is unreadable in the downscaled
// full screenshot). Best-effort: returns "" if not found.
func (c *Controller) addressBarURL(ctx context.Context) string {
	if _, err := exec.LookPath("tesseract"); err != nil {
		return ""
	}
	stripH := 95
	if stripH > c.rh {
		stripH = c.rh
	}
	png, err := c.captureGeom(ctx, c.rx, c.ry, c.rw, stripH, c.rw*2, stripH*2, "png")
	if err != nil {
		return ""
	}
	f, err := os.CreateTemp("", "cu-url-*.png")
	if err != nil {
		return ""
	}
	defer os.Remove(f.Name())
	f.Write(png)
	f.Close()
	gray, gerr := os.CreateTemp("", "cu-url-g-*.png")
	if gerr != nil {
		return ""
	}
	defer os.Remove(gray.Name())
	gray.Close()
	if exec.CommandContext(ctx, "convert", f.Name(), "-colorspace", "Gray", gray.Name()).Run() != nil {
		return ""
	}
	out := bytes.Buffer{}
	cmd := exec.CommandContext(ctx, "tesseract", gray.Name(), "stdout", "--psm", "6", "-l", "eng")
	cmd.Stdout = &out
	if cmd.Run() != nil {
		return ""
	}
	return urlRe.FindString(out.String())
}

// ocrWord is one OCR-detected word with its bounding box (in the model's
// image-space coordinates, i.e. the downscaled screenshot the model sees).
type ocrWord struct {
	text              string
	x, y, w, h        int
	block, par, line  int
}

// ocrWords runs OCR (tesseract) on the controlled monitor and returns the
// detected words with their boxes, in the same image-space the model clicks in.
// Generic: works for any app, since it reads the rendered pixels.
func (c *Controller) ocrWords(ctx context.Context) ([]ocrWord, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("computer use is only supported on Linux/X11 (current: %s)", runtime.GOOS)
	}
	if err := c.resolveRegion(ctx); err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("tesseract"); err != nil {
		return nil, fmt.Errorf("tesseract OCR not installed")
	}
	// Capture the same downscaled view the model sees (via ffmpeg x11grab, no
	// X-server grab), so OCR coordinates match the model's image space.
	png, err := c.captureRegion(ctx, c.imgW, c.imgH, "png")
	if err != nil {
		return nil, fmt.Errorf("screenshot for OCR: %w", err)
	}
	f, err := os.CreateTemp("", "cu-ocr-*.png")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	f.Write(png)
	f.Close()

	// Preprocess for reliable OCR: grayscale + upscale 2x (tesseract needs
	// reasonably large text), run with --psm 3 (full-page layout). Do it on the
	// image AND its negative so we read both dark-on-light and light-on-dark
	// (dark-theme UIs, colored buttons). Coordinates from the 2x image are
	// divided back by 2 to the model's image space.
	const ocrScale = 2
	var words []ocrWord
	for _, negate := range []bool{false, true} {
		prep, perr := os.CreateTemp("", "cu-ocr-prep-*.png")
		if perr != nil {
			continue
		}
		prep.Close()
		args := []string{f.Name()}
		if negate {
			args = append(args, "-negate")
		}
		args = append(args, "-colorspace", "Gray", "-resize", "200%", prep.Name())
		if exec.CommandContext(ctx, "convert", args...).Run() == nil {
			words = append(words, c.runTesseract(ctx, prep.Name(), ocrScale)...)
		}
		os.Remove(prep.Name())
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("OCR found no text on screen")
	}
	return words, nil
}

// runTesseract OCRs one image file (--psm 3) and returns its words, scaling
// coordinates down by `div` (the OCR upscale factor).
func (c *Controller) runTesseract(ctx context.Context, path string, div int) []ocrWord {
	ocr := exec.CommandContext(ctx, "tesseract", path, "stdout", "--psm", "3", "-l", "eng+spa", "tsv")
	var out bytes.Buffer
	ocr.Stdout = &out
	if err := ocr.Run(); err != nil {
		return nil
	}
	return parseTSV(out.String(), div)
}

func parseTSV(tsv string, div int) []ocrWord {
	if div < 1 {
		div = 1
	}
	var words []ocrWord
	for i, line := range strings.Split(tsv, "\n") {
		if i == 0 { // header
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 12 {
			continue
		}
		text := strings.TrimSpace(f[11])
		if text == "" {
			continue
		}
		conf, _ := strconv.ParseFloat(f[10], 64)
		if conf < 30 {
			continue
		}
		atoiRaw := func(s string) int { n, _ := strconv.Atoi(s); return n }
		px := func(s string) int { return atoiRaw(s) / div } // pixel coords scaled back
		words = append(words, ocrWord{
			text:  text,
			x:     px(f[6]), y: px(f[7]), w: px(f[8]), h: px(f[9]),
			block: atoiRaw(f[2]), par: atoiRaw(f[3]), line: atoiRaw(f[4]),
		})
	}
	return words
}

// findText locates `query` among OCR words and returns the center of the
// matching text. Matches a single word containing the query, or a run of
// consecutive words on one line whose joined text contains it.
func findText(words []ocrWord, query string) (cx, cy int, ok bool) {
	q := strings.ToLower(strings.Join(strings.Fields(query), " "))
	if q == "" {
		return 0, 0, false
	}
	for _, w := range words {
		if strings.Contains(strings.ToLower(w.text), q) {
			return w.x + w.w/2, w.y + w.h/2, true
		}
	}
	// group words by line, preserving order
	type key struct{ b, p, l int }
	lines := map[key][]ocrWord{}
	var order []key
	for _, w := range words {
		k := key{w.block, w.par, w.line}
		if _, seen := lines[k]; !seen {
			order = append(order, k)
		}
		lines[k] = append(lines[k], w)
	}
	for _, k := range order {
		ws := lines[k]
		for i := range ws {
			joined := ""
			for j := i; j < len(ws); j++ {
				if joined != "" {
					joined += " "
				}
				joined += strings.ToLower(ws[j].text)
				if strings.Contains(joined, q) {
					minx, miny := ws[i].x, ws[i].y
					maxx, maxy := ws[i].x+ws[i].w, ws[i].y+ws[i].h
					for m := i; m <= j; m++ {
						if ws[m].x < minx {
							minx = ws[m].x
						}
						if ws[m].y < miny {
							miny = ws[m].y
						}
						if ws[m].x+ws[m].w > maxx {
							maxx = ws[m].x + ws[m].w
						}
						if ws[m].y+ws[m].h > maxy {
							maxy = ws[m].y + ws[m].h
						}
					}
					return (minx + maxx) / 2, (miny + maxy) / 2, true
				}
				if len(joined) > len(q)+40 {
					break
				}
			}
		}
	}
	return 0, 0, false
}
