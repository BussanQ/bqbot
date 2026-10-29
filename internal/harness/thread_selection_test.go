package harness

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"bqbot/internal/driver"
	"bqbot/internal/mcp"
	"bqbot/internal/store"
)

func TestAsyncHandoffExplicitNewThread(t *testing.T) {
	grants := make(chan mcp.Grant, 1)
	d := &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		data, err := os.ReadFile(in.MCP.Args[1])
		if err != nil {
			return err
		}
		var grant mcp.Grant
		if err := json.Unmarshal(data, &grant); err != nil {
			return err
		}
		grants <- grant
		<-ctx.Done()
		return ctx.Err()
	}}
	r := newRig(t, d, &driver.Echo{})
	a := r.bot("Ada", "test", "")
	b := r.bot("Bo", "echo", "")
	old, err := r.st.HandoffThread(b.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.st.AppendThread(old.ID, store.Message{ID: "old", Role: "user", Text: "previous topic"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h.Send(a, "delegate"); err != nil {
		t.Fatal(err)
	}
	grant := receive(t, grants)
	backend := &mcp.HTTPBackend{URL: grant.URL, Token: grant.Token}
	receipt, err := backend.AskBotInThread(context.Background(), "Bo", "new topic", "new")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal([]byte(receipt), &result); err != nil {
		t.Fatal(err)
	}
	if result.ThreadID == "" || result.ThreadID == old.ID {
		t.Fatalf("reused previous thread: %s", receipt)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows, err := r.st.ThreadTranscript(result.ThreadID)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("target did not reply: %+v", rows)
		}
		time.Sleep(time.Millisecond)
	}
	rows, err := r.st.ThreadTranscript(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Text != "previous topic" {
		t.Fatal("previous thread was modified")
	}
	latest, err := r.st.HandoffThread(b.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != result.ThreadID {
		t.Fatal("subsequent dispatch would resume obsolete thread")
	}
	if _, err := backend.AskBotInThread(context.Background(), "Bo", "question", "invalid"); err == nil {
		t.Fatal("invalid thread selector accepted")
	}
	r.h.Stop(a.ID)
	waitJobs(t, r.h)
}
