package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/approval"
	"github.com/DIYgod/pinky/internal/demo"
)

func TestAgentApprovalContinuationBinding(t *testing.T) {
	g, dev := newTestGateway(t, func(o *Options) {
		o.TrustNewAgents = false
		o.MaxBlock = 40 * time.Millisecond
		o.ApprovalTimeout = 3 * time.Second
	})
	s := g.OpenSession(agent)
	t.Cleanup(s.Close)
	req := ConnectRequest{Call: Call{Device: "pixel"}, Task: "Lunch", Apps: []string{"Dinely"}, ReadOnly: true}
	res := s.Connect(context.Background(), req)
	mustStatus(t, res, StatusPending)
	if res.Rule != "New agent" || res.ApprovalID == "" {
		t.Fatalf("pending: %+v", res)
	}
	assertAgentHasNoControl(t, g, dev, agent.Name)
	id := res.ApprovalID
	if n := waitNeed(t, g); n.ID != id || n.DeviceID != "" || !n.ReadOnly {
		t.Fatalf("card: %+v", n)
	}

	req.Call.ApprovalID = id
	same := req
	mismatches := []ConnectRequest{
		{Call: Call{Device: "other-phone", ApprovalID: id}, Task: "Lunch", Apps: []string{"Dinely"}, ReadOnly: true},
		{Call: Call{Device: "pixel", ApprovalID: id}, Task: "Dinner", Apps: []string{"Dinely"}, ReadOnly: true},
		{Call: Call{Device: "pixel", ApprovalID: id}, Task: "Lunch", Apps: []string{"Dinely", "Chatter"}, ReadOnly: true},
		{Call: Call{Device: "pixel", ApprovalID: id}, Task: "Lunch", Apps: []string{"Dinely"}, ReadOnly: false},
	}
	for _, bad := range mismatches {
		got := s.Connect(context.Background(), bad)
		if got.Error != ErrInvalidArgument || got.Status == StatusSettled {
			t.Fatalf("mismatch %+v: %+v", bad, got)
		}
		assertAgentHasNoControl(t, g, dev, agent.Name)
	}
	if needs := g.Needs(); len(needs) != 1 || needs[0].ID != id {
		t.Fatalf("mismatch consumed the approval: %+v", needs)
	}

	other := g.OpenSession(AgentInfo{Name: "Codex", Client: "codex 1.0"})
	t.Cleanup(other.Close)
	if got := other.Connect(context.Background(), same); got.Error != ErrInvalidArgument || got.Status == StatusSettled {
		t.Fatalf("other session: %+v", got)
	}
	assertAgentHasNoControl(t, g, dev, "Codex")
	if needs := g.Needs(); len(needs) != 1 || needs[0].ID != id {
		t.Fatalf("other session consumed the approval: %+v", needs)
	}

	again := s.Connect(context.Background(), same)
	mustStatus(t, again, StatusPending)
	if again.ApprovalID != id {
		t.Fatalf("continuation id %q, want %s", again.ApprovalID, id)
	}
	assertAgentHasNoControl(t, g, dev, agent.Name)

	if err := g.Decide(id, AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	ok := s.Connect(context.Background(), same)
	mustStatus(t, ok, StatusSettled)
	if !strings.Contains(ok.Message, "read-only") {
		t.Fatalf("settled: %s", ok.Message)
	}
	tasks := g.Tasks()
	if len(tasks) != 1 || !tasks[0].ReadOnly || tasks[0].Title != "Lunch" {
		t.Fatalf("task: %+v", tasks)
	}
	if res := s.Act(context.Background(), Call{}, Action{Tool: "launch", App: "Chatter"}); res.Error != ErrScopeDenied {
		t.Fatalf("widened apps after approval: %+v", res)
	}
	for _, in := range dev.Inputs() {
		if in.Kind == "launch" && strings.Contains(in.Text, "Chatter") {
			t.Fatalf("refused launch reached the phone: %+v", dev.Inputs())
		}
	}

	s.Close()
	waitUntil(t, "the lease to end", func() bool { return g.Devices()[0].Status == DeviceIdle })
	if needs := g.Needs(); len(needs) != 0 {
		t.Fatalf("approval left open after the session closed: %+v", needs)
	}
}

func TestAgentApprovalDenyTimeoutAndCancelDoNothing(t *testing.T) {
	t.Run("deny", func(t *testing.T) {
		g, dev := newTestGateway(t, func(o *Options) {
			o.TrustNewAgents = false
			o.MaxBlock = 40 * time.Millisecond
		})
		s := g.OpenSession(agent)
		t.Cleanup(s.Close)
		res := s.Connect(context.Background(), ConnectRequest{Task: "Lunch"})
		mustStatus(t, res, StatusPending)
		if err := g.Decide(res.ApprovalID, Deny, "no"); err != nil {
			t.Fatal(err)
		}
		got := s.Connect(context.Background(), ConnectRequest{Call: Call{ApprovalID: res.ApprovalID}, Task: "Lunch"})
		mustStatus(t, got, StatusDenied)
		if !strings.Contains(got.Note, "no") {
			t.Fatalf("note: %+v", got)
		}
		assertAgentHasNoControl(t, g, dev, agent.Name)
		if err := g.Decide(res.ApprovalID, AllowOnce, ""); err == nil {
			t.Fatal("a denied approval accepted another decision")
		}
		revived := s.Connect(context.Background(), ConnectRequest{Call: Call{ApprovalID: res.ApprovalID}, Task: "Lunch"})
		if revived.Status == StatusSettled || revived.Error == "" {
			t.Fatalf("denied id revived: %+v", revived)
		}
		assertAgentHasNoControl(t, g, dev, agent.Name)
	})

	t.Run("timeout", func(t *testing.T) {
		g, dev := newTestGateway(t, func(o *Options) {
			o.TrustNewAgents = false
			o.ApprovalTimeout = 2 * time.Second
			o.MaxBlock = 30 * time.Millisecond
		})
		s := g.OpenSession(agent)
		t.Cleanup(s.Close)
		res := s.Connect(context.Background(), ConnectRequest{Task: "Lunch"})
		mustStatus(t, res, StatusPending)
		id := res.ApprovalID
		waitUntil(t, "the approval to time out", func() bool { return len(g.Needs()) == 0 })
		got := s.Connect(context.Background(), ConnectRequest{Call: Call{ApprovalID: id}, Task: "Lunch"})
		mustStatus(t, got, StatusDenied)
		if !strings.Contains(got.Note, "No answer") {
			t.Fatalf("timeout: %+v", got)
		}
		assertAgentHasNoControl(t, g, dev, agent.Name)
		again := s.Connect(context.Background(), ConnectRequest{Call: Call{ApprovalID: id}, Task: "Lunch"})
		if again.Status == StatusSettled || again.Status == StatusPending || again.Error == "" {
			t.Fatalf("timed-out id replayed: %+v", again)
		}
		assertAgentHasNoControl(t, g, dev, agent.Name)
	})

	t.Run("cancel", func(t *testing.T) {
		g, dev := newTestGateway(t, func(o *Options) {
			o.TrustNewAgents = false
			o.MaxBlock = time.Second
			o.ApprovalTimeout = 5 * time.Second
		})
		s := g.OpenSession(agent)
		t.Cleanup(s.Close)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan *Result, 1)
		go func() {
			done <- s.Connect(ctx, ConnectRequest{Task: "Lunch"})
		}()
		n := waitNeed(t, g)
		cancel()
		res := <-done
		if res.Error != ErrInternal || res.Status == StatusSettled {
			t.Fatalf("cancel: %+v", res)
		}
		assertAgentHasNoControl(t, g, dev, agent.Name)
		if needs := g.Needs(); len(needs) != 0 {
			t.Fatalf("cancelled approval still open: %+v", needs)
		}
		if err := g.Decide(n.ID, AllowOnce, ""); err == nil {
			t.Fatal("a cancelled approval accepted a decision")
		}
		again := s.Connect(context.Background(), ConnectRequest{Call: Call{ApprovalID: n.ID}, Task: "Lunch"})
		if again.Status == StatusSettled || again.Error == "" && again.Status != StatusDenied {
			t.Fatalf("cancelled id revived: %+v", again)
		}
		assertAgentHasNoControl(t, g, dev, agent.Name)
	})

	t.Run("forged", func(t *testing.T) {
		g, dev := newTestGateway(t, func(o *Options) {
			o.TrustNewAgents = false
			o.MaxBlock = 40 * time.Millisecond
		})
		s := g.OpenSession(agent)
		t.Cleanup(s.Close)
		got := s.Connect(context.Background(), ConnectRequest{Call: Call{ApprovalID: "ap_forged"}, Task: "Lunch"})
		if got.Error != ErrInvalidArgument || got.Status == StatusSettled {
			t.Fatalf("forged id: %+v", got)
		}
		assertAgentHasNoControl(t, g, dev, agent.Name)
		if _, err := g.approvals.Get("ap_forged"); err != approval.ErrUnknown {
			t.Fatalf("forged id created an approval: %v", err)
		}
	})
}

