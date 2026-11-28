package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebMobileNotesClipAndTextSizeAdjustContracts(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	script := `
const fs = require("fs");
const assert = require("assert");

const cssSrc = fs.readFileSync("web/app.css", "utf8");
const demoCssSrc = fs.readFileSync("docs/demo/app.css", "utf8");

for (const [name, src] of [
  ["web/app.css", cssSrc],
  ["docs/demo/app.css", demoCssSrc],
]) {
  // 1) Global iOS Safari / WebKit text-size-adjust guard on :root and html, body
  assert.match(
    src,
    /:root\s*\{[\s\S]*?-webkit-text-size-adjust:\s*100%;\s*text-size-adjust:\s*100%;/,
    name + " :root must set -webkit-text-size-adjust: 100% and text-size-adjust: 100%",
  );
  assert.ok(
    src.includes("html { background: var(--bg); }") &&
      src.includes("html, body { margin: 0; height: 100%; -webkit-text-size-adjust: 100%; text-size-adjust: 100%; }"),
    name + " html, body must enforce text-size-adjust: 100% while preserving html background contract",
  );

  // 2) Notes list / thread list / fold grid track containment (prevents implicit auto-column blowout & clipping)
  assert.match(
    src,
    /\.np-list\s*\{[^}]*display:\s*grid;\s*grid-template-columns:\s*minmax\(0,\s*1fr\);[^}]*min-width:\s*0;/,
    name + " .np-list must constrain single grid track to minmax(0, 1fr)",
  );
  assert.ok(
    src.includes(".np-list > li:not(.np-fold-more) { min-width: 0; max-width: 100%; }") &&
      src.includes(".np-list > li.np-fold-more { min-width: 0; max-width: calc(100% + 12px); }"),
    name + " .np-list items must shrink via min-width: 0 while preserving full bleed width for .np-fold-more",
  );
  assert.match(
    src,
    /\.np-th-list\s*\{[^}]*display:\s*grid;\s*grid-template-columns:\s*minmax\(0,\s*1fr\);[^}]*min-width:\s*0;/,
    name + " .np-th-list must constrain grid track to minmax(0, 1fr)",
  );
  assert.match(
    src,
    /\.np-th-sublist\s*\{[^}]*display:\s*grid;\s*grid-template-columns:\s*minmax\(0,\s*1fr\);[^}]*min-width:\s*0;/,
    name + " .np-th-sublist must constrain grid track to minmax(0, 1fr)",
  );
  assert.ok(
    src.includes(".np-th-list > :not(.np-fold-more), .np-th-sublist > * { min-width: 0; max-width: 100%; }") &&
      src.includes(".np-th-list > .np-fold-more { min-width: 0; max-width: calc(100% + 12px); }"),
    name + " thread list items must shrink within container width without narrowing .np-fold-more",
  );
  assert.match(
    src,
    /\.np-fold\s*\{[\s\S]*?grid-template-columns:\s*minmax\(0,\s*1fr\);[\s\S]*?min-width:\s*0;/,
    name + " .np-fold must constrain inline grid column to minmax(0, 1fr)",
  );
  assert.match(
    src,
    /\.np-fold\s*>\s*\.np-fold-inner\s*\{[\s\S]*?min-width:\s*0;\s*min-height:\s*0;/,
    name + " .np-fold > .np-fold-inner must set min-width: 0",
  );
  assert.match(
    src,
    /\.np-item,\s*\.np-file\s*\{[^}]*width:\s*calc\(100% \+ 12px\);\s*max-width:\s*calc\(100% \+ 12px\);\s*min-width:\s*0;/,
    name + " .np-item and .np-file must cap max-width and allow min-width: 0",
  );

  // 3) Tablet & mobile (<= 1359px) notes sheet: harmonious typography + graceful 2-line clamp + top-aligned dots
  const sheetIdx = src.indexOf("/* Narrow desktop & tablet (< 1360px):");
  assert.ok(sheetIdx > 0, name + " narrow/tablet notes media block must exist");
  const sheetCss = src.slice(sheetIdx, src.indexOf("/* Mobile (<= 760px): unified rounded bottom sheet", sheetIdx));
  assert.match(
    sheetCss,
    /\.notepane \.np-item-main\s*\{\s*align-items:\s*start;\s*\}/,
    name + " <=1359px .np-item-main must top-align bullet dot for multi-line titles",
  );
  assert.match(
    sheetCss,
    /\.notepane \.np-item-title\s*\{[\s\S]*?font-size:\s*13px;\s*line-height:\s*1\.45;[\s\S]*?white-space:\s*normal;\s*overflow-wrap:\s*anywhere;[\s\S]*?-webkit-line-clamp:\s*2;\s*line-clamp:\s*2;/,
    name + " <=1359px .np-item-title must wrap cleanly with 2-line clamp and match 13px file title scale",
  );
  assert.match(
    sheetCss,
    /\.notepane \.np-item-note\s*\{[\s\S]*?font-size:\s*12px;\s*line-height:\s*1\.45;[\s\S]*?white-space:\s*normal;\s*overflow-wrap:\s*anywhere;[\s\S]*?-webkit-line-clamp:\s*2;\s*line-clamp:\s*2;/,
    name + " <=1359px .np-item-note must wrap cleanly with 2-line clamp at 12px",
  );
  assert.match(
    sheetCss,
    /\.notepane \.th-pop-title\s*\{[\s\S]*?font-size:\s*13px;\s*line-height:\s*1\.45;[\s\S]*?white-space:\s*normal;\s*overflow-wrap:\s*anywhere;[\s\S]*?-webkit-line-clamp:\s*2;\s*line-clamp:\s*2;/,
    name + " <=1359px .th-pop-title must share 13px 2-line clamp styling with active tasks",
  );

  // 4) Mobile (<= 760px) drawer reading typography calibration (#file-view, #thread-drawer, #worker-drawer)
  assert.match(
    src,
    /\.filepane \.fv-body:not\(\.is-code\):not\(\.is-html\):not\(\.is-csv\)\s*\{[\s\S]*?overflow-x:\s*hidden;/,
    name + " mobile file preview markdown body must prevent horizontal overflow and keep safe-area horizontal padding",
  );
  assert.ok(
    src.includes(".filepane .fv-md { font-size: calc(var(--msg-fs-m, 15.5px) - 1px); line-height: 1.68; }"),
    name + " mobile file preview markdown must use calibrated drawer prose size relative to --msg-fs-m",
  );
  assert.ok(
    src.includes(".threadpane .td-text,\n  .threadpane .td-text.msg { font-size: calc(var(--msg-fs-m, 15.5px) - 1px); line-height: 1.66; }"),
    name + " mobile thread drawer must keep user and assistant text aligned and prevent .msg cascade override",
  );
  assert.ok(
    src.includes(".workerpane .wd-thread .msg.assistant { font-size: calc(var(--msg-fs-m, 15.5px) - 1px); line-height: 1.68; }"),
    name + " mobile worker drawer assistant prose must match calibrated drawer scale",
  );
  assert.ok(
    src.includes(".fv-md { max-width: none; width: 100%; min-width: 0; background: none; padding: 0; }"),
    name + " .fv-md must set width: 100% and min-width: 0",
  );
}

console.log("ALL_MOBILE_NOTES_CLIP_CONTRACTS_OK");
`
	cmd := exec.Command("node", "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mobile notes clip contract test failed: %v\n%s", err, out)
	}
}
