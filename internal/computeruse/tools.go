package computeruse

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"openuai/internal/tools"
)

// atoi parses an int argument, defaulting to def on error.
func atoi(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// shot waits for the screen to settle, then captures a screenshot and returns
// it as an image so the model sees a stable, finished state after the action.
func (c *Controller) shot(ctx context.Context, output string) tools.Result {
	c.WaitStable(ctx, 4*time.Second)
	img, err := c.Screenshot(ctx)
	if err != nil {
		return tools.Result{Output: output, Error: err.Error()}
	}
	// Anchor the screenshot with "where am I" context so the model can orient.
	if cxt := c.WindowContext(ctx); cxt != "" {
		output = output + "\n[" + cxt + "]"
	}
	return tools.Result{Output: output, Images: []string{img}}
}

// RegisterTools registers all computer-use tools backed by the controller.
func RegisterTools(reg *tools.Registry, c *Controller) {
	reg.Register(&screenshotTool{c})
	reg.Register(&clickTool{c, "computer_click", "Left-click", 1, 1})
	reg.Register(&clickTool{c, "computer_double_click", "Double-click", 1, 2})
	reg.Register(&clickTool{c, "computer_right_click", "Right-click", 3, 1})
	reg.Register(&typeTool{c})
	reg.Register(&keyTool{c})
	reg.Register(&scrollTool{c})
	reg.Register(&openURLTool{c})
	reg.Register(&actionsTool{c})
	reg.Register(&findTextTool{c})
	reg.Register(&clickTextTool{c})
}

type findTextTool struct{ c *Controller }

func (t *findTextTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "computer_find_text",
		Description: "Locate visible text on screen via OCR and return its pixel coordinates. Use this to find exactly where a label/button/link is instead of guessing coordinates from the screenshot.",
		Parameters: []tools.Parameter{
			{Name: "text", Type: "string", Description: "The visible text to locate (e.g. a button or link label)", Required: true},
		},
		RequiresPermission: "none", // read-only
	}
}
func (t *findTextTool) Execute(ctx context.Context, args map[string]string) tools.Result {
	words, err := t.c.ocrWords(ctx)
	if err != nil {
		return tools.Result{Error: err.Error()}
	}
	cx, cy, ok := findText(words, args["text"])
	if !ok {
		return tools.Result{Output: fmt.Sprintf("Text %q not found on screen", args["text"])}
	}
	return tools.Result{Output: fmt.Sprintf("Found %q at (%d,%d)", args["text"], cx, cy)}
}

type clickTextTool struct{ c *Controller }

func (t *clickTextTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "computer_click_text",
		Description: "Find visible text on screen via OCR and click its center. The reliable way to click a button or link by its label — no coordinate guessing. Returns a screenshot of the result.",
		Parameters: []tools.Parameter{
			{Name: "text", Type: "string", Description: "The visible text of the element to click (e.g. \"Contact Us\", \"Submit\")", Required: true},
		},
		RequiresPermission: perm,
	}
}
func (t *clickTextTool) Execute(ctx context.Context, args map[string]string) tools.Result {
	words, err := t.c.ocrWords(ctx)
	if err != nil {
		return tools.Result{Error: err.Error()}
	}
	cx, cy, ok := findText(words, args["text"])
	if !ok {
		return tools.Result{Error: fmt.Sprintf("Text %q not found on screen — take a screenshot to see what's there", args["text"])}
	}
	if err := t.c.Click(ctx, cx, cy, 1, 1); err != nil {
		return tools.Result{Error: err.Error()}
	}
	return t.c.shot(ctx, fmt.Sprintf("clicked text %q at (%d,%d)", args["text"], cx, cy))
}

type openURLTool struct{ c *Controller }

