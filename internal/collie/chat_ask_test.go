package collie

import (
	"encoding/json"
	"testing"
)

func TestAskOf(t *testing.T) {
	m := askOf("AskUserQuestion", json.RawMessage(`{"questions":[{"question":"Which?","header":"H","options":[{"label":"A"},{"label":"B"}],"multiSelect":false}]}`))
	if m == nil || m.Kind != "ask" || m.Text != "Which?" || len(m.Options) != 2 {
		t.Fatalf("AskUserQuestion: %+v", m)
	}
	m = askOf("mcp__ccd_turn__wrap_up", json.RawMessage(`{"recap":"R","ask":"Go?","actions":[{"label":"Yes"},{"action":"commit","label":"x"}]}`))
	if m == nil || m.Name != "wrap_up" || m.Text != "Go?" || m.Input != "R" || len(m.Options) != 1 || m.Options[0] != "Yes" {
		t.Fatalf("wrap_up: %+v", m)
	}
	if askOf("mcp__ccd_turn__wrap_up", json.RawMessage(`{"recap":"R"}`)) != nil {
		t.Fatal("wrap_up without a question is not an ask")
	}
}

func TestQueuedCommand(t *testing.T) {
	tr := newTranscript("", "claude")
	if !tr.claudeLine([]byte(`{"type":"attachment","attachment":{"type":"queued_command","prompt":"还有些消息看不到","origin":{"kind":"human"}}}`)) {
		t.Fatal("human queued command not taken")
	}
	tr.claudeLine([]byte(`{"type":"attachment","attachment":{"type":"queued_command","prompt":[{"type":"image","source":{}},{"type":"text","text":"看图"}],"origin":{"kind":"human"}}}`))
	if tr.claudeLine([]byte(`{"type":"attachment","attachment":{"type":"queued_command","prompt":"x","origin":{"kind":"task-notification"}}}`)) {
		t.Fatal("non-human queued command shown")
	}
	if len(tr.msgs) != 2 || tr.msgs[0].Text != "还有些消息看不到" || tr.msgs[1].Text != "[image]\n看图" {
		t.Fatalf("%+v %+v", tr.msgs[0], tr.msgs[len(tr.msgs)-1])
	}
}
