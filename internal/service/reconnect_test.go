package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"bqbot/internal/driver"
	"bqbot/internal/harness"
)

func TestReconnectRestoresCompletedReplyAndClearsApproval(t *testing.T) {
	d := &fakeDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		decision, err := in.Approve(ctx, driver.ApprovalRequest{ID: "permission", Kind: "tool", Tool: "write", Input: json.RawMessage(`{"path":"note.txt"}`)})
		if err != nil {
			return err
		}
		emit(driver.Event{Kind: driver.EvText, Text: decision.Outcome})
		return nil
	}}
	_, client, h := newTestService(t, d)
	b := mustSave(t, client, "ada")
	updates, unsubscribe := h.Subscribe()
	defer unsubscribe()
	if _, err := client.Send(b.ID, "write a note", nil, "queue"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		select {
		case u := <-updates:
			if u.Approval != nil {
				goto pending
			}
		case <-ctx.Done():
			t.Fatal("approval not pending")
		}
	}
pending:
	snapshot, err := client.Snapshot()
	if err != nil || len(snapshot) != 1 || len(snapshot[0].Approvals) != 1 {
		t.Fatalf("pending snapshot %+v, %v", snapshot, err)
	}
	// The client's event stream is disconnected while another client approves.
	if err := client.Approve(b.ID, "permission", driver.ApprovalDecision{Outcome: "allow"}); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case u := <-updates:
			if u.Done {
				goto complete
			}
		case <-ctx.Done():
			t.Fatal("turn did not complete")
		}
	}
complete:
	var recovered harness.Update
	reconnect, stop := context.WithCancel(ctx)
	defer stop()
	client.Watch(reconnect, func(u harness.Update) {
		if u.Sync {
			recovered = u
			stop()
		}
	})
	if recovered.Busy || len(recovered.Approvals) != 0 || len(recovered.Messages) != 2 || recovered.Messages[1].Text != "allow" {
		t.Fatalf("recovered state %+v", recovered)
	}
}
