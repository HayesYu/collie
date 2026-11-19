package collie

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Notifications can also go out as a Feishu/Lark message from the hub's own
// lark-cli: its bot writes to whoever is logged in to it. No lark-cli on the
// hub, or no one logged in, and nothing is sent.

type larkTarget struct {
	mu      sync.Mutex
	bin     string
	openID  string
	who     string
	err     string
	checked time.Time
}

var lark larkTarget

// get says who lark-cli would write to, looking again every 10 minutes (or
// at once when asked), so installing or logging in later just works.
func (l *larkTarget) get(fresh bool) (bin, openID, who, problem string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if fresh || time.Since(l.checked) > 10*time.Minute {
		l.checked = time.Now()
		l.bin, l.openID, l.who, l.err = larkLookup()
	}
	return l.bin, l.openID, l.who, l.err
}

func larkLookup() (bin, openID, who, problem string) {
	home, _ := os.UserHomeDir()
	if bin = findBin("lark-cli", home); bin == "" {
		return "", "", "", "hub 上没有 lark-cli"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "auth", "status").Output()
	var st struct {
		Identities struct {
			Bot struct {
				Available bool `json:"available"`
			} `json:"bot"`
			User struct {
				OpenID   string `json:"openId"`
				UserName string `json:"userName"`
			} `json:"user"`
		} `json:"identities"`
	}
	if err != nil || json.Unmarshal(out, &st) != nil {
		return bin, "", "", "lark-cli 读不到登录状态"
	}
	if !st.Identities.Bot.Available {
		return bin, "", "", "lark-cli 的机器人身份不可用"
	}
	if st.Identities.User.OpenID == "" {
		return bin, "", "", "lark-cli 没有登录用户（lark-cli auth login）"
	}
	return bin, st.Identities.User.OpenID, st.Identities.User.UserName, ""
}

// larkSend writes text to the logged-in user, as the bot (a message from
// yourself would not ring).
func larkSend(text string) error {
	bin, openID, _, problem := lark.get(false)
	if problem != "" {
		return errors.New(problem)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "im", "+messages-send", "--as", "bot", "--user-id", openID, "--text", text).CombinedOutput()
	if err != nil {
		lark.get(true) // logged out, or moved: look again next time
		return errors.New(clip(string(out), 300))
	}
	return nil
}