func assertAgentHasNoControl(t *testing.T, g *Gateway, dev *demo.Device, name string) {
	t.Helper()
	if tasks := g.Tasks(); len(tasks) != 0 {
		t.Fatalf("task exists: %+v", tasks)
	}
	for _, d := range g.Devices() {
		if d.Status == DeviceWorking {
			t.Fatalf("device in use: %+v", d)
		}
	}
	agents, err := g.Agents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range agents {
		if a.Name == name && a.Approved {
			t.Fatalf("%s is approved: %+v", name, a)
		}
	}
	if ins := dev.Inputs(); len(ins) != 0 {
		t.Fatalf("phone input: %+v", ins)
	}
}

func TestAgentConnectKeyDoesNotMergeDifferentRequests(t *testing.T) {
	var fromJSON string
	if err := json.Unmarshal([]byte(`"a\u0000b\u001fc"`), &fromJSON); err != nil {
		t.Fatal(err)
	}
	if fromJSON != "a\x00b\x1fc" {
		t.Fatalf("JSON lost a control character: %q", fromJSON)
	}

	base := AgentInfo{Name: "Claude Code", Client: "claude-code 1.0"}
	key := func(name, client, device, task string, apps []string, readOnly bool) string {
		return agentConnectKey(AgentInfo{Name: name, Client: client}, ConnectRequest{
			Call: Call{Device: device}, Task: task, Apps: apps, ReadOnly: readOnly,
		})
	}
	same := []struct{ a, b string }{
		{key(base.Name, base.Client, " pixel ", " Lunch ", nil, false), key(base.Name, base.Client, "pixel", "Lunch", []string{}, false)},
		{key(base.Name, base.Client, "pixel", "Lunch", []string{"Dinely"}, true), key(base.Name, base.Client, "pixel", "Lunch", []string{"Dinely"}, true)},
	}
	for _, c := range same {
		if c.a != c.b {
			t.Fatalf("normalization changed:\n%q\n%q", c.a, c.b)
		}
	}
	different := []struct{ a, b string }{
		{key(base.Name, base.Client, "pixel\x00Lunch", "x", nil, false), key(base.Name, base.Client, "pixel", "Lunch\x00x", nil, false)},
		{key(base.Name, base.Client, "pixel", "Lunch", []string{"a\x1fb"}, false), key(base.Name, base.Client, "pixel", "Lunch", []string{"a", "b"}, false)},
		{key(base.Name, base.Client, "pixel", "Lunch\x00false", nil, true), key(base.Name, base.Client, "pixel", "Lunch", nil, false)},
		{key("A\x00B", "C", "pixel", "Lunch", nil, false), key("A", "B\x00C", "pixel", "Lunch", nil, false)},
		{key(base.Name, base.Client, "pixel", "Lunch", []string{"Dinely", "Clock"}, false), key(base.Name, base.Client, "pixel", "Lunch", []string{"Clock", "Dinely"}, false)},
		{key(base.Name, base.Client, "pixel", "Lunch", nil, false), key(base.Name, base.Client, "pixel", "Lunch", nil, true)},
	}
	for _, c := range different {
		if c.a == c.b {
			t.Fatalf("different requests share one key:\n%q", c.a)
		}
	}
}

