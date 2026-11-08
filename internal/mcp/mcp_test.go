package mcp

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/policy"
)

type harness struct {
	g   *gateway.Gateway
	dev *demo.Device
	srv *Server
}

func newHarness(t *testing.T, opts ...func(*gateway.Options)) *harness {
	t.Helper()
	dev := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	pol := policy.Default()
	for cat, e := range demo.PolicyApps() {
		pol.AddApps(cat, e)
	}
	o := gateway.Options{Providers: []driver.Provider{demo.NewProvider(driver.Android, dev)}, Policy: pol,
		TrustNewAgents: true, NoScreenshots: true, SettleMax: 200 * time.Millisecond, PollInterval: 5 * time.Millisecond,
		MaxBlock: 2 * time.Second, DiscoverEvery: 20 * time.Millisecond}
	for _, f := range opts {
		f(&o)
	}
	g, err := gateway.New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return &harness{g: g, dev: dev, srv: New(g, "test")}
}

type client struct {
	cs       *sdk.ClientSession
	mu       sync.Mutex
	progress []string
	done     chan error
}

// connect starts an MCP session over the SDK's in-memory transport.
func (h *harness) connect(t *testing.T, name string) *client {
	t.Helper()
	st, ct := sdk.NewInMemoryTransports()
	c := &client{done: make(chan error, 1)}
	go func() { c.done <- h.srv.Serve(context.Background(), st) }()
	cl := sdk.NewClient(&sdk.Implementation{Name: name, Version: "1.0"}, &sdk.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *sdk.ProgressNotificationClientRequest) {
			c.mu.Lock()
			c.progress = append(c.progress, req.Params.Message)
			c.mu.Unlock()
		},
	})
	cs, err := cl.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.cs = cs
	t.Cleanup(func() { cs.Close() })
	return c
}

func (c *client) call(t *testing.T, tool string, args map[string]any) (string, bool) {
	t.Helper()
	return c.callWith(t, &sdk.CallToolParams{Name: tool, Arguments: args})
}

func (c *client) callWith(t *testing.T, p *sdk.CallToolParams) (string, bool) {
	t.Helper()
	res, err := c.cs.CallTool(context.Background(), p)
	if err != nil {
		t.Fatalf("%s: %v", p.Name, err)
	}
	var texts []string
	for _, ct := range res.Content {
		if tc, ok := ct.(*sdk.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}
	return strings.Join(texts, "\n"), res.IsError
}

var refRe = regexp.MustCompile(`\[(\d+)\] \w+ "([^"]*)"`)

// refOf finds the ref of a label in a result's text.
func refOf(t *testing.T, text, label string) int {
	t.Helper()
	for _, m := range refRe.FindAllStringSubmatch(text, -1) {
		if m[2] == label {
			n, _ := strconv.Atoi(m[1])
			return n
		}
	}
	t.Fatalf("%q not in:\n%s", label, text)
	return 0
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func TestToolList(t *testing.T) {
	h := newHarness(t)
	c := h.connect(t, "claude-code")
	res, err := c.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"phone_connect", "phone_release", "phone_observe", "phone_tap", "phone_type", "phone_swipe", "phone_key",
		"phone_launch", "phone_apps", "phone_batch", "phone_wait_for", "phone_files", "phone_request_approval", "phone_handoff"}
	got := map[string]*sdk.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	if len(got) != len(want) {
		t.Fatalf("%d tools, want %d", len(got), len(want))
	}
	for _, name := range want {
		tool := got[name]
		if tool == nil || tool.Description == "" {
			t.Fatalf("tool %s missing or undocumented", name)
		}
	}
	schema, _ := json.Marshal(got["phone_key"].InputSchema)
	for _, s := range []string{`"enum"`, `"recents"`, `"approval_id"`, `"intent"`, `"device"`} {
		if !strings.Contains(string(schema), s) {
			t.Fatalf("phone_key schema misses %s: %s", s, schema)
		}
	}
	schema, _ = json.Marshal(got["phone_tap"].InputSchema)
	if strings.Contains(string(schema), `"null"`) {
		t.Fatalf("phone_tap schema advertises null types: %s", schema)
	}
	schema, _ = json.Marshal(got["phone_batch"].InputSchema)
	if !strings.Contains(string(schema), `"maxItems":50`) || !strings.Contains(string(schema), `"launch"`) {
		t.Fatalf("phone_batch schema: %s", schema)
	}
	if c.cs.InitializeResult().Instructions == "" {
		t.Fatal("no server instructions")
	}
}

func TestFlowWithApproval(t *testing.T) {
	h := newHarness(t)
	c := h.connect(t, "claude-code")
	text, isErr := c.call(t, "phone_connect", map[string]any{"task": "Reorder last week's lunch"})
	if isErr || !strings.HasPrefix(text, "settled · Pixel 9") || !strings.Contains(text, "screen 1080x2400") {
		t.Fatalf("connect:\n%s", text)
	}
	if tasks := h.g.Tasks(); len(tasks) != 1 || tasks[0].Agent != "Claude Code" {
		t.Fatalf("agent name: %+v", tasks)
	}
	text, _ = c.call(t, "phone_launch", map[string]any{"app": "Dinely"})
	text, _ = c.call(t, "phone_tap", map[string]any{"ref": refOf(t, text, "Orders")})
	text, _ = c.call(t, "phone_tap", map[string]any{"ref": refOf(t, text, "Reorder · Chicken rice bowl")})
	if !strings.Contains(text, `toast: "Added to cart"`) || !strings.Contains(text, `+ [`) {
		t.Fatalf("diff after tap:\n%s", text)
	}
	text, _ = c.call(t, "phone_tap", map[string]any{"ref": refOf(t, text, "Cart · 1")})
	place := refOf(t, text, "Place order · $12.80")

	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if needs := h.g.Needs(); len(needs) > 0 {
				h.g.Decide(needs[0].ID, gateway.AllowOnce, "ok")
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	p := &sdk.CallToolParams{Name: "phone_tap", Arguments: map[string]any{"ref": place, "intent": "Pay $12.80 for lunch"}}
	p.SetProgressToken("tap-1")
	text, isErr = c.callWith(t, p)
	if isErr || !strings.HasPrefix(text, "settled") || !strings.Contains(text, `note: "ok"`) || !strings.Contains(text, "Order placed") {
		t.Fatalf("approved tap:\n%s", text)
	}
	// The client handles notifications on a goroutine of their own, which
	// may run after the call's result came in.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		c.mu.Lock()
		progress := append([]string(nil), c.progress...)
		c.mu.Unlock()
		if len(progress) > 0 && strings.Contains(progress[0], "Waiting for the person") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no progress notification: %q", progress)
		}
	}

	text, _ = c.call(t, "phone_release", map[string]any{"outcome": "done", "summary": "Ordered lunch."})
	if !strings.HasPrefix(text, "settled") {
		t.Fatalf("release:\n%s", text)
	}
}

