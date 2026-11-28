package collie

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRichExpressionBlocksAndInlineMicroSyntax(t *testing.T) {
	t.Parallel()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping JS rich expression tests")
	}
	root := filepath.Join("..", "..")
	jsTest := `
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const root = process.argv[1];
const appSrc = fs.readFileSync(path.join(root, "web/app.js"), "utf8");
const cssSrc = fs.readFileSync(path.join(root, "web/app.css"), "utf8");
const demoAppSrc = fs.readFileSync(path.join(root, "docs/demo/app.js"), "utf8");
const demoCssSrc = fs.readFileSync(path.join(root, "docs/demo/app.css"), "utf8");

// ─── 1. Static CSS, Frameless, Theme, Compositor-Only Motion & Demo Sync Contracts ───
{
  const sectionStart = cssSrc.indexOf("/* ─── Native Rich Expression Blocks:");
  assert.ok(sectionStart > 0, "web/app.css must define Native Rich Expression Blocks section");
  const richCss = cssSrc.slice(sectionStart);

  // Frameless KPI metrics strip + desktop cols + mobile/container 2-col grid + tabular-nums + Retina 600 weight & contrast
  assert.ok(richCss.includes(".md-metrics {") && richCss.includes("border: 0;") && richCss.includes("background: transparent;") && richCss.includes("box-shadow: none;"), "KPI metrics strip must be frameless and transparent");
  assert.ok(richCss.includes("grid-template-columns: repeat(2, minmax(0, 1fr));") && richCss.includes("@media (max-width: 760px)") && richCss.includes("@container (max-width: 520px)"), "KPI metrics strip must adapt cleanly to 2 columns on mobile and narrow drawers");
  assert.ok(richCss.includes("font-variant-numeric: tabular-nums;"), "KPI metrics must use tabular-nums");
  const metricValBlock = richCss.slice(richCss.indexOf(".md-metric-val {"), richCss.indexOf("}", richCss.indexOf(".md-metric-val {")) + 1);
  assert.ok(metricValBlock.includes("font-weight: 600;") && !metricValBlock.includes("font-weight: 650;"), "KPI .md-metric-val must use standard 600 weight instead of synthetic 650 faux-bold");
  assert.ok(metricValBlock.includes("font-size: 22px;"), "KPI .md-metric-val must use crisp integer base font-size");
  assert.ok(richCss.includes("--md-metric-sub: color-mix(in oklab, var(--fg-2) 86%, var(--fg-3));") && demoCssSrc.includes("--md-metric-sub: color-mix(in oklab, var(--fg-2) 86%, var(--fg-3));"), "KPI labels/deltas must use AA+ contrast token synced to demo");

  // Frameless stepper timeline + GPU-only breathing dot gated on prefers-reduced-motion: no-preference
  assert.ok(richCss.includes("ol.md-steps") && richCss.includes("li.md-step:not(:last-child)::before"), "steps timeline must render vertical hairline connector");
  const pulseIdx = richCss.indexOf("@keyframes md-step-pulse");
  assert.ok(pulseIdx > 0, "active step must define compositor breathing keyframes");
  const pulseBody = richCss.slice(pulseIdx, richCss.indexOf("}", richCss.indexOf("100%", pulseIdx)) + 2);
  assert.ok(pulseBody.includes("transform:") && pulseBody.includes("opacity:") && !/box-shadow|width|height|left|top/.test(pulseBody), "md-step-pulse must animate ONLY GPU transform/opacity");
  assert.ok(richCss.includes("@media (prefers-reduced-motion: no-preference) {\n  .md-step.is-active .md-step-node::after"), "active step pulse must opt in under prefers-reduced-motion: no-preference");

  // Entity pills: neutral monochrome without blue box styling
  assert.ok(richCss.includes("a.url.entity-pill") && richCss.includes("background: var(--code-link-bg);") && !richCss.includes("var(--hl-ty)"), "entity pills must use neutral monochrome surface, never blue pills");
  assert.ok(richCss.includes("code.code-link.is-fref .fref-line"), "file:line pill must style muted tabular line suffix");

  // Task list: broad container scope + custom SVG checkbox + readable reduced opacity without harsh strikethrough
  assert.ok(cssSrc.includes(":is(.msg, .td-text, .mem-body, .fv-md, .rv-md) ul.md-task-list"), "task list CSS must apply across main chat, thread drawer, notes, file viewer, and reader view");
  assert.ok(cssSrc.includes(".md-task-box .md-task-svg") && cssSrc.includes(".md-task-item.is-done .md-task-text { color: var(--fg); opacity: .62; text-decoration: none; }"), "completed task items must gently dim opacity without harsh strikethrough");

  // Keycap <kbd> and soft <mark> highlight in light, system-dark, explicit-dark, user bubble, and forced-colors
  assert.ok(richCss.includes("kbd.md-kbd") && richCss.includes("mark.md-mark"), "CSS must style kbd.md-kbd and mark.md-mark");
  assert.ok(richCss.includes(':root[data-theme="dark"] :is(.msg, .td-text, .mem-body, .fv-md, .rv-md) kbd.md-kbd'), "kbd must support explicit dark theme");
  assert.ok(richCss.includes(':root[data-theme="dark"] :is(.msg, .td-text, .mem-body, .fv-md, .rv-md) mark.md-mark'), "mark must support explicit dark theme");
  assert.ok(richCss.includes(".msg.user kbd.md-kbd") && richCss.includes(".msg.user mark.md-mark"), "kbd and mark must adapt inside dark user bubbles");
  assert.ok(richCss.includes("@media (forced-colors: active)") && richCss.includes("background-color: Mark; color: MarkText;"), "rich blocks must support forced-colors mode");

  // Entry micro-motion skips blur on .md-metrics and .md-steps
  assert.ok(cssSrc.includes(".rv-card, .md-metrics, .md-steps,"), "entry blur animation must skip .md-metrics and .md-steps");

  // Demo sync
  assert.ok(demoCssSrc.includes(".md-metrics {") && demoCssSrc.includes("a.url.entity-pill"), "docs/demo/app.css must be synced with web/app.css");
  assert.ok(demoAppSrc.includes("renderMetricsBlock") && demoAppSrc.includes("renderStepsBlock") && demoAppSrc.includes("parseGitEntityUrl"), "docs/demo/app.js must be synced with web/app.js");
}

// ─── 2. Load web/app.js renderer in isolated Node VM ───
const noop = () => {};
const stubEl = () => ({
  hidden: false, className: "", dataset: {}, style: { setProperty: noop },
  classList: { add: noop, remove: noop, toggle: noop, contains: () => false },
  setAttribute: noop, getAttribute: () => "", addEventListener: noop, removeEventListener: noop,
  querySelector: () => stubEl(), querySelectorAll: () => [], append: noop, after: noop,
  closest: () => null, contains: () => false,
  getBoundingClientRect: () => ({ top: 0, bottom: 100, left: 0, right: 600, width: 600, height: 100 }),
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
  fetch: () => Promise.reject(new Error("no network")),
  innerWidth: 1440, innerHeight: 900,
};
ctx.window = ctx;
ctx.globalThis = ctx;

const marker = "  boot();\n})();";
assert.ok(appSrc.includes(marker), "expected boot() tail in web/app.js");
const instrumented = appSrc.replace(
  marker,
  "  globalThis.__rb = { mdRender, plainLine, parseUserParts, userText, parseMetricsBlock, parseStepsBlock, parseGitEntityUrl, parseFileLineRef };\n})();"
);
vm.runInNewContext(instrumented, ctx, { filename: "web/app.js" });
const R = ctx.__rb;
const bt = "\x60";
const bt3 = bt.repeat(3);
const bt4 = bt.repeat(4);

// ─── 3. KPI Metrics Strip (metrics fence) ───
{
  const md = [
    bt3 + "metrics",
    "Total Records | 1.14M | +5,398",
    "Error Rate | 0 ERROR | 18h clean",
    "P99 Latency | 42 ms | -18.4%",
    "Build Time: 4.8s",
    bt3,
  ].join("\n");
  const html = R.mdRender(md);
  assert.ok(html.includes('class="md-metrics"') && html.includes('data-cols="4"') && html.includes('data-count="4"'), "4 metrics emit data-cols=4: " + html);
  assert.ok(html.includes('<dt class="md-metric-label">Total Records</dt><dd class="md-metric-val">1.14M</dd><dd class="md-metric-delta is-pos">+5,398</dd>'), "positive delta gets is-pos");
  assert.ok(html.includes('<dd class="md-metric-delta">18h clean</dd>'), "neutral note gets plain md-metric-delta");
  assert.ok(html.includes('<dd class="md-metric-delta is-neg">-18.4%</dd>'), "negative delta gets is-neg");
  assert.ok(html.includes('<dt class="md-metric-label">Build Time</dt><dd class="md-metric-val">4.8s</dd>'), "colon syntax Label: Value works");

  // 5 items -> balanced 3-col grid; tilde fence works; bullet & Chinese colon work
  const m5 = R.mdRender("~~~metrics\n- A | 1 | ↑ 10%\n- B | 2 | ↓ 3%\nC：3\nD | 4\nE | 5 | 持平\n~~~");
  assert.ok(m5.includes('data-cols="3"') && m5.includes('data-count="5"'), "5 items use 3-col grid");
  assert.ok(m5.includes("is-pos") && m5.includes("is-neg"), "arrow deltas classify pos/neg");

  // 4-backtick inline span mentioning fenced metrics must NEVER render metrics block
  const literalFence = R.mdRender("使用 " + bt4 + " " + bt3 + "metrics " + bt4 + " 展示指标。");
  assert.ok(!literalFence.includes("md-metrics") && literalFence.includes("<code>" + bt3 + "metrics</code>"), "inline 4-backtick literal fence must stay code");

  // Empty/unparseable metrics fence falls back to plain code block
  const badMetrics = R.mdRender(bt3 + "metrics\njust plain text without delimiters\n" + bt3);
  assert.ok(!badMetrics.includes('class="md-metrics"') && /class="md-code(?:\s|")/.test(badMetrics), "unparseable metrics falls back to code block");

  // XSS inside metrics is strictly escaped
  const xssMetrics = R.mdRender(bt3 + 'metrics\n<script>alert(1)</script> | <img src=x onerror=alert(2)> | +<svg onload=alert(3)>\n' + bt3);
  assert.ok(xssMetrics.includes('class="md-metrics"'), "renders safely");
  assert.ok(!/<script\b|<img\b|<svg\b/i.test(xssMetrics), "metrics must escape all raw HTML tags: " + xssMetrics);
}

// ─── 4. Stepper Timeline (steps fence) ───
{
  const md = [
    bt3 + "steps",
    "[x] Build artifacts | " + bt + "web/app.js:120" + bt + " synced",
    "[~] Verify responsive views | ==mobile + desktop== via <kbd>⌘R</kbd>",
    "[*] Smoke test PR | https://github.com/octocat/hello-world/pull/42",
    "[!] Blocked migration | waiting on schema lock",
    "[ ] Publish release | pending approval",
    bt3,
  ].join("\n");
  const html = R.mdRender(md);
  assert.ok(html.includes('<ol class="md-steps"'), "steps emits semantic <ol class=md-steps>");
  assert.ok(html.includes('class="md-step is-done"') && html.includes('class="md-step is-active"') && html.includes('class="md-step is-error"') && html.includes('class="md-step is-todo"'), "all 4 step statuses render");
  assert.equal((html.match(/class="md-step is-active"/g) || []).length, 2, "both [~] and [*] map to is-active");
  assert.ok(html.includes('data-path="web/app.js"') && html.includes('data-line="120"'), "inline file:line pill works inside steps note");
  assert.ok(html.includes('<mark class="md-mark">mobile + desktop</mark>') && html.includes('<kbd class="md-kbd">⌘R</kbd>'), "inline mark and kbd work inside steps note");
  assert.ok(html.includes('class="url entity-pill is-pr"') && html.includes("octocat/hello-world") && html.includes("#42"), "inline PR pill works inside steps note");

  // Non-step content falls back to code block
  const badSteps = R.mdRender(bt3 + "steps\nnot a step list\n" + bt3);
  assert.ok(!badSteps.includes('class="md-steps"') && /class="md-code(?:\s|")/.test(badSteps), "non-step content falls back to code block");

  // XSS inside steps title & note is neutralized
  const xssSteps = R.mdRender(bt3 + 'steps\n[x] <script>alert(1)</script> | <img src=x onerror=alert(2)> <kbd onclick="alert(3)">x</kbd>\n' + bt3);
  assert.ok(xssSteps.includes('class="md-steps"'), "steps still renders safely");
  assert.ok(!/<script\b|<img\b|<kbd\b/i.test(xssSteps) && !/<[^>]*\bon(?:error|click|mouseover|load)\s*=/i.test(xssSteps), "steps XSS neutralized: " + xssSteps);
}

// ─── 5. Inline Entity Pills (GitHub/GitLab PR/MR/Issue/Release + file:line) ───
{
  const urls = R.mdRender([
    "参考：",
    "https://github.com/org/repo/pull/12 ，",
    "https://github.com/org/repo/pull/34/files 、",
    "https://github.com/org/repo/issues/99 、",
    "https://github.com/org/repo/releases/tag/v0.3.14 、",
    "https://gitlab.com/team/core/api/-/merge_requests/48 、",
    "https://gitlab.com/org/repo/-/issues/7 、",
    "https://gitlab.com/org/repo/-/releases/v2.0.0 。",
    "以及 [https://github.com/org/repo/pull/55](https://github.com/org/repo/pull/55) 和 [自定义链接](https://github.com/org/repo/pull/66)。",
  ].join(" "));

  assert.ok(urls.includes('class="url entity-pill is-pr" href="https://github.com/org/repo/pull/12"') && urls.includes('<span class="ep-repo">org/repo</span><span class="ep-ref">#12</span>'), "GitHub PR renders compact entity pill");
  assert.ok(urls.includes('href="https://github.com/org/repo/pull/34/files"') && urls.includes('<span class="ep-ref">#34</span>'), "GitHub PR /files renders compact entity pill");
  assert.ok(urls.includes('class="url entity-pill is-issue" href="https://github.com/org/repo/issues/99"') && urls.includes('<span class="ep-ref">#99</span>'), "GitHub Issue renders compact entity pill");
  assert.ok(urls.includes('class="url entity-pill is-release" href="https://github.com/org/repo/releases/tag/v0.3.14"') && urls.includes('<span class="ep-ref">@v0.3.14</span>'), "GitHub Release renders compact entity pill");
  assert.ok(urls.includes('class="url entity-pill is-mr" href="https://gitlab.com/team/core/api/-/merge_requests/48"') && urls.includes('<span class="ep-repo">core/api</span><span class="ep-ref">!48</span>'), "GitLab MR renders compact entity pill with !");
  assert.ok(urls.includes('class="url entity-pill is-issue" href="https://gitlab.com/org/repo/-/issues/7"'), "GitLab Issue renders compact entity pill");
  assert.ok(urls.includes('class="url entity-pill is-release" href="https://gitlab.com/org/repo/-/releases/v2.0.0"') && urls.includes('<span class="ep-ref">@v2.0.0</span>'), "GitLab Release renders compact entity pill");
  assert.ok(urls.includes('href="https://github.com/org/repo/pull/55"') && urls.includes('<span class="ep-ref">#55</span>'), "same-URL markdown link upgrades to entity pill");
  assert.ok(urls.includes('<a href="https://github.com/org/repo/pull/66" target="_blank" rel="noopener noreferrer">自定义链接</a>'), "custom markdown link label is preserved");

  // Non-entity GitHub URLs remain normal links
  const plainGh = R.mdRender("主页 https://github.com/org/repo 与列表 https://github.com/org/repo/pulls");
  assert.ok(!plainGh.includes("entity-pill"), "repo root and /pulls list stay normal links: " + plainGh);

  // Malformed/XSS release tag is rejected from entity pill & stays safely escaped
  const tagXss = R.mdRender("https://github.com/org/repo/releases/tag/%3Cimg%20src=x%20onerror=alert(1)%3E");
  assert.ok(!tagXss.includes("entity-pill") && !/<img\b/i.test(tagXss), "encoded XSS in release tag must not emit entity pill or raw <img>: " + tagXss);

  // Inline file:line pills in bare prose, CJK-adjacent prose, and inline backticks
  const frefs = R.mdRender("定位到web/app.js:27631附近，同时看 " + bt + "internal/collie/cli.go:42-58" + bt + "、~/repo/main.go:88、/tmp/demo.py:10、./src/App.tsx:12:4 和 web/app.js#L123-L145。");
  assert.ok(frefs.includes('class="fpath has-code-link" href="#" data-path="web/app.js"') && frefs.includes('data-line="27631"') && frefs.includes('<code class="code-link is-fref"><span class="fref-path">web/app.js</span><span class="fref-line">:27631</span></code>'), "bare CJK-adjacent file:line pill works");
  assert.ok(frefs.includes('data-path="internal/collie/cli.go"') && frefs.includes('data-line="42-58"') && frefs.includes(':42-58</span>'), "backtick file:line range pill works");
  assert.ok(frefs.includes('data-path="~/repo/main.go"') && frefs.includes('data-line="88"'), "home path file:line pill works");
  assert.ok(frefs.includes('data-path="/tmp/demo.py"') && frefs.includes('data-line="10"'), "absolute path file:line pill works");
  assert.ok(frefs.includes('data-path="./src/App.tsx"') && frefs.includes('data-line="12:4"'), "dot-slash file:line:col pill works");
  assert.ok(frefs.includes('data-path="web/app.js"') && frefs.includes('data-line="123-L145"') && frefs.includes('#L123-L145</span>'), "#L line range pill works");

  // False positives & path traversal rejected
  const nonRefs = R.mdRender("版本 v0.3.14:123，时间 12:30，日期 12/31.2026:10，以及 ../secret/a.txt:1 和 " + bt + "../secret/a.txt:1" + bt + "。");
  assert.ok(!nonRefs.includes("is-fref") && !nonRefs.includes("data-path="), "false positives and .. traversal must NOT turn into file links: " + nonRefs);

  // User bubble also supports entity pills & file:line pills
  const up = R.parseUserParts("帮忙看 https://github.com/org/repo/pull/7 和 web/app.js:42");
  assert.ok(up.textHtml.includes('class="url entity-pill is-pr"') && up.textHtml.includes('data-path="web/app.js"') && up.textHtml.includes('data-line="42"'), "user bubble renders entity and file:line pills");
}

// ─── 6. High-Craft GFM Task List ───
{
  const md = [
    "- [x] 完成主流程 ==已验证==",
    "  - [x] 子项一 " + bt + "web/app.js:10" + bt,
    "    - [ ] 深层子项 <kbd>Esc</kbd>",
    "* [X] 大写 X 也支持",
    "+ [ ] 待办项",
  ].join("\n");
  const html = R.mdRender(md);
  assert.ok(html.includes('<ul class="md-task-list">'), "renders md-task-list");
  assert.ok(!html.includes("<input"), "must NOT emit raw <input> checkboxes");
  assert.equal((html.match(/class="md-task-svg"/g) || []).length, 5, "every checked and unchecked task item renders custom rounded SVG checkbox");
  assert.ok(html.includes('data-depth="1"') && html.includes('data-depth="2"'), "nested task list items emit depth attributes");
  assert.ok(html.includes('<mark class="md-mark">已验证</mark>') && html.includes('<kbd class="md-kbd">Esc</kbd>'), "inline micro-expressions work inside task items");
}

// ─── 7. Inline Micro-Expressions (<kbd> + ==highlight==) & Strict Code/XSS Safety ───
{
  const ok = R.mdRender("按 <kbd>Esc</kbd> 或 <kbd>⌘K</kbd>（组合 <kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>P</kbd>），确认 ==重点结论== 与 ==**加粗重点**== 以及 **==外粗内亮==** 和 ==含 " + bt + "code" + bt + " 高亮== 与 ==a == b==。");
  assert.equal((ok.match(/<kbd class="md-kbd">/g) || []).length, 5, "all 5 valid <kbd> keycaps render: " + ok);
  assert.ok(ok.includes('<kbd class="md-kbd">Esc</kbd>') && ok.includes('<kbd class="md-kbd">⌘K</kbd>'), "keycap labels preserved");
  assert.ok(ok.includes('<mark class="md-mark">重点结论</mark>'), "basic ==highlight== renders");
  assert.ok(ok.includes('<mark class="md-mark"><strong>加粗重点</strong></mark>'), "==**bold**== renders");
  assert.ok(ok.includes('<strong><mark class="md-mark">外粗内亮</mark></strong>'), "**==bold==** renders");
  assert.ok(ok.includes('<mark class="md-mark">含 <code>code</code> 高亮</mark>'), "==highlight with inline code== renders");
  assert.ok(ok.includes('<mark class="md-mark">a == b</mark>'), "==a == b== keeps inner spaced == intact");

  // <kbd> containing == or * does NOT get mangled
  const kbdSym = R.mdRender("输入 <kbd>==</kbd> 和 <kbd>*</kbd> 符号");
  assert.ok(kbdSym.includes('<kbd class="md-kbd">==</kbd>') && kbdSym.includes('<kbd class="md-kbd">*</kbd>') && !kbdSym.includes("<mark"), "<kbd> protects inner symbols");

  // Code blocks, inline code, math, URLs, prose == operators & escapes MUST NOT be broken!
  const codeBlock = R.mdRender([
    bt3 + "js",
    "if (a == b && c == d) return x === y;",
    "const label = '==not highlight==';",
    "const tag = '<kbd>Esc</kbd>';",
    bt3,
  ].join("\n"));
  assert.ok(!codeBlock.includes("<mark") && !codeBlock.includes("md-kbd"), "fenced code block must NEVER trigger <mark> or <kbd>: " + codeBlock);

  const inlineProtected = R.mdRender("行内代码 " + bt + "a == b && c == d" + bt + "、" + bt + "==literal==" + bt + "、" + bt + "<kbd>Esc</kbd>" + bt + "，公式 $x == y$，链接 https://example.com/?a=1==&b=2==，转义 \\==不亮== 与 \\<kbd\\>Esc\\</kbd\\>。");
  assert.ok(!inlineProtected.includes("<mark") && !inlineProtected.includes("<kbd"), "inline code, math, URLs, and escapes must NEVER trigger <mark> or <kbd>: " + inlineProtected);
  assert.ok(inlineProtected.includes("<code>a == b &amp;&amp; c == d</code>") && inlineProtected.includes("<code>==literal==</code>"), "inline code == preserved verbatim");

  const proseOperators = R.mdRender("判断 if (a == b && c == d) 或 a==1 && b==2 或 === 与 ==== 与 ===foo=== 均不高亮。");
  assert.ok(!proseOperators.includes("<mark"), "prose == operators and ===/==== must NOT trigger <mark>: " + proseOperators);

  // Strict XSS defense on <kbd> and ==highlight==
  const xssCases = [
    '<kbd onclick="alert(1)">Esc</kbd>',
    '<kbd onmouseover=alert(1)>Esc</kbd>',
    '<kbd><script>alert(1)</script></kbd>',
    '<kbd><img src=x onerror=alert(1)></kbd>',
    '==<script>alert(1)</script>==',
    '==<img src=x onerror=alert(2)>==',
    '==<a href="javascript:alert(3)">x</a>==',
  ];
  for (const payload of xssCases) {
    const out = R.mdRender(payload);
    assert.ok(!/<script\b|<img\b|<a\b/i.test(out), "XSS tag blocked for " + payload + " -> " + out);
    assert.ok(!/<[^>]*\bon(?:click|mouseover|error|load)\s*=/i.test(out), "XSS event handler blocked for " + payload + " -> " + out);
  }
  assert.ok(!R.mdRender('<kbd onclick="alert(1)">Esc</kbd>').includes("<kbd"), "attribute-bearing <kbd> must stay escaped text");
  assert.ok(!R.mdRender('<kbd><script>alert(1)</script></kbd>').includes("<kbd"), "nested HTML inside <kbd> must stay escaped text");

  // plainLine cleans new syntax for quote previews without breaking existing escapes
  assert.equal(R.plainLine("按 <kbd>⌘K</kbd> 看 ==重点== 约合 \\$9.99"), "按 ⌘K 看 重点 约合 $9.99");
  assert.equal(R.plainLine("[~] 正在部署 | 灰度中"), "正在部署 | 灰度中");
}

console.log("ALL_RICH_BLOCKS_TESTS_PASSED");
`
	cmd := exec.Command(nodeBin, "-e", jsTest, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rich blocks JS test failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "ALL_RICH_BLOCKS_TESTS_PASSED") {
		t.Fatalf("unexpected test output:\n%s", string(out))
	}
}
