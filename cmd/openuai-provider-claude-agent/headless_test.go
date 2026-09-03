package main

import (
	"encoding/json"
	"strings"
	"testing"

	"openuai/internal/llm"
	"openuai/internal/llm/plugin"
)

// The description the core caches has to survive its own validation, which
// refuses a description that contradicts itself. It is also the one place the
// name and the optional-key wording are stated, so both are pinned here.
func TestDescriptionIsValidAndCarriesTheChosenName(t *testing.T) {
	d := describe()
	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if d.Name != "claude-headless" {
		t.Errorf("Name = %q, want claude-headless", d.Name)
	}
	// Anthropic's branding guidance does not permit a third-party product to
	// be called this, so a rename that reintroduces it should fail here.
	if strings.Contains(d.DisplayName, "Claude Code") {
		t.Errorf("DisplayName = %q, which is a name the branding guidance does not permit", d.DisplayName)
	}
	if !strings.Contains(strings.ToLower(d.SecretPlaceholder), "optional") {
		t.Errorf("SecretPlaceholder = %q, want it to say the key is optional: the ordinary path is the "+
			"session signed in outside openuai", d.SecretPlaceholder)
	}
	if !d.SupportsTools || !d.SupportsReady || !d.SupportsSecret {
		t.Errorf("capabilities = tools:%v ready:%v secret:%v, want all three",
			d.SupportsTools, d.SupportsReady, d.SupportsSecret)
	}
}

// --tools "" is what makes this a model backend rather than a second agent,
// and --restricted is what stops the run reading the working directory's
// settings. Both are load-bearing, so their absence is a test failure.
func TestArgsLeaveTheAgentNoToolsAndNoHostSettings(t *testing.T) {
	args := invocation{model: "opus", system: "sys", prompt: "hi"}.args()
	joined := strings.Join(args, " ")

	for _, want := range []string{"-p", "--output-format stream-json", "--verbose", "--restricted", "--strict-mcp-config"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args are missing %q: %v", want, args)
		}
	}
	// --tools is followed by an empty string, which does not survive a naive
	// join, so it is checked positionally.
	found := false
	for i, a := range args {
		if a == "--tools" && i+1 < len(args) && args[i+1] == "" {
			found = true
		}
	}
	if !found {
		t.Errorf(`args do not pass --tools "": %q`, args)
	}
}

// --bare never reads OAuth credentials or the keychain, so on the prior-login
// path — the ordinary one — it would authenticate nothing. It belongs only to
// the API key path.
func TestBareIsUsedOnlyWithAnAPIKey(t *testing.T) {
	withKey := strings.Join(invocation{apiKey: "sk-ant-x"}.args(), " ")
	if !strings.Contains(withKey, "--bare") {
		t.Error("an API key run does not pass --bare, so it would read host configuration it does not need")
	}

	withSession := strings.Join(invocation{}.args(), " ")
	if strings.Contains(withSession, "--bare") {
		t.Error("a prior-login run passes --bare, which never reads OAuth credentials: it would authenticate nothing")
	}
}

// A bad credential is retried ten times over about three minutes, while the
// core abandons a call long before that. Reporting the category on the first
// retry is what turns that into a message someone can act on.
func TestFatalRetryIsSurfacedImmediately(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","model":"claude-opus-5","tools":[]}`,
		`{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,"error_status":401,"error":"authentication_failed"}`,
		`{"type":"system","subtype":"api_retry","attempt":2,"max_retries":10,"error_status":401,"error":"authentication_failed"}`,
	}, "\n")

	_, err := parseStream(strings.NewReader(stream))
	if err == nil {
		t.Fatal("parseStream() = nil error on an authentication failure, want it reported")
	}
	if !strings.Contains(err.Error(), "auth login") {
		t.Errorf("error = %q, want it to name the action that fixes it", err)
	}
}

