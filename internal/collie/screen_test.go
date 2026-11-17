package collie

import (
	"strings"
	"testing"
)

// Real screens from claude 2.1 on herdr 0.8, which reports both as "idle".
const idleClaude = `           Claude Code v2.1.325
 ▐▛███▛█   llmbox-2/seed-evolving[1m]
▝▜██████▀  API Usage Billing
 ▝▝   ▝▝   /…/home/u/.collie/router

  ⎿  SessionStart:startup hook error
  ⎿  Failed with non-blocking status code:
     node:internal/modules/cjs/loader:1433

● 已回复：自我介绍 + hostname（n37-125-077），两条测试
  消息内容相同，合并为一条回复。

✻ Worked for 14s · done 5:11 AM

──────────────────────────────────────────────────────
❯ Try "edit <filepath> to..."
──────────────────────────────────────────────────────
  ⏸ manual mode on · ? for shortcuts
`

const trustDialog = ` Accessing workspace:
 /data00/home/u/.collie
 Quick safety check: Is this a project you created or
 one you trust? (Like your own code, a well-known
 open source project, or work from your team). If
 not, take a moment to review what's in this folder
 first.
 Claude Code'll be able to read, edit, and execute
 files here.
 Security guide
 ❯ 1. Yes, I trust this folder
   2. No, exit
 Enter to confirm · Esc to cancel
`

const wrappedMenu = `─────────────────────────────────────────────────────
 Read file
 Symlink target: /data00/home/u/.collie/up
 loads/20261117/0d224b-number.png

╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 Read(/home/u/.collie/uploads/20261117/0d2
 24b-number.png)
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, allow reading from
      /home/u/.collie/uploads/20261117 and
      /data00/home/u/.collie/uploads/2026
      1117 during this session
   3. No

 Esc to cancel · Tab to amend`

func TestWrappedChoices(t *testing.T) {
	a := parseApproval(wrappedMenu)
	if len(a.Choices) != 3 || !a.Choices[0].Current || !strings.HasPrefix(a.Choices[1].Label, "Yes, allow reading from /home/u") || a.Choices[2].Label != "No" {
		t.Fatalf("choices: %+v", a.Choices)
	}
	if !strings.HasSuffix(a.Question, "Do you want to proceed?") || !strings.Contains(a.Question, "Read file") {
		t.Fatalf("question: %q", a.Question)
	}
}

func TestScreenAsks(t *testing.T) {
	if screenAsks(idleClaude) {
		t.Error("idle prompt taken for a question")
	}
	if !screenAsks(trustDialog) {
		t.Error("trust dialog not seen")
	}
	if !screenAsks(cursorTrust) {
		t.Error("cursor-style trust dialog not seen")
	}
}

const cursorTrust = ` Claude Code'll be able to read, edit, and execute
 files here.
 Security guide
 ❯ No, exit
   Yes, I trust this folder
 Enter to confirm · Esc to cancel
`

const trustFull = `u@host:~/collie-card-test$ claude

──────────────────────────────────────────────────────
 Accessing workspace:

 /data00/home/u/collie-card-test

 Quick safety check: Is this a project you created or
 one you trust? (Like your own code, a well-known
 open source project, or work from your team). If
 not, take a moment to review what's in this folder
 first.

 Claude Code'll be able to read, edit, and execute
 files here.

 Security guide

 ❯ No, exit
   Yes, I trust this folder

 Enter to confirm · Esc to cancel
`

func TestApprovalKeepsParagraphs(t *testing.T) {
	a := parseApproval(trustFull)
	if len(a.Choices) != 2 {
		t.Fatalf("choices: %+v", a.Choices)
	}
	if strings.Contains(a.Question, "$ claude") {
		t.Errorf("question starts above the dialog:\n%s", a.Question)
	}
	if !strings.HasPrefix(a.Question, " Accessing workspace:\n\n") || !strings.Contains(a.Question, "\n\n Quick safety check:") {
		t.Errorf("paragraphs lost:\n%q", a.Question)
	}
}
