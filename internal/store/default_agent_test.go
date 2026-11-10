package store

import "testing"

func TestDefaultAgentBootstrap(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	existing := Bot{ID: "bqbot", Name: "Previously installed", Engine: "echo"}
	if err = st.SaveBot(existing); err != nil {
		t.Fatal(err)
	}
	if err = st.EnsureDefaultAgent(); err != nil {
		t.Fatal(err)
	}
	bots, err := st.Bots()
	if err != nil || len(bots) != 2 || bots[0].Name != existing.Name || bots[1].Name != "bqbot" || bots[1].ID == existing.ID || bots[1].Engine != "bqbot" || bots[1].ApprovalMode != "ask" {
		t.Fatalf("upgrade: %+v %v", bots, err)
	}
	id := bots[1].ID
	bots[1].Name = "我的助手"
	if err = st.SaveProfile(bots[1]); err != nil {
		t.Fatal(err)
	}
	if err = st.EnsureDefaultAgent(); err != nil {
		t.Fatal(err)
	}
	bots, err = st.Bots()
	if err != nil || len(bots) != 2 || bots[1].Name != "我的助手" {
		t.Fatal(bots, err)
	}
	if err = st.DeleteBot(id); err != nil {
		t.Fatal(err)
	}
	if err = st.EnsureDefaultAgent(); err != nil {
		t.Fatal(err)
	}
	bots, err = st.Bots()
	if err != nil || len(bots) != 1 || bots[0].Engine != "echo" {
		t.Fatal(bots, err)
	}
}
