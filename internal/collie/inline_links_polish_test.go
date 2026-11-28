package collie

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWebInlineLinksMinimalistPolish(t *testing.T) {
	t.Parallel()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

const cssSrc = fs.readFileSync("web/app.css", "utf8");
const demoCssSrc = fs.readFileSync("docs/demo/app.css", "utf8");
const appSrc = fs.readFileSync("web/app.js", "utf8");
const demoAppSrc = fs.readFileSync("docs/demo/app.js", "utf8");

// ─── 1. CSS Design & Accessibility Contracts ───

// 1a) Completely remove blue 4-layer stacking (--hl-ty + border + underline) from code.code-link
const codeLinkSection = cssSrc.slice(
  cssSrc.indexOf("/* Inline prose links:"),
  cssSrc.indexOf(".md-table:focus-visible"),
);
assert.ok(codeLinkSection.length > 200, "inline prose link CSS section must exist");
assert.ok(!codeLinkSection.includes("var(--hl-ty)"), "code.code-link must NOT use syntax-highlight blue (--hl-ty)");
assert.ok(
  /:is\(\.msg,\s*\.td-text,\s*\.mem-body,\s*\.fv-md,\s*\.rv-md\)\s+code\.code-link\s*\{[\s\S]*?border:\s*0;[\s\S]*?box-decoration-break:\s*clone;[\s\S]*?text-decoration:\s*none;/.test(codeLinkSection),
  "code.code-link must cover all 5 prose containers with border:0, box-decoration-break:clone, and text-decoration:none",
);

// 1b) Light (classic B&W) and Dark (Linear deep graphite) tokens + user bubble contrast overrides
for (const tok of [
  "--link-ul:",
  "--link-ul-hover:",
  "--code-link-bg:",
  "--code-link-bg-hover:",
  "--code-link-bg-active:",
  "--code-link-cue:",
  "--code-link-cue-hover:",
]) {
  const occurrences = cssSrc.split(tok).length - 1;
  assert.ok(occurrences >= 4, tok + " must be defined in light, system-dark, explicit-dark, and .msg.user (got " + occurrences + ")");
}
const userBubbleBlock = cssSrc.match(/\.msg\.user\s*\{[^}]+\}/);
assert.ok(userBubbleBlock && userBubbleBlock[0].includes("--link-ul:") && userBubbleBlock[0].includes("--code-link-bg:"), ".msg.user must override link tokens for dark-bubble contrast");
assert.ok(!/a\.url\s*\{[^}]*color:\s*var\(--accent/.test(cssSrc), "a.url must inherit text color instead of var(--accent) to preserve user bubble legibility");

// 1c) Worker links: status micro-dot + subtle chevron + nowrap atomic badge
assert.ok(codeLinkSection.includes('a.worker-link > code.code-link::before'), "worker link must include subtle leading status dot");
assert.ok(codeLinkSection.includes('a.worker-link[data-worker-status="working"] > code.code-link::before'), "worker link must map working status semantics");
assert.ok(codeLinkSection.includes('a.worker-link[data-worker-status="blocked"] > code.code-link::before'), "worker link must map blocked status semantics");
assert.ok(codeLinkSection.includes('a.worker-link[data-worker-status="waiting"] > code.code-link::before'), "worker link must map waiting status semantics");
assert.ok(codeLinkSection.includes('a.worker-link > code.code-link::after') && codeLinkSection.includes('content: "\\203A" / "";'), "worker link must include subtle accessible chevron");
assert.ok(/a\.worker-link > code\.code-link[\s\S]*?white-space:\s*nowrap;/.test(codeLinkSection), "worker link badge must stay atomic without orphaning dot/chevron");

// 1d) File paths & code URLs (Word Joiner prevents orphan icon/arrow across wrapped lines)
assert.ok(codeLinkSection.includes("a.fpath > code.code-link::before") && codeLinkSection.includes("a.fpath.img-link > code.code-link::before") && codeLinkSection.includes('content: "\\2060" / "";'), "file path code-link must bind monochrome file/image glyph with Word Joiner");
assert.ok(codeLinkSection.includes("a.url > code.code-link::after") && codeLinkSection.includes('content: "\\2060\\2197" / "";'), "code URL link must bind subtle accessible external arrow with Word Joiner");

// 1e) Plain-text external links: offset 3px + restrained arrow excluding buttons/cards/images/code-link wrappers
assert.ok(codeLinkSection.includes("text-underline-offset: 3px;"), "plain-text links must use 3px underline offset");
assert.ok(
  codeLinkSection.includes(':not(.btn, .re, .fpath, .worker-link, .td-worker, .snap-head, .snap-chip):not(:has(> code.code-link)):not(:has(img, svg, button, picture, video, canvas))::after'),
  "plain-text external arrow must strictly exclude buttons, cards, images, and code-link wrappers",
);

// 1f) Focus-visible, reduced-motion, forced-colors & demo sync
assert.ok(codeLinkSection.includes(":focus-visible"), "inline links must define :focus-visible ring");
assert.ok(codeLinkSection.includes("@media (prefers-reduced-motion: reduce)"), "inline link animations must honor prefers-reduced-motion");
assert.ok(codeLinkSection.includes("@media (forced-colors: active)"), "inline links must provide forced-colors fallback");
assert.ok(demoCssSrc.includes("/* Inline prose links:") && !demoCssSrc.includes("var(--hl-ty) 84%"), "docs/demo/app.css must be synced with web/app.css");
assert.ok(demoAppSrc.includes("data-worker-status="), "docs/demo/app.js must be synced with web/app.js");

// ─── 2. Runtime JS Contracts (worker status semantics + user bubble URL/file linkify) ───
const noop = () => {};
const stubEl = () => ({
  hidden: false, className: "", dataset: {}, style: { setProperty: noop },
  classList: { add: noop, remove: noop, toggle: noop, contains: () => false },
  setAttribute: noop, getAttribute: () => "", addEventListener: noop, removeEventListener: noop,
  querySelector: () => stubEl(), querySelectorAll: () => [], append: noop, after: noop,
  closest: () => null, contains: () => false,
  getBoundingClientRect: () => ({ top: 0, bottom: 0, left: 0, right: 0, width: 0, height: 0 }),
  focus: noop, scrollIntoView: noop, options: [], selectedOptions: [], children: [],
});
const doc = {
  documentElement: stubEl(), body: stubEl(), hidden: false,
  createElement: () => stubEl(), getElementById: () => stubEl(),
  querySelector: () => stubEl(), querySelectorAll: () => [],
  addEventListener: noop, hasFocus: () => false,
};
const ctx = {
  console: { log: noop, info: noop, warn: noop, error: noop },
  document: doc, window: null,
  location: { hash: "", search: "", pathname: "/", origin: "http://127.0.0.1", hostname: "127.0.0.1" },
  history: { state: {}, replaceState: noop, back: noop },
  navigator: { userAgent: "NodeTest" },
  localStorage: { getItem: () => null, setItem: noop },
  matchMedia: () => ({ matches: false, addEventListener: noop }),
  MutationObserver: class { observe() {} },
  EventSource: class { addEventListener() {} close() {} },
  CSS: { escape: (s) => String(s) },
  URL, URLSearchParams, performance: { now: () => 0 },
  requestAnimationFrame: () => 0, setTimeout: () => 0, clearTimeout: noop,
  setInterval: () => 0, clearInterval: noop,
  addEventListener: noop, removeEventListener: noop, dispatchEvent: noop,
  Event, HTMLDialogElement: { prototype: {} },
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  innerWidth: 1440, innerHeight: 900,
  fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) }),
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker), "expected boot() tail in web/app.js");
const hook = "  window.__inlineLinkTest = { md, parseUserParts, userText, setSup: (s) => { sup = s; }, setState: (s) => { state = s; } };\n})();";
const instrumented = appSrc.replace(marker, hook);
assert.notEqual(instrumented, appSrc, "failed to instrument web/app.js");
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const T = ctx.__inlineLinkTest;