func TestAgentApprovalControlCharactersDoNotContinue(t *testing.T) {
	g, dev := newTestGateway(t, func(o *Options) {
		o.TrustNewAgents = false
		o.MaxBlock = 40 * time.Millisecond
		o.ApprovalTimeout = 3 * time.Second
	})
	s := g.OpenSession(agent)
	t.Cleanup(s.Close)
	res := s.Connect(context.Background(), ConnectRequest{
		Call: Call{Device: "pixel"}, Task: "Lunch", Apps: []string{"a", "b"}, ReadOnly: true,
	})
	mustStatus(t, res, StatusPending)
	id := res.ApprovalID
	got := s.Connect(context.Background(), ConnectRequest{
		Call: Call{Device: "pixel", ApprovalID: id}, Task: "Lunch", Apps: []string{"a\x1fb"}, ReadOnly: true,
	})
	if got.Error != ErrInvalidArgument || got.Status == StatusSettled {
		t.Fatalf("joined app name continued the two-app request: %+v", got)
	}
	split := s.Connect(context.Background(), ConnectRequest{
		Call: Call{Device: "pixel\x00Lunch", ApprovalID: id}, Task: "x", Apps: []string{"a", "b"}, ReadOnly: true,
	})
	if split.Error != ErrInvalidArgument || split.Status == StatusSettled {
		t.Fatalf("split device and task continued the original: %+v", split)
	}
	assertAgentHasNoControl(t, g, dev, agent.Name)
	if needs := g.Needs(); len(needs) != 1 || needs[0].ID != id {
		t.Fatalf("card consumed: %+v", needs)
	}
	again := s.Connect(context.Background(), ConnectRequest{
		Call: Call{Device: "pixel", ApprovalID: id}, Task: "Lunch", Apps: []string{"a", "b"}, ReadOnly: true,
	})
	mustStatus(t, again, StatusPending)
	if again.ApprovalID != id {
		t.Fatalf("continuation id %q", again.ApprovalID)
	}
}