// A category that retrying can fix must not abort the run: that is what the
// retry is for.
func TestRetryableCategoryIsLeftToTheRetry(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,"error_status":529,"error":"overloaded"}`,
		`{"type":"result","result":"done","usage":{"input_tokens":5,"output_tokens":7},"total_cost_usd":0.01}`,
	}, "\n")

	out, err := parseStream(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parseStream() = %v, want the overload left to the retry", err)
	}
	if out.Text != "done" {
		t.Errorf("text = %q, want done", out.Text)
	}
}

// The resolved model id comes back on every run, so usage is costed against a
// real name rather than the alias that was asked for.
func TestResolvedModelIsReportedNotTheAlias(t *testing.T) {
	stream := `{"type":"system","subtype":"init","model":"claude-opus-5","tools":[]}` + "\n" +
		`{"type":"result","result":"hi","usage":{"input_tokens":11,"output_tokens":3}}`

	out, err := parseStream(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parseStream() = %v", err)
	}
	resp := out.response("opus")
	if resp.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want the resolved id claude-opus-5", resp.Model)
	}
	if resp.InputTokens != 11 || resp.OutputTokens != 3 {
		t.Errorf("tokens = %d/%d, want 11/3", resp.InputTokens, resp.OutputTokens)
	}

	// With no init event there is nothing to resolve, so the alias stands.
	bare, _ := parseStream(strings.NewReader(`{"type":"result","result":"hi"}`))
	if got := bare.response("sonnet").Model; got != "sonnet" {
		t.Errorf("Model = %q with no init event, want the alias sonnet", got)
	}
}

// An unknown event type must not break the parse: the stream gains event
// types, and a provider that fails on one it has never seen is a provider that
// breaks on an upgrade.
func TestUnknownEventsAreIgnored(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"something_new","whatever":{"nested":true}}`,
		`not json at all`,
		`{"type":"result","result":"still fine","usage":{"output_tokens":2}}`,
	}, "\n")

	out, err := parseStream(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parseStream() = %v, want unknown events ignored", err)
	}
	if out.Text != "still fine" {
		t.Errorf("text = %q", out.Text)
	}
}

// A run that reports failure in its result line has to fail the call, or a
// turn silently succeeds with no content.
func TestResultMarkedAsErrorFailsTheCall(t *testing.T) {
	_, err := parseStream(strings.NewReader(
		`{"type":"result","is_error":true,"result":"something went wrong","terminal_reason":"api_error"}`))
	if err == nil || !strings.Contains(err.Error(), "something went wrong") {
		t.Errorf("error = %v, want the reported failure", err)
	}
}

// Tool calls come back as data because the tools belong to openuai, so the
// mapping onto llm.ToolCall is the whole point of the structured answer.
func TestStructuredAnswerBecomesToolCalls(t *testing.T) {
	out := &outcome{StructuredOutput: json.RawMessage(
		`{"reply":"","tool_calls":[{"name":"read_file","arguments":{"path":"/tmp/x"}},{"name":"bash","arguments":{"command":"ls"}}]}`)}

	a, err := parseAnswer(out)
	if err != nil {
		t.Fatalf("parseAnswer() = %v", err)
	}
	calls := a.toolCalls()
	if len(calls) != 2 {
		t.Fatalf("got %d tool calls, want 2", len(calls))
	}
	if calls[0].Name != "read_file" || calls[0].Arguments["path"] != "/tmp/x" {
		t.Errorf("first call = %+v", calls[0])
	}
	// The core matches a result back to its call by id, and the headless run
	// mints none, so each call needs a distinct one.
	if calls[0].ID == "" || calls[0].ID == calls[1].ID {
		t.Errorf("ids are not distinct: %q and %q", calls[0].ID, calls[1].ID)
	}
}

// A text answer with no structured output is the ordinary case when nothing is
// called, and it must not be read as an empty reply.
func TestPlainTextAnswerSurvivesWithNoStructuredOutput(t *testing.T) {
	a, err := parseAnswer(&outcome{Text: "just an answer"})
	if err != nil {
		t.Fatalf("parseAnswer() = %v", err)
	}
	if a.Reply != "just an answer" {
		t.Errorf("Reply = %q", a.Reply)
	}
	if len(a.toolCalls()) != 0 {
		t.Errorf("tool calls = %v, want none", a.toolCalls())
	}
}