func TestPendingAndErrors(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) { o.MaxBlock = 30 * time.Millisecond })
	c := h.connect(t, "codex")
	text, _ := c.call(t, "phone_connect", map[string]any{"task": "Lunch"})
	h.dev.Show("cart")
	text, _ = c.call(t, "phone_observe", nil)
	place := refOf(t, text, "Place order · $12.80")
	text, isErr := c.call(t, "phone_tap", map[string]any{"ref": place})
	if isErr || !strings.HasPrefix(text, "approval_pending · Pixel 9 · approval_id: ap_") || !strings.Contains(text, "rule: Payments") {
		t.Fatalf("pending:\n%s", text)
	}
	id := regexp.MustCompile(`approval_id: (ap_\w+)`).FindStringSubmatch(text)[1]
	h.g.Decide(id, gateway.Deny, "not today")
	text, _ = c.call(t, "phone_tap", map[string]any{"ref": place, "approval_id": id})
	if !strings.HasPrefix(text, "approval_denied") || !strings.Contains(text, `note: "not today"`) {
		t.Fatalf("denied:\n%s", text)
	}

	// A second agent gets lease_held, as an error result.
	other := h.connect(t, "cursor")
	text, isErr = other.call(t, "phone_connect", map[string]any{"task": "Something"})
	if !isErr || !strings.HasPrefix(text, "error: lease_held") || !strings.Contains(text, "Codex · Lunch") {
		t.Fatalf("lease_held:\n%s", text)
	}
	// Bad arguments are caught.
	if text, isErr = c.call(t, "phone_tap", map[string]any{"x": 3}); !isErr || !strings.Contains(text, "x and y") {
		t.Fatalf("x without y:\n%s", text)
	}
	if text, isErr = c.call(t, "phone_key", map[string]any{"key": "power"}); !isErr {
		t.Fatalf("power key accepted:\n%s", text)
	}
	if text, isErr = c.call(t, "phone_files", nil); !isErr || !strings.HasPrefix(text, "error: unsupported") {
		t.Fatalf("phone_files:\n%s", text)
	}
	// Typing into a password field is refused.
	h.dev.Show("account")
	text, _ = c.call(t, "phone_observe", nil)
	text, _ = c.call(t, "phone_type", map[string]any{"ref": refOf(t, text, "Password"), "text": "hunter2"})
	if !strings.HasPrefix(text, "blocked_by_policy") || !strings.Contains(text, "rule: credentials") {
		t.Fatalf("password:\n%s", text)
	}
}