// 2a) Worker status semantics + cache invalidation when worker transitions
T.setState({ agents: [], shells: [], machines: [] });
T.setSup({
  workers: [
    { name: "fix-alpha", status: "working", pane_id: "w2:p1", ended: false },
    { name: "review-beta", status: "blocked", pane_id: "w3:p1", ended: false },
    { name: "wait-gamma", status: "waiting", pane_id: "w4:p1", ended: false },
    { name: "done-delta", status: "done", pane_id: "", ended: true },
  ],
});
const msg = "状态：` + "`fix-alpha`" + `、` + "`review-beta`" + `、` + "`wait-gamma`" + `、` + "`done-delta`" + `，代码 ` + "`go test ./...`" + `，文件 ` + "`~/code/app/main.go`" + `，图 ` + "`~/code/app/shot.png`" + `，链接 ` + "`https://example.com/run/1`" + ` 与 [文档](https://example.com/docs)。";
const h1 = T.md(msg);
assert.ok(h1.includes('data-open-worker="fix-alpha" data-worker-status="working"'), "working worker carries data-worker-status=working");
assert.ok(h1.includes('data-open-worker="review-beta" data-worker-status="blocked"'), "blocked worker carries data-worker-status=blocked");
assert.ok(h1.includes('data-open-worker="wait-gamma" data-worker-status="waiting"'), "waiting worker carries data-worker-status=waiting");
assert.ok(h1.includes('data-open-worker="done-delta" data-worker-status="ended"'), "ended worker carries data-worker-status=ended");
assert.ok(h1.includes("<code>go test ./...</code>"), "plain inline code stays non-clickable");
assert.ok(h1.includes('class="fpath" href="#" data-path="~/code/app/main.go"') && h1.includes('<code class="code-link">~/code/app/main.go</code>'), "file path code link preserved");
assert.ok(h1.includes('class="fpath img-link" href="#" data-path="~/code/app/shot.png"'), "image path code link carries img-link");
assert.ok(h1.includes('class="url" href="https://example.com/run/1"') && h1.includes('<code class="code-link">https://example.com/run/1</code>'), "inline URL code link preserved");
assert.ok(h1.includes('<a href="https://example.com/docs" target="_blank" rel="noopener noreferrer">文档</a>'), "markdown external link preserved");

