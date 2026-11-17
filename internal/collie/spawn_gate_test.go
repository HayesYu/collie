package collie

import "testing"

func TestMentions(t *testing.T) {
	yes := func(ok bool, what string) {
		t.Helper()
		if !ok {
			t.Errorf("should count: %s", what)
		}
	}
	no := func(ok bool, what string) {
		t.Helper()
		if ok {
			t.Errorf("should not count: %s", what)
		}
	}
	// The timer that made the router pick Claude names no agent.
	timer := "检查下 book_df、paper_df 是否有正常更新分区，references 等字段是否已成功更新"
	no(mentionsKind(timer, "claude"), "timer → claude")
	no(mentionsModel(timer, "claude-opus-5-5-medium"), "timer → opus")
	no(mentionsEffort(timer, "high"), "timer → high")

	yes(mentionsKind("用 Codex 把测试补上", "codex"), "用 Codex")
	yes(mentionsKind("让 agy 去查一下", "antigravity"), "agy")
	yes(mentionsKind("交给 Claude 吧", "claude"), "Claude")
	no(mentionsKind("用 Codex 把测试补上", "claude"), "Codex ≠ claude")

	yes(mentionsModel("这个用 opus 做", "claude-opus-5-5-medium"), "opus")
	yes(mentionsModel("换成 gpt-5.5", "gpt-5.5-codex"), "gpt-5.5")
	yes(mentionsModel("用 GPT 5.5 Codex", "gpt-5.5-codex"), "GPT 5.5 Codex")
	no(mentionsModel("用 claude 做", "claude-opus-5-5-medium"), "claude alone")
	no(mentionsModel("修一下 5.5 版本的 bug", "gpt-5.5-codex"), "a version number alone")

	yes(mentionsEffort("思考强度调高一点", "high"), "思考强度高")
	yes(mentionsEffort("effort max", "max"), "effort max")
	no(mentionsEffort("把分辨率提高", "high"), "提高 is not effort")
	no(mentionsEffort("highlight the diff", "high"), "highlight")
}

func TestGateSpawn(t *testing.T) {
	sp := &Super{st: defaultState("/tmp")}
	sp.st.Settings.WorkerKind = "antigravity"
	sp.ingestLocked("chat", mainThread, "you", "检查下 book_df 的分区", "", "", "")
	k, m, e, dropped := sp.gateSpawn("claude", "claude-opus-5-5-medium", "high")
	if k != "" || m != "" || e != "" || len(dropped) != 3 {
		t.Fatalf("unasked choices kept: %q %q %q %v", k, m, e, dropped)
	}
	sp.ingestLocked("chat", mainThread, "you", "这个用 Codex 做，思考强度高", "", "", "")
	k, _, e, dropped = sp.gateSpawn("codex", "", "high")
	if k != "codex" || e != "high" || len(dropped) != 0 {
		t.Fatalf("asked choices dropped: %q %q %v", k, e, dropped)
	}
	// Asking for the default kind needs no permission.
	if k, _, _, d := sp.gateSpawn("antigravity", "", ""); k != "antigravity" || len(d) != 0 {
		t.Fatalf("default kind dropped: %q %v", k, d)
	}
}