func TestSessionCloseReleasesLease(t *testing.T) {
	h := newHarness(t)
	c := h.connect(t, "claude-code")
	c.call(t, "phone_connect", map[string]any{"task": "Lunch"})
	c.cs.Close()
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after the client closed")
	}
	other := h.connect(t, "codex")
	if text, isErr := other.call(t, "phone_connect", map[string]any{"task": "Next"}); isErr {
		t.Fatalf("lease not released:\n%s", text)
	}
}

func TestBatchAndScreenshot(t *testing.T) {
	h := newHarness(t)
	c := h.connect(t, "claude-code")
	text, _ := c.call(t, "phone_connect", map[string]any{"task": "Calc"})
	text, _ = c.call(t, "phone_batch", map[string]any{"steps": []map[string]any{
		{"tool": "launch", "app": "Dinely"},
		{"tool": "phone_key", "key": "back"},
		{"tool": "key", "key": "home"},
	}})
	if !strings.HasPrefix(text, "settled") || !strings.Contains(text, "step 3: settled") {
		t.Fatalf("batch:\n%s", text)
	}
	res, err := c.cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "phone_observe", Arguments: map[string]any{"screenshot": true}})
	if err != nil {
		t.Fatal(err)
	}
	var img *sdk.ImageContent
	for _, ct := range res.Content {
		if ic, ok := ct.(*sdk.ImageContent); ok {
			img = ic
		}
	}
	if img == nil || img.MIMEType != "image/jpeg" || len(img.Data) < 100 {
		t.Fatalf("no screenshot: %+v", res.Content)
	}
}