func (t *openURLTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "computer_open_url",
		Description: "Open a URL in the default browser directly (no clicking needed). Use this to start a web task instead of clicking through the UI. Returns a screenshot once loaded.",
		Parameters: []tools.Parameter{
			{Name: "url", Type: "string", Description: "The URL to open", Required: true},
		},
		RequiresPermission: perm,
	}
}
func (t *openURLTool) Execute(ctx context.Context, args map[string]string) tools.Result {
	if args["url"] == "" {
		return tools.Result{Error: "url is required"}
	}
	if err := t.c.OpenURL(ctx, args["url"]); err != nil {
		return tools.Result{Error: err.Error()}
	}
	return t.c.shot(ctx, "opened "+args["url"])
}

// actionStep is one step in a batched computer_actions call.
type actionStep struct {
	Action    string `json:"action"` // click, double_click, right_click, type, key, scroll, move, wait
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Text      string `json:"text"`
	Keys      string `json:"keys"`
	Direction string `json:"direction"`
	Amount    int    `json:"amount"`
	Seconds   int    `json:"seconds"`
}

type actionsTool struct{ c *Controller }

func (t *actionsTool) Definition() tools.Definition {
	return tools.Definition{
		Name: "computer_actions",
		Description: "Execute a SEQUENCE of screen actions in ONE call (efficient: take a screenshot once, locate ALL targets, then do everything here). Returns a single screenshot after all steps. " +
			"The `steps` argument is a JSON array; each item has an \"action\" plus its fields:\n" +
			"  {\"action\":\"click\",\"x\":100,\"y\":200}\n" +
			"  {\"action\":\"double_click\"|\"right_click\",\"x\":..,\"y\":..}\n" +
			"  {\"action\":\"type\",\"text\":\"hello\"}\n" +
			"  {\"action\":\"key\",\"keys\":\"Tab\"|\"Return\"|\"ctrl+a\"}\n" +
			"  {\"action\":\"scroll\",\"x\":..,\"y\":..,\"direction\":\"down\",\"amount\":3}\n" +
			"  {\"action\":\"move\",\"x\":..,\"y\":..}\n" +
			"Example filling a form: click field 1, type, click field 2, type, ... in a single call. " +
			"You do not need to add waits — the screen is allowed to settle automatically before the next screenshot.",
		Parameters: []tools.Parameter{
			{Name: "steps", Type: "string", Description: "JSON array of action steps (see description)", Required: true},
		},
		RequiresPermission: perm,
	}
}
func (t *actionsTool) Execute(ctx context.Context, args map[string]string) tools.Result {
	var steps []actionStep
	if err := json.Unmarshal([]byte(args["steps"]), &steps); err != nil {
		return tools.Result{Error: "steps must be a JSON array of action objects: " + err.Error()}
	}
	if len(steps) == 0 {
		return tools.Result{Error: "steps is empty"}
	}
	done := 0
	for i, s := range steps {
		var err error
		switch s.Action {
		case "click":
			err = t.c.Click(ctx, s.X, s.Y, 1, 1)
		case "double_click":
			err = t.c.Click(ctx, s.X, s.Y, 1, 2)
		case "right_click":
			err = t.c.Click(ctx, s.X, s.Y, 3, 1)
		case "type":
			err = t.c.TypeText(ctx, s.Text)
		case "key":
			err = t.c.Key(ctx, s.Keys)
		case "scroll":
			err = t.c.Scroll(ctx, s.X, s.Y, s.Direction, s.Amount)
		case "move":
			err = t.c.Move(ctx, s.X, s.Y)
		default:
			err = fmt.Errorf("unknown action %q", s.Action)
		}
		if err != nil {
			res := t.c.shot(ctx, fmt.Sprintf("completed %d/%d steps, then step %d (%s) failed", done, len(steps), i+1, s.Action))
			res.Error = err.Error()
			return res
		}
		done++
	}
	return t.c.shot(ctx, fmt.Sprintf("executed %d steps", done))
}

const perm = "session"

type screenshotTool struct{ c *Controller }

func (t *screenshotTool) Definition() tools.Definition {
	return tools.Definition{
		Name:               "computer_screenshot",
		Description:        "Take a screenshot of the screen to see its current state. Use this first, and whenever you need to re-check the screen.",
		Parameters:         []tools.Parameter{},
		RequiresPermission: "none", // read-only: just looking at the screen
	}
}
func (t *screenshotTool) Execute(ctx context.Context, _ map[string]string) tools.Result {
	return t.c.shot(ctx, "screenshot")
}