// Status transition working -> ended invalidates mdCache cleanly
T.setSup({
  workers: [
    { name: "fix-alpha", status: "done", pane_id: "", ended: true },
    { name: "review-beta", status: "blocked", pane_id: "w3:p1", ended: false },
    { name: "wait-gamma", status: "waiting", pane_id: "w4:p1", ended: false },
    { name: "done-delta", status: "done", pane_id: "", ended: true },
  ],
});
const h2 = T.md(msg);
assert.ok(h2.includes('data-open-worker="fix-alpha" data-worker-status="ended"'), "mdCache must refresh worker status when worker ends");

// 2b) User bubbles: linkify both bare URLs and file paths without double-matching URL paths
const up = T.parseUserParts("看下 https://docs.example.com/guide/v1.md 和 ~/code/app/README.md。\n\n附件：\n/tmp/a1b2c3-notes.txt");
assert.ok(up.textHtml.includes('<a class="url" href="https://docs.example.com/guide/v1.md" target="_blank" rel="noopener noreferrer"'), "user bubble linkifies bare URL");
assert.ok(!up.textHtml.includes('data-path="/guide/v1.md"'), "URL path segment must not be double-linkified as file path");
assert.ok(up.textHtml.includes('class="fpath" href="#" data-path="~/code/app/README.md"'), "user bubble linkifies file path");
assert.ok(up.attHtml.includes('class="att"') && up.attHtml.includes("notes.txt"), "user bubble preserves attachment chip");

const ut = T.userText("同步看 https://example.com/spec 和 /tmp/demo.go");
assert.ok(ut.includes('class="url" href="https://example.com/spec"') && ut.includes('data-path="/tmp/demo.go"'), "thread drawer userText linkifies both URL and file path");

console.log("ALL_INLINE_LINKS_POLISH_TESTS_PASSED");
`
	cmd := exec.Command(nodeBin, "-e", script)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("inline links polish test failed: %v\n%s", err, string(out))
	}
}
