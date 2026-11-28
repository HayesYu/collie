/* Collie — Account Subscription Quota Badge & Popover/Sheet Controller
   Standalone module bridged with app.js via window.__collieQuota to avoid merge conflicts. */
(() => {
  "use strict";

  const $ = (s, el = document) => el.querySelector(s);
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
  const interp = (tpl, vars) => (!vars ? String(tpl ?? "") : String(tpl ?? "").replace(/\{([a-zA-Z0-9_]+)\}/g, (m, k) => (vars[k] !== undefined && vars[k] !== null ? String(vars[k]) : m)));
  const tr = (key, fallback, vars) => (window.CollieI18n ? window.CollieI18n.t(key, vars, fallback) : interp(fallback, vars));
  const locQ = (s) => (window.CollieI18n?.localizeQuotaText ? window.CollieI18n.localizeQuotaText(s) : String(s ?? ""));

  const CACHE_TTL_MS = 3 * 60 * 1000;
  const LOW_THRESHOLD = 20;
  const getClaudeOptinGuide = () => tr("quota.claudeGuide", "Collie 绝不修改 ~/.claude/settings.json，也不抓取私有 OAuth 接口；若你自行配置了 Claude Code 官方 statusLine 写入本地缓存，Collie 会自动读取并展示 5 小时与 7 天余量。");

  function isNarrow() {
    return typeof matchMedia === "function" && matchMedia("(max-width: 760px)").matches;
  }

  function isSafeHttpsURL(raw) {
    try {
      const u = new URL(String(raw || ""));
      return u.protocol === "https:";
    } catch {
      return false;
    }
  }

  function isWindowExpired(win, nowMs) {
    if (!win?.resets_at) return false;
    const t = Date.parse(win.resets_at);
    return Number.isFinite(t) && t <= nowMs;
  }

  function formatResetCountdown(resetsAt, remainingPct, nowMs = Date.now()) {
    if (!resetsAt) {
      return remainingPct === 100 ? tr("quota.unused", "尚未消耗") : "";
    }
    const target = Date.parse(resetsAt);
    if (!Number.isFinite(target)) return "";
    const diff = target - nowMs;
    if (diff <= 0) return tr("quota.resetDue", "已到重置点，待刷新");
    const totalMin = Math.ceil(diff / 60000);
    if (totalMin < 1) return tr("quota.resetLt1m", "<1m 后重置");
    if (totalMin < 60) return tr("quota.resetMin", "{m}m 后重置", { m: totalMin });
    const totalH = Math.floor(totalMin / 60);
    const remM = totalMin % 60;
    if (totalH < 24) return remM > 0 ? tr("quota.resetHrMin", "{h}h {m}m 后重置", { h: totalH, m: remM }) : tr("quota.resetHr", "{h}h 后重置", { h: totalH });
    const days = Math.floor(totalH / 24);
    const remH = totalH % 24;
    return remH > 0 ? tr("quota.resetDayHr", "{d}d {h}h 后重置", { d: days, h: remH }) : tr("quota.resetDay", "{d}d 后重置", { d: days });
  }

  function formatUpdatedAgo(updatedAt, nowMs = Date.now()) {
    if (!updatedAt) return tr("quota.neverSynced", "尚未同步");
    const t = Date.parse(updatedAt);
    if (!Number.isFinite(t)) return tr("quota.neverSynced", "尚未同步");
    const sec = Math.max(0, Math.floor((nowMs - t) / 1000));
    if (sec < 45) return tr("quota.justSynced", "刚刚同步");
    const min = Math.floor(sec / 60);
    if (min < 60) return tr("common.minsAgo", "{n} 分钟前", { n: min });
    const hr = Math.floor(min / 60);
    if (hr < 24) return tr("common.hoursAgo", "{n} 小时前", { n: hr });
    return tr("common.daysAgo", "{n} 天前", { n: Math.floor(hr / 24) });
  }

  // summarizeQuota extracts the bottleneck remaining % per numeric agent and
  // computes the overall quiet/low/exhausted level + compact hover tooltip.
  function summarizeQuota(snap, nowMs = Date.now()) {
    const agents = Array.isArray(snap?.agents) ? snap.agents : [];
    const items = [];
    for (const ag of agents) {
      if (ag?.status !== "ok" || !Array.isArray(ag.pools)) continue;
      let minVal = Infinity;
      let activeCount = 0;
      for (const p of ag.pools) {
        for (const w of (p.windows || [])) {
          if (typeof w.remaining_pct !== "number") continue;
          if (isWindowExpired(w, nowMs)) continue;
          activeCount++;
          if (w.remaining_pct < minVal) minVal = w.remaining_pct;
        }
      }
      if (activeCount > 0 && minVal !== Infinity) {
        items.push({ kind: ag.kind || "", name: ag.name || ag.kind || "", min: minVal });
      }
    }
    if (!items.length) {
      return {
        known: false,
        min: null,
        level: "unknown",
        tooltip: tr("quota.clickToView", "订阅额度 · 点击查看"),
        shortPct: "",
        summary: tr("common.view", "查看"),
      };
    }
    const overallMin = Math.min(...items.map((x) => x.min));
    const level = overallMin === 0 ? "exhausted" : overallMin < LOW_THRESHOLD ? "low" : "normal";
    const ordered = level === "normal" ? items : [...items].sort((a, b) => a.min - b.min);
    const summary = ordered.slice(0, 3).map((x) => `${x.kind} ${x.min}%`).join(" · ");
    const prefix = level === "exhausted"
      ? tr("quota.exhaustedPrefix", "额度已用尽")
      : level === "low"
        ? tr("quota.lowPrefix", "额度偏低")
        : tr("quota.title", "订阅额度");
    return {
      known: true,
      min: overallMin,
      level,
      tooltip: `${prefix} · ${summary}`,
      shortPct: `${overallMin}%`,
      summary,
    };
  }

  let hostAPI = null;
  let hostToast = null;
  let initialized = false;
  let wired = false;
  let snapshot = { updated_at: "", agents: [] };
  let isRefreshing = false;
  let lastTrigger = null;
  let claudeGuideOpen = false;
  let cooldownUntil = 0;
  let cooldownTimer = null;
  let openTickTimer = null;
  let fallbackScrim = null;

  function hasNativePopover(el) {
    return !!(el && typeof el.showPopover === "function" && typeof el.hidePopover === "function");
  }

  function isPopoverOpen(pop) {
    if (!pop) return false;
    if (hasNativePopover(pop)) {
      try { return pop.matches(":popover-open"); } catch {}
    }
    return pop.classList.contains(":popover-open");
  }

  function applyRing(el, val, level) {
    if (!el) return;
    el.classList.remove("is-low", "is-exhausted", "is-unknown");
    if (level === "unknown" || val === null || !Number.isFinite(val)) {
      el.classList.add("is-unknown");
      el.setAttribute("value", "0");
      el.style.setProperty("--q-val", "0");
      return;
    }
    const clamped = Math.max(0, Math.min(100, Math.round(val)));
    el.setAttribute("value", String(clamped));
    el.style.setProperty("--q-val", String(clamped));
    if (level === "low") el.classList.add("is-low");
    if (level === "exhausted") el.classList.add("is-exhausted");
  }

  function renderTriggers(nowMs = Date.now()) {
    const sum = summarizeQuota(snapshot, nowMs);
    const warn = sum.level === "low" || sum.level === "exhausted";
    const danger = sum.level === "exhausted";

    const navBtn = $("#nav-quota");
    const navRing = $("#nav-quota-ring");
    const navDot = $("#nav-quota-dot");
    if (navBtn) {
      applyRing(navRing, sum.min, sum.level);
      navBtn.title = sum.tooltip;
      navBtn.setAttribute("aria-label", sum.tooltip);
      if (navDot) {
        navDot.hidden = !warn;
        navDot.classList.toggle("is-danger", danger);
      }
    }

    const railBtn = $("#rail-quota");
    const railRing = $("#rail-quota-ring");
    const railDot = $("#rail-quota-dot");
    if (railBtn) {
      applyRing(railRing, sum.min, sum.level);
      railBtn.title = sum.tooltip;
      railBtn.setAttribute("aria-label", sum.tooltip);
      if (railDot) {
        railDot.hidden = !warn;
        railDot.classList.toggle("is-danger", danger);
      }
    }

    // Mobile main chat header: 0 extra widgets; only a 5px corner dot on ⋯ when <20%.
    const mobMoreDot = $("#collie-more-quota-dot");
    if (mobMoreDot) {
      mobMoreDot.hidden = !warn;
      mobMoreDot.classList.toggle("is-danger", danger);
    }

    const menuSub = $("#menu-quota-sub");
    if (menuSub) {
      menuSub.textContent = sum.known ? (warn ? tr("quota.minPct", "最低 {pct}", { pct: sum.shortPct }) : sum.shortPct) : tr("common.view", "查看");
      menuSub.classList.toggle("is-low", sum.level === "low");
      menuSub.classList.toggle("is-exhausted", danger);
    }
  }

  function syncRefreshControls(nowMs = Date.now()) {
    const btn = $("#qp-refresh-btn");
    const upd = $("#qp-updated");
    const cd = $("#qp-cooldown");
    const remSec = Math.max(0, Math.ceil((cooldownUntil - nowMs) / 1000));
    if (btn) {
      btn.classList.toggle("is-spinning", isRefreshing);
      btn.disabled = isRefreshing || remSec > 0;
    }
    if (upd) {
      upd.textContent = isRefreshing ? tr("quota.syncing", "同步中…") : formatUpdatedAgo(snapshot?.updated_at, nowMs);
    }
    if (cd) {
      cd.textContent = !isRefreshing && remSec > 0 ? tr("quota.cooldown", "{s}s 后可再刷", { s: remSec }) : "";
    }
    if (remSec <= 0 && cooldownTimer) {
      clearInterval(cooldownTimer);
      cooldownTimer = null;
    }
  }

  function startCooldown(seconds) {
    const sec = Number(seconds) || 0;
    if (sec <= 0) return;
    cooldownUntil = Math.max(cooldownUntil, Date.now() + sec * 1000);
    syncRefreshControls();
    if (!cooldownTimer) {
      cooldownTimer = setInterval(() => syncRefreshControls(), 1000);
    }
  }

  function renderPopoverBody(nowMs = Date.now()) {
    const body = $("#qp-body");
    if (!body) return;
    syncRefreshControls(nowMs);

    const agents = Array.isArray(snapshot?.agents) ? snapshot.agents : [];
    if (!agents.length) {
      const msg = isRefreshing || snapshot?.refreshing
        ? tr("quota.emptyLoading", "正在读取本机 Agent 订阅额度…")
        : tr("quota.emptyNone", "未检测到支持额度查询的本机 Agent");
      body.innerHTML = `<div class="qp-empty">${esc(msg)}</div>`;
      return;
    }

    const parts = [];
    for (const ag of agents) {
      let agMin = Infinity;
      let anyExpired = false;
      let activeWin = 0;
      if (ag.status === "ok" && Array.isArray(ag.pools)) {
        for (const p of ag.pools) {
          for (const w of (p.windows || [])) {
            if (isWindowExpired(w, nowMs)) {
              anyExpired = true;
              continue;
            }
            if (typeof w.remaining_pct === "number") {
              activeWin++;
              if (w.remaining_pct < agMin) agMin = w.remaining_pct;
            }
          }
        }
      }
      const agLevel = activeWin > 0
        ? (agMin === 0 ? "exhausted" : agMin < LOW_THRESHOLD ? "low" : "normal")
        : (anyExpired ? "expired" : "normal");

      let stateHtml = "";
      if (agLevel === "exhausted") stateHtml = `<span class="qa-state is-exhausted">${esc(tr("quota.stateExhausted", "已触限"))}</span>`;
      else if (agLevel === "low") stateHtml = `<span class="qa-state is-low">${esc(tr("quota.stateLow", "余量偏低"))}</span>`;
      else if (agLevel === "expired") stateHtml = `<span class="qa-state">${esc(tr("quota.stateExpired", "已到重置点"))}</span>`;

      let innerHtml = "";
      if (ag.status === "ok" && Array.isArray(ag.pools) && ag.pools.length) {
        const rows = [];
        let wIdx = 0;
        for (const pool of ag.pools) {
          if (pool.compact_full) {
            const label = pool.group
              ? tr("quota.poolFullGroup", "{group} 池 · 100% 满额", { group: pool.group })
              : tr("quota.poolFullOther", "其余模型池 · 100% 满额");
            rows.push(`<div class="qm-compact"><span>${esc(label)}</span></div>`);
            continue;
          }
          for (const w of (pool.windows || [])) {
            wIdx++;
            const pct = Math.max(0, Math.min(100, Math.round(Number(w.remaining_pct) || 0)));
            const expired = isWindowExpired(w, nowMs);
            const wLevel = expired ? "normal" : (pct === 0 ? "exhausted" : pct < LOW_THRESHOLD ? "low" : "normal");
            const cls = wLevel === "low" ? "is-low" : wLevel === "exhausted" ? "is-exhausted" : "";
            const winLbl = locQ(w.label || w.id);
            const title = pool.group ? `${pool.group} · ${winLbl}` : winLbl;
            const resetTxt = formatResetCountdown(w.resets_at, pct, nowMs);
            const lblId = `qm-${esc(ag.kind)}-${wIdx}`;
            const valText = tr("quota.progressAria", "剩余 {pct}%{reset}", {
              pct,
              reset: resetTxt ? tr("quota.progressComma", "，{reset}", { reset: resetTxt }) : "",
            });
            rows.push(`
              <div class="qm-row${expired ? " is-expired" : ""}">
                <div class="qm-top">
                  <span class="qm-left">
                    <span class="qm-lbl" id="${lblId}">${esc(title)}</span>
                    ${resetTxt ? `<span class="qm-reset">${esc(resetTxt)}</span>` : ""}
                  </span>
                  <span class="qm-pct ${cls}">${pct}%</span>
                </div>
                <progress class="q-bar ${cls}" value="${pct}" max="100" aria-labelledby="${lblId}" aria-valuetext="${esc(valText)}"></progress>
              </div>
            `);
          }
        }
        innerHtml = `<div class="qa-meters">${rows.join("")}</div>`;
        if (ag.note) {
          innerHtml += `<div class="qa-note is-sub"><span>${esc(locQ(ag.note))}</span></div>`;
        }
      } else if (ag.status === "optin") {
        const toggleTxt = claudeGuideOpen ? tr("quota.hideGuide", "收起说明") : tr("int.guide", "说明");
        innerHtml = `
          <div class="qa-note">
            <span>${esc(ag.note ? locQ(ag.note) : tr("quota.optinFallback", "按对话被动同步 · 未开启状态行采集"))}</span>
            <button type="button" class="qa-link" data-quota-guide="claude" aria-expanded="${claudeGuideOpen ? "true" : "false"}">${esc(toggleTxt)}</button>
          </div>
          ${claudeGuideOpen ? `<div class="qa-guide">${esc(getClaudeOptinGuide()).replace("~/.claude/settings.json", "<code>~/.claude/settings.json</code>")}</div>` : ""}
        `;
      } else if (ag.status === "external") {
        const link = isSafeHttpsURL(ag.link_url)
          ? `<a class="qa-link" href="${esc(ag.link_url)}" target="_blank" rel="noopener noreferrer">${esc(ag.link_text ? locQ(ag.link_text) : tr("quota.consoleLink", "控制台 ↗"))}</a>`
          : "";
        innerHtml = `
          <div class="qa-note">
            <span>${esc(ag.note ? locQ(ag.note) : tr("quota.externalFallback", "CLI 未开放实时额度接口"))}</span>
            ${link}
          </div>
        `;
      } else {
        innerHtml = `<div class="qa-note"><span>${esc(ag.note ? locQ(ag.note) : tr("quota.unavailFallback", "暂不可用"))}</span></div>`;
      }

      parts.push(`
        <section class="qa-sec" data-quota-kind="${esc(ag.kind)}">
          <div class="qa-head">
            <div class="qa-id">
              <h3 class="qa-name">${esc(ag.name || ag.kind)}</h3>
              ${ag.plan ? `<span class="qa-plan">${esc(locQ(ag.plan))}</span>` : ""}
            </div>
            ${stateHtml}
          </div>
          ${innerHtml}
        </section>
      `);
    }
    body.innerHTML = parts.join("");
  }

  function clearPopoverPosition(pop) {
    pop.classList.remove("pos-anchor-full", "pos-anchor-rail", "pos-manual", "is-sheet");
    pop.style.left = "";
    pop.style.right = "";
    pop.style.top = "";
    pop.style.bottom = "";
  }

  function resolveTrigger(triggerEl) {
    if (triggerEl && triggerEl.getBoundingClientRect && triggerEl.getBoundingClientRect().width > 0) {
      return triggerEl;
    }
    const app = $("#app");
    if (app?.classList.contains("side-collapsed") && !isNarrow()) {
      return $("#rail-quota") || $("#nav-quota");
    }
    return $("#nav-quota") || $("#rail-quota");
  }

  function positionPopover(pop, triggerEl) {
    clearPopoverPosition(pop);
    if (isNarrow()) {
      pop.classList.add("is-sheet");
      return;
    }

    const navBtn = $("#nav-quota");
    const railBtn = $("#rail-quota");
    const supportsAnchor = typeof CSS !== "undefined" && typeof CSS.supports === "function" && CSS.supports("position-area: block-start");
    const anchor = resolveTrigger(triggerEl);

    if (supportsAnchor && anchor === navBtn) {
      pop.classList.add("pos-anchor-full");
      return;
    }
    if (supportsAnchor && anchor === railBtn) {
      pop.classList.add("pos-anchor-rail");
      return;
    }

    // Manual clamped positioning fallback (e.g. Firefox or opened via ⋯ menu when sidebar collapsed/hidden)
    pop.classList.add("pos-manual");
    const popW = Math.min(326, window.innerWidth - 24);
    const r = anchor?.getBoundingClientRect?.() || { left: 12, right: 54, top: window.innerHeight - 48, bottom: window.innerHeight - 10 };
    if (anchor === railBtn) {
      pop.style.left = `${Math.max(10, Math.min(window.innerWidth - popW - 12, Math.round(r.right + 12)))}px`;
      pop.style.bottom = `${Math.max(12, Math.round(window.innerHeight - r.bottom))}px`;
    } else {
      pop.style.left = `${Math.max(10, Math.min(window.innerWidth - popW - 12, Math.round(r.left)))}px`;
      pop.style.bottom = `${Math.max(12, Math.round(window.innerHeight - r.top + 8))}px`;
    }
  }

  function setExpandedAria(open) {
    for (const btn of [$("#nav-quota"), $("#rail-quota")]) {
      if (!btn) continue;
      btn.setAttribute("aria-expanded", open && btn === lastTrigger ? "true" : "false");
    }
  }

  function startOpenTicker() {
    stopOpenTicker();
    openTickTimer = setInterval(() => {
      const pop = $("#quota-popover");
      if (!isPopoverOpen(pop)) {
        stopOpenTicker();
        return;
      }
      renderPopoverBody();
      renderTriggers();
    }, 30000);
  }

  function stopOpenTicker() {
    if (openTickTimer) {
      clearInterval(openTickTimer);
      openTickTimer = null;
    }
  }

  function removeFallbackScrim() {
    if (fallbackScrim) {
      fallbackScrim.remove();
      fallbackScrim = null;
    }
  }

  function hidePopoverEl(pop) {
    if (!pop || !isPopoverOpen(pop)) return;
    if (hasNativePopover(pop)) {
      pop.hidePopover();
    } else {
      pop.classList.remove(":popover-open");
      removeFallbackScrim();
      setExpandedAria(false);
      stopOpenTicker();
    }
  }

  function showPopoverEl(pop) {
    if (!pop) return;
    if (hasNativePopover(pop)) {
      pop.showPopover();
    } else {
      pop.classList.add(":popover-open");
      if (pop.classList.contains("is-sheet")) {
        removeFallbackScrim();
        fallbackScrim = document.createElement("div");
        fallbackScrim.className = "quota-fallback-scrim";
        fallbackScrim.addEventListener("click", () => hidePopoverEl(pop));
        document.body.append(fallbackScrim);
      }
      setExpandedAria(true);
      startOpenTicker();
    }
  }

  function isSnapshotStale(nowMs = Date.now()) {
    if (!snapshot?.updated_at) return true;
    const t = Date.parse(snapshot.updated_at);
    if (!Number.isFinite(t) || nowMs - t >= CACHE_TTL_MS) return true;
    for (const ag of (snapshot.agents || [])) {
      for (const p of (ag.pools || [])) {
        for (const w of (p.windows || [])) {
          if (isWindowExpired(w, nowMs)) return true;
        }
      }
    }
    return false;
  }

  async function fetchQuota() {
    if (!hostAPI) return;
    try {
      const data = await hostAPI("/api/quota");
      applySnapshot(data);
    } catch {
      // Quietly keep previous state; never disrupt main chat on quota errors.
      isRefreshing = false;
      syncRefreshControls();
    }
  }

  async function manualRefresh() {
    if (!hostAPI || isRefreshing) return;
    if (cooldownUntil > Date.now()) {
      syncRefreshControls();
      return;
    }
    isRefreshing = true;
    syncRefreshControls();
    try {
      const data = await hostAPI("/api/quota/refresh", { method: "POST", body: {} });
      isRefreshing = false;
      applySnapshot(data);
      const cd = Number(data?.refresh_cooldown_sec) || 30;
      startCooldown(cd);
      const live = $("#quota-live-status");
      if (live) live.textContent = tr("quota.liveSynced", "已同步最新订阅额度，未消耗模型额度");
    } catch (err) {
      isRefreshing = false;
      syncRefreshControls();
      hostToast?.(err?.message || tr("quota.refreshFail", "刷新订阅额度失败"), "attn");
    }
  }

  function applySnapshot(data) {
    if (!data || typeof data !== "object") return;
    snapshot = {
      updated_at: data.updated_at || "",
      stale: !!data.stale,
      refreshing: !!data.refreshing,
      refresh_cooldown_sec: Number(data.refresh_cooldown_sec) || 0,
      agents: Array.isArray(data.agents) ? data.agents : [],
    };
    isRefreshing = !!snapshot.refreshing;
    if (snapshot.refresh_cooldown_sec > 0) {
      startCooldown(snapshot.refresh_cooldown_sec);
    }
    renderTriggers();
    const pop = $("#quota-popover");
    if (isPopoverOpen(pop)) {
      renderPopoverBody();
    }
  }

  function openQuota(triggerEl) {
    const pop = $("#quota-popover");
    if (!pop) return;
    document.querySelectorAll?.(".menu")?.forEach((m) => { m.hidden = true; });
    const resolved = triggerEl || resolveTrigger(null);
    if (isPopoverOpen(pop) && lastTrigger === resolved) {
      hidePopoverEl(pop);
      return;
    }
    lastTrigger = resolved;
    renderPopoverBody();
    positionPopover(pop, resolved);
    if (!isPopoverOpen(pop)) {
      showPopoverEl(pop);
    }
    setExpandedAria(true);
    if (isSnapshotStale()) {
      fetchQuota();
    }
  }

  function wireDOM() {
    if (wired) return;
    wired = true;

    const navBtn = $("#nav-quota");
    const railBtn = $("#rail-quota");
    const pop = $("#quota-popover");
    const refreshBtn = $("#qp-refresh-btn");
    const closeBtn = $("#qp-close-btn");
    const body = $("#qp-body");

    navBtn?.addEventListener("click", (e) => {
      e.stopPropagation();
      openQuota(navBtn);
    });
    railBtn?.addEventListener("click", (e) => {
      e.stopPropagation();
      openQuota(railBtn);
    });
    refreshBtn?.addEventListener("click", () => manualRefresh());
    closeBtn?.addEventListener("click", () => {
      hidePopoverEl(pop);
      lastTrigger?.focus?.();
    });
    body?.addEventListener("click", (e) => {
      const btn = e.target?.closest?.('[data-quota-guide="claude"]');
      if (!btn) return;
      claudeGuideOpen = !claudeGuideOpen;
      renderPopoverBody();
    });

    pop?.addEventListener("toggle", (e) => {
      const open = e.newState === "open";
      setExpandedAria(open);
      if (open) startOpenTicker();
      else stopOpenTicker();
    });

    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape" && pop && !hasNativePopover(pop) && isPopoverOpen(pop)) {
        hidePopoverEl(pop);
        lastTrigger?.focus?.();
      }
    });
    document.addEventListener("click", (e) => {
      if (pop && !hasNativePopover(pop) && isPopoverOpen(pop) && !pop.contains(e.target) && e.target !== lastTrigger) {
        hidePopoverEl(pop);
      }
    });
    window.addEventListener("resize", () => {
      if (pop && isPopoverOpen(pop)) {
        positionPopover(pop, lastTrigger);
      }
    });
  }

  function init(opts = {}) {
    if (opts.api) hostAPI = opts.api;
    if (opts.toast) hostToast = opts.toast;
    wireDOM();
    if (!initialized) {
      initialized = true;
      renderTriggers();
    }
    fetchQuota();
  }

  function onLangChange() {
    renderTriggers();
    const pop = $("#quota-popover");
    if (isPopoverOpen(pop)) renderPopoverBody();
    else syncRefreshControls();
  }

  window.__collieQuota = {
    init,
    onEvent: applySnapshot,
    onLangChange,
    open: openQuota,
    close: () => hidePopoverEl($("#quota-popover")),
    refresh: manualRefresh,
    getSnapshot: () => snapshot,
    // Pure helpers exported for contract tests
    summarizeQuota,
    formatResetCountdown,
    formatUpdatedAgo,
  };
})();