type clickTool struct {
	c           *Controller
	name, desc  string
	button      int
	repeat      int
}

func (t *clickTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        t.name,
		Description: t.desc + " at absolute pixel coordinates (x,y), top-left origin. Returns a screenshot of the result.",
		Parameters: []tools.Parameter{
			{Name: "x", Type: "integer", Description: "X pixel coordinate", Required: true},
			{Name: "y", Type: "integer", Description: "Y pixel coordinate", Required: true},
		},
		RequiresPermission: perm,
	}
}
func (t *clickTool) Execute(ctx context.Context, args map[string]string) tools.Result {
	x, y := atoi(args["x"], -1), atoi(args["y"], -1)
	if x < 0 || y < 0 {
		return tools.Result{Error: "x and y are required integer coordinates"}
	}
	if err := t.c.Click(ctx, x, y, t.button, t.repeat); err != nil {
		return tools.Result{Error: err.Error()}
	}
	return t.c.shot(ctx, fmt.Sprintf("%s at (%d,%d)", t.desc, x, y))
}

type typeTool struct{ c *Controller }

func (t *typeTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "computer_type",
		Description: "Type literal text at the currently focused field. Click the field first. Returns a screenshot.",
		Parameters: []tools.Parameter{
			{Name: "text", Type: "string", Description: "The text to type", Required: true},
		},
		RequiresPermission: perm,
	}
}
func (t *typeTool) Execute(ctx context.Context, args map[string]string) tools.Result {
	if args["text"] == "" {
		return tools.Result{Error: "text is required"}
	}
	if err := t.c.TypeText(ctx, args["text"]); err != nil {
		return tools.Result{Error: err.Error()}
	}
	return t.c.shot(ctx, "typed text")
}

type keyTool struct{ c *Controller }

func (t *keyTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "computer_key",
		Description: "Press a key or combo using xdotool keysyms, e.g. \"Return\", \"Tab\", \"ctrl+a\", \"Escape\". Returns a screenshot.",
		Parameters: []tools.Parameter{
			{Name: "keys", Type: "string", Description: "Key combo (xdotool keysym syntax)", Required: true},
		},
		RequiresPermission: perm,
	}
}
func (t *keyTool) Execute(ctx context.Context, args map[string]string) tools.Result {
	if args["keys"] == "" {
		return tools.Result{Error: "keys is required"}
	}
	if err := t.c.Key(ctx, args["keys"]); err != nil {
		return tools.Result{Error: err.Error()}
	}
	return t.c.shot(ctx, "pressed "+args["keys"])
}

type scrollTool struct{ c *Controller }

func (t *scrollTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "computer_scroll",
		Description: "Scroll at (x,y) in a direction (up/down/left/right) by `amount` clicks. Returns a screenshot.",
		Parameters: []tools.Parameter{
			{Name: "x", Type: "integer", Description: "X pixel coordinate", Required: true},
			{Name: "y", Type: "integer", Description: "Y pixel coordinate", Required: true},
			{Name: "direction", Type: "string", Description: "up, down, left or right", Required: true},
			{Name: "amount", Type: "integer", Description: "Number of scroll clicks (default 3)", Required: false},
		},
		RequiresPermission: perm,
	}
}
func (t *scrollTool) Execute(ctx context.Context, args map[string]string) tools.Result {
	x, y := atoi(args["x"], -1), atoi(args["y"], -1)
	if x < 0 || y < 0 {
		return tools.Result{Error: "x and y are required integer coordinates"}
	}
	if err := t.c.Scroll(ctx, x, y, args["direction"], atoi(args["amount"], 3)); err != nil {
		return tools.Result{Error: err.Error()}
	}
	return t.c.shot(ctx, fmt.Sprintf("scrolled %s at (%d,%d)", args["direction"], x, y))
}