// The prompt is the only channel to the model, so a tool result has to arrive
// tied to the call it answers or the loop cannot proceed.
func TestPromptTiesToolResultsToTheirCalls(t *testing.T) {
	prompt := renderPrompt([]llm.Message{
		{Role: llm.RoleUser, Content: "list the files"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "bash", Arguments: map[string]string{"command": "ls"}}}},
		{Role: llm.RoleToolResult, ToolCallID: "call_1", Content: "a.txt"},
	}, []llm.ToolDefinition{{
		Name:        "bash",
		Description: "run a command",
		Parameters:  []llm.ToolParam{{Name: "command", Type: "string", Description: "the command", Required: true}},
	}})

	for _, want := range []string{"bash: run a command", "command (string, required)", "call_1", "a.txt"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not carry %q:\n%s", want, prompt)
		}
	}
}

// Images cannot cross this boundary — a headless run takes a prompt, not image
// inputs — and computer-use turns depend on them, so the loss is stated in the
// prompt rather than passing unnoticed.
func TestOmittedImagesAreDeclaredInThePrompt(t *testing.T) {
	prompt := renderPrompt([]llm.Message{
		{Role: llm.RoleUser, Content: "what is on screen?", Images: []string{"iVBOR", "iVBOR"}},
	}, nil)
	if !strings.Contains(prompt, "2 image(s) omitted") {
		t.Errorf("prompt does not declare the omitted images:\n%s", prompt)
	}
}

// The result line carries a whole reply, which outgrows a scanner's default
// buffer. A long answer must not come back as a read error.
func TestLongResultLineIsRead(t *testing.T) {
	long := strings.Repeat("x", 200*1024)
	line, err := json.Marshal(map[string]any{"type": "result", "result": long})
	if err != nil {
		t.Fatal(err)
	}
	out, err := parseStream(strings.NewReader(string(line)))
	if err != nil {
		t.Fatalf("parseStream() on a %d byte result = %v", len(long), err)
	}
	if len(out.Text) != len(long) {
		t.Errorf("text is %d bytes, want %d", len(out.Text), len(long))
	}
}

// The plugin speaks the same protocol types the core does, so a description
// round-trips through the wire shape without losing a capability.
func TestDescriptionRoundTripsThroughTheWire(t *testing.T) {
	data, err := json.Marshal(describe())
	if err != nil {
		t.Fatal(err)
	}
	var back plugin.Description
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Name != describe().Name || !back.SupportsTools || back.DefaultModel != "opus" {
		t.Errorf("round-tripped description = %+v", back)
	}
}

// The core ships no prices of its own, so a provider that declares none has
// every turn costed at zero — silently, because nothing errors. Every model
// offered must be priced, and so must the id each alias resolves to, since it
// is the resolved id that a run reports and therefore the one that gets
// priced.
func TestEveryOfferedModelIsPriced(t *testing.T) {
	d := describe()
	if len(d.Pricing) == 0 {
		t.Fatal("no pricing declared: every turn through this provider would be costed at zero")
	}
	for _, m := range d.Models {
		price, ok := d.Pricing[m]
		if !ok {
			t.Errorf("model %q is offered but has no price, so its turns cost zero", m)
			continue
		}
		if price[0] <= 0 || price[1] <= 0 {
			t.Errorf("model %q is priced %v, want both figures above zero", m, price)
		}
	}
	if _, ok := d.Pricing[d.DefaultModel]; !ok {
		t.Errorf("the default model %q has no price", d.DefaultModel)
	}
	// The alias is what is asked for; the resolved id is what comes back and
	// what the core looks up.
	for _, resolved := range []string{"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"} {
		if _, ok := d.Pricing[resolved]; !ok {
			t.Errorf("resolved id %q has no price, so a run reporting it costs zero", resolved)
		}
	}
}
