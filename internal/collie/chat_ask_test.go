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