func TestHandoffOverMCP(t *testing.T) {
	h := newHarness(t)
	c := h.connect(t, "claude-code")
	c.call(t, "phone_connect", map[string]any{"task": "Sign in"})
	go func() {
		for i := 0; i < 1000; i++ {
			for _, n := range h.g.Needs() {
				if n.Kind == gateway.NeedHandoff {
					h.g.TakeOver("pixel")
					h.g.HandBack("pixel", "signed in for you")
					return
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	text, isErr := c.call(t, "phone_handoff", map[string]any{"reason": "Please sign in"})
	if isErr || !strings.HasPrefix(text, "settled") || !strings.Contains(text, "signed in for you") {
		t.Fatalf("handoff:\n%s", text)
	}
}

func TestDisplayName(t *testing.T) {
	for in, want := range map[string]string{"claude-code": "Claude Code", "codex-mcp-client": "Codex", "my-agent": "My Agent", "": "Agent"} {
		if got := DisplayName(in, ""); got != want {
			t.Errorf("DisplayName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := DisplayName("x-client", "X Client Pro"); got != "X Client Pro" {
		t.Errorf("title not used: %q", got)
	}
}

func TestPhoneConnectApprovalContinuation(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) {
		o.TrustNewAgents = false
		o.MaxBlock = 150 * time.Millisecond
		o.ApprovalTimeout = 3 * time.Second
	})
	c := h.connect(t, "claude-code")
	listed, err := c.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var connectSchema []byte
	for _, tool := range listed.Tools {
		if tool.Name == "phone_connect" {
			connectSchema, _ = json.Marshal(tool.InputSchema)
		}
	}
	var parsed struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties json.RawMessage            `json:"additionalProperties"`
	}
	if err := json.Unmarshal(connectSchema, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed.Properties["approval_id"]; !ok {
		t.Errorf("phone_connect schema omits approval_id: %s", connectSchema)
	}
	if string(parsed.AdditionalProperties) != "false" {
		t.Errorf("additionalProperties = %s, want false", parsed.AdditionalProperties)
	}
	if strings.Join(parsed.Required, ",") != "task" {
		t.Errorf("required = %v, want [task]", parsed.Required)
	}

	args := map[string]any{"device": "pixel", "task": "Reorder last week's lunch", "apps": []string{"Dinely"}, "read_only": true}
	text, isErr := c.call(t, "phone_connect", args)
	if isErr || !strings.HasPrefix(text, "approval_pending") || !strings.Contains(text, "rule: New agent") {
		t.Fatalf("first connect:\n%s", text)
	}
	id := regexp.MustCompile(`approval_id: (ap_\w+)`).FindStringSubmatch(text)
	if id == nil {
		t.Fatalf("no approval id:\n%s", text)
	}
	assertNoPhoneControl(t, h, "Claude Code")

	again := map[string]any{"device": "pixel", "task": "Reorder last week's lunch", "apps": []string{"Dinely"}, "read_only": true, "approval_id": id[1]}
	text, isErr = c.call(t, "phone_connect", again)
	if isErr || !strings.HasPrefix(text, "approval_pending") || !strings.Contains(text, id[1]) {
		t.Fatalf("continuation:\n%s", text)
	}
	assertNoPhoneControl(t, h, "Claude Code")

	wide := map[string]any{"device": "pixel", "task": "Reorder last week's lunch", "apps": []string{"Dinely", "Chatter"}, "read_only": false, "approval_id": id[1]}
	text, isErr = c.call(t, "phone_connect", wide)
	if !isErr || !strings.Contains(text, "error: invalid_argument") {
		t.Fatalf("widened continuation:\n%s", text)
	}
	assertNoPhoneControl(t, h, "Claude Code")
	if needs := h.g.Needs(); len(needs) != 1 || needs[0].ID != id[1] {
		t.Fatalf("original approval consumed: %+v", needs)
	}

	other := h.connect(t, "codex")
	stolen := map[string]any{"device": "pixel", "task": "Reorder last week's lunch", "apps": []string{"Dinely"}, "read_only": true, "approval_id": id[1]}
	text, isErr = other.call(t, "phone_connect", stolen)
	if !isErr || !strings.Contains(text, "error: invalid_argument") {
		t.Fatalf("other agent:\n%s", text)
	}
	assertNoPhoneControl(t, h, "Codex")
	if needs := h.g.Needs(); len(needs) != 1 || needs[0].ID != id[1] {
		t.Fatalf("cross-agent call consumed the approval: %+v", needs)
	}

	if err := h.g.Decide(id[1], gateway.AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	text, isErr = c.call(t, "phone_connect", again)
	if isErr || !strings.HasPrefix(text, "settled") || !strings.Contains(text, "read-only") {
		t.Fatalf("after the person allowed:\n%s", text)
	}
	tasks := h.g.Tasks()
	if len(tasks) != 1 || !tasks[0].ReadOnly || tasks[0].Agent != "Claude Code" {
		t.Fatalf("task: %+v", tasks)
	}
	text, isErr = c.call(t, "phone_launch", map[string]any{"app": "Chatter"})
	if !isErr || !strings.Contains(text, "error: scope_denied") {
		t.Fatalf("app outside the bound list:\n%s", text)
	}
	for _, in := range h.dev.Inputs() {
		if in.Kind == "launch" && strings.Contains(in.Text, "Chatter") {
			t.Fatalf("refused launch reached the phone: %+v", h.dev.Inputs())
		}
	}
}

func assertNoPhoneControl(t *testing.T, h *harness, agent string) {
	t.Helper()
	if tasks := h.g.Tasks(); len(tasks) != 0 {
		t.Fatalf("phone leased: %+v", tasks)
	}
	for _, d := range h.g.Devices() {
		if d.Status == gateway.DeviceWorking {
			t.Fatalf("device in use: %+v", d)
		}
	}
	agents, err := h.g.Agents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range agents {
		if a.Name == agent && a.Approved {
			t.Fatalf("%s approved before a decision: %+v", agent, a)
		}
	}
	for _, in := range h.dev.Inputs() {
		if in.Kind != "" {
			t.Fatalf("phone input before approval: %+v", h.dev.Inputs())
		}
	}
}

func TestStatusLine(t *testing.T) {
	m := true
	got := StatusLine(&gateway.Result{Status: gateway.StatusPending, DeviceName: "Pixel 9", ApprovalID: "ap_1", Rule: "Payments", Matched: &m})
	if got != "approval_pending · Pixel 9 · approval_id: ap_1 · rule: Payments · matched: true" {
		t.Fatal(got)
	}
	if got := firstLine(StatusLine(&gateway.Result{Error: gateway.ErrLeaseHeld, Holder: "Codex · Lunch"})); got != "error: lease_held · holder: Codex · Lunch" {
		t.Fatal(got)
	}
	if got := StatusLine(&gateway.Result{Status: gateway.StatusNeedsSetup, DeviceName: "iPhone 16", Setup: "trust"}); got != "device_needs_setup · iPhone 16 · setup: trust" {
		t.Fatal(got)
	}
}
