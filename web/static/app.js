// Lighthouse dashboard. Plain ES modules, no build step, no dependencies.
// All server data is escaped before it touches innerHTML.

const $ = (sel, root = document) => root.querySelector(sel);

const state = { label: "", q: "", sort: "health", lastFocus: null };

// ---------------------------------------------------------------- helpers

const esc = (v) =>
  String(v ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);

const money = (n) =>
  n >= 1000 ? `$${(n / 1000).toFixed(n >= 100000 ? 0 : 1)}k` : `$${n}`;
const moneyFull = (n) => `$${n.toLocaleString("en-US")}`;
const labelText = { healthy: "Healthy", watch: "Watch", "at-risk": "At risk" };
const fmtDate = (iso) => new Date(iso).toLocaleDateString("en-GB", { day: "numeric", month: "short", timeZone: "UTC" });

function ago(iso) {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  return `${Math.floor(s / 3600)} h ago`;
}

async function api(path, opts) {
  const res = await fetch(path, { headers: { Accept: "application/json" }, ...opts });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body?.error?.message || `Request failed (${res.status})`);
  return body;
}

// Widths are applied through the CSSOM so the page works under a strict CSP.
function applyWidths(root) {
  root.querySelectorAll("[data-w]").forEach((el) => (el.style.width = `${el.dataset.w}%`));
}

function showBanner(msg) {
  const b = $("#banner");
  b.textContent = msg;
  b.hidden = !msg;
}

// ---------------------------------------------------------------- summary

function renderSummary(s) {
  $("#kpi-mrr").textContent = moneyFull(s.total_mrr);
  $("#kpi-accounts").textContent = `${s.accounts} accounts`;
  $("#kpi-risk").textContent = moneyFull(s.mrr_at_risk);
  const pct = s.total_mrr ? Math.round((s.mrr_at_risk / s.total_mrr) * 100) : 0;
  $("#kpi-risk-sub").textContent = `${pct}% of MRR · ${s.by_label["at-risk"]} accounts at risk`;
  $("#kpi-tickets").textContent = s.open_tickets;
  $("#kpi-urgent").textContent = `${s.urgent_open} urgent`;

  const order = ["at-risk", "watch", "healthy"];
  const dist = $("#kpi-dist");
  dist.innerHTML = order
    .filter((k) => s.by_label[k] > 0)
    .map((k) => `<span class="bg-${k}" data-w="${(s.by_label[k] / s.accounts) * 100}" title="${labelText[k]}: ${s.by_label[k]}"></span>`)
    .join("");
  dist.setAttribute("aria-label", order.map((k) => `${labelText[k]} ${s.by_label[k]}`).join(", "));
  applyWidths(dist);
  $("#kpi-legend").innerHTML = order
    .map((k) => `<li><i class="sw bg-${k}"></i>${labelText[k]} <b>${s.by_label[k]}</b></li>`)
    .join("");

  renderWeekly(s.weekly);
  $("#refreshed").textContent = `Data refreshed ${ago(s.refreshed_at)}`;
}

function renderWeekly(weeks) {
  const W = 560, H = 220, padL = 28, padB = 26, padT = 12;
  const max = Math.max(4, ...weeks.flatMap((w) => [w.opened, w.closed]));
  const step = Math.ceil(max / 4);
  const top = step * 4;
  const plotW = W - padL, plotH = H - padB - padT;
  const groupW = plotW / weeks.length;
  const barW = Math.min(16, groupW / 3);
  const y = (v) => padT + plotH - (v / top) * plotH;

  let svg = "";
  for (let v = 0; v <= top; v += step) {
    svg += `<line class="grid" x1="${padL}" x2="${W}" y1="${y(v)}" y2="${y(v)}"/>`;
    svg += `<text class="axis" x="${padL - 8}" y="${y(v) + 4}" text-anchor="end">${v}</text>`;
  }
  weeks.forEach((w, i) => {
    const cx = padL + groupW * i + groupW / 2;
    const bars = [["opened", w.opened, cx - barW - 1], ["closed", w.closed, cx + 1]];
    for (const [cls, v, x] of bars) {
      const h = Math.max(0, y(0) - y(v));
      svg += `<rect class="bar-${cls}" x="${x}" y="${y(v)}" width="${barW}" height="${h}" rx="3"><title>${cls}: ${v}</title></rect>`;
      if (v > 0) svg += `<text class="val" x="${x + barW / 2}" y="${y(v) - 4}" text-anchor="middle">${v}</text>`;
    }
    svg += `<text class="axis" x="${cx}" y="${H - 6}" text-anchor="middle">${esc(fmtDate(w.week_start))}</text>`;
  });
  const total = weeks.reduce((a, w) => a + w.opened, 0);
  $("#chart-weekly").innerHTML =
    `<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="Tickets opened and closed per week, last ${weeks.length} weeks. ${total} opened in total.">${svg}</svg>`;
}

// ---------------------------------------------------------------- accounts

function pill(label) {
  return `<span class="pill pill-${esc(label)}">${esc(labelText[label] || label)}</span>`;
}

function sparkline(values, w = 84, h = 24) {
  if (!values || values.length < 2) return `<span class="muted small">no data</span>`;
  const min = Math.min(...values), max = Math.max(...values);
  const span = max - min || 1;
  const pts = values
    .map((v, i) => `${((i / (values.length - 1)) * (w - 4) + 2).toFixed(1)},${(h - 2 - ((v - min) / span) * (h - 4)).toFixed(1)}`)
    .join(" ");
  return `<svg width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" aria-hidden="true"><polyline class="line" stroke="currentColor" points="${pts}"/></svg>`;
}

function trendClass(pct) {
  if (pct <= -10) return "trend-down";
  if (pct >= 10) return "trend-up";
  return "trend-flat";
}

function row(a) {
  const color = `bg-${a.health_label}`;
  const delta = `${a.usage_change_pct > 0 ? "+" : ""}${a.usage_change_pct}%`;
  const renew = a.days_to_renewal < 0 ? "overdue" : `${a.days_to_renewal} d`;
  return `<tr data-id="${esc(a.id)}">
    <td><button class="acct-btn" type="button" data-id="${esc(a.id)}">${esc(a.name)}</button><div class="acct-sub">${esc(a.plan)} · ${esc(a.industry)}</div></td>
    <td>${esc(a.owner)}</td>
    <td class="num">${moneyFull(a.mrr)}</td>
    <td><div class="health"><span class="score">${a.health}</span><span class="meter"><span class="${color}" data-w="${a.health}"></span></span>${pill(a.health_label)}</div></td>
    <td class="num">${a.open_tickets}${a.urgent_open ? ` <span class="pill pill-at-risk">${a.urgent_open} urgent</span>` : ""}</td>
    <td><div class="spark ${trendClass(a.usage_change_pct)}">${sparkline(a.usage)}<span class="delta">${esc(delta)}</span></div></td>
    <td class="num ${a.days_to_renewal <= 45 && a.health_label !== "healthy" ? "soon" : ""}">${renew}</td>
  </tr>`;
}

let accountsReq = 0;
async function loadAccounts() {
  const id = ++accountsReq;
  const p = new URLSearchParams();
  if (state.label) p.set("label", state.label);
  if (state.q) p.set("q", state.q);
  if (state.sort && state.sort !== "health") p.set("sort", state.sort);
  const { accounts } = await api(`api/accounts?${p}`);
  if (id !== accountsReq) return; // a newer request superseded this one
  const tbody = $("#rows");
  tbody.innerHTML = accounts.map(row).join("");
  applyWidths(tbody);
  $("#empty").hidden = accounts.length > 0;
  $("#acct-count").textContent = `· ${accounts.length}`;
}

async function loadFocus() {
  const { accounts } = await api("api/accounts");
  const top = accounts.filter((a) => a.health_label !== "healthy").slice(0, 3);
  $("#focus").innerHTML = top.length
    ? top
        .map(
          (a) => `<li><button type="button" data-id="${esc(a.id)}">
            <div class="focus-top"><span class="focus-name">${esc(a.name)}</span>${pill(a.health_label)}</div>
            <div class="focus-sig">${esc(moneyFull(a.mrr))} MRR · ${esc(a.signals.slice(0, 2).join(" · "))}</div>
          </button></li>`,
        )
        .join("")
    : `<li class="muted">Every account is healthy. 🎉</li>`;
}

// ---------------------------------------------------------------- sources

async function loadSources() {
  const r = await api("api/sources");
  $("#sources").innerHTML = r.sources
    .map(
      (s) => `<li><span class="dot ${s.ok ? "dot-ok" : "dot-fail"}"></span><div>
        <div class="source-name">${esc(s.name)}</div>
        <div class="small muted">${s.ok ? `${s.records} records · ${s.latency_ms.toFixed(1)} ms` : esc(s.error)}</div>
      </div></li>`,
    )
    .join("");
  $("#warnings").innerHTML = r.warnings.map((w) => `<li>⚠ ${esc(w)}</li>`).join("");
  const failed = r.sources.filter((s) => !s.ok).map((s) => s.name);
  if (r.stale) showBanner("All sources failed on the last refresh. Showing the previous data.");
  else if (failed.length) showBanner(`Some data is missing: ${failed.join(", ")} could not be read. Scores may be incomplete.`);
  else showBanner("");
}

// ---------------------------------------------------------------- drawer

function usageChart(points) {
  if (!points.length) return `<p class="muted small">No usage data from analytics.</p>`;
  const W = 460, H = 120, pad = 18;
  const vals = points.map((p) => p.active_users);
  const min = Math.min(...vals) * 0.9, max = Math.max(...vals) * 1.05;
  const x = (i) => pad + (i / (vals.length - 1)) * (W - pad * 2);
  const y = (v) => 8 + (1 - (v - min) / (max - min || 1)) * (H - 30);
  const line = vals.map((v, i) => `${x(i).toFixed(1)},${y(v).toFixed(1)}`).join(" ");
  const area = `${x(0)},${H - 22} ${line} ${x(vals.length - 1)},${H - 22}`;
  const labels = [0, vals.length - 1]
    .map((i) => `<text class="axis" x="${x(i)}" y="${H - 6}" text-anchor="${i ? "end" : "start"}">${esc(fmtDate(points[i].week_start))} · ${vals[i]}</text>`)
    .join("");
  return `<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="Weekly active users from ${vals[0]} to ${vals[vals.length - 1]}"><polygon class="area" points="${area}"/><polyline class="line" points="${line}"/>${labels}</svg>`;
}

function ticketItem(t) {
  const closed = t.status === "closed";
  const p = { urgent: "at-risk", high: "watch" }[t.priority] || "neutral";
  return `<li class="${closed ? "closed" : ""}"><div>
      <div class="t-sub">${esc(t.subject)}</div>
      <div class="t-meta">${esc(t.id)} · opened ${esc(fmtDate(t.opened_at))}${closed ? ` · closed ${esc(fmtDate(t.closed_at))}` : ` · ${esc(t.status)}`}</div>
    </div><span class="pill pill-${p}">${esc(t.priority)}</span></li>`;
}

async function openDrawer(id) {
  state.lastFocus = document.activeElement;
  const drawer = $("#drawer");
  $("#d-name").textContent = "Loading…";
  $("#d-meta").textContent = "";
  $("#d-body").innerHTML = "";
  drawer.hidden = false;
  $("#scrim").hidden = false;
  $("#d-close").focus();

  let a;
  try {
    a = await api(`api/accounts/${encodeURIComponent(id)}`);
  } catch (e) {
    $("#d-name").textContent = "Could not load account";
    $("#d-body").innerHTML = `<p class="muted">${esc(e.message)}</p>`;
    return;
  }
  $("#d-name").textContent = a.name;
  $("#d-meta").textContent = `${a.plan} · ${a.industry} · Owner: ${a.owner}`;
  const open = a.tickets.filter((t) => t.status !== "closed");
  const closed = a.tickets.filter((t) => t.status === "closed");

  $("#d-body").innerHTML = `
    <section>
      <div class="stats">
        <div class="stat"><b>${a.health}</b><span>Health · ${esc(labelText[a.health_label])}</span></div>
        <div class="stat"><b>${money(a.mrr)}</b><span>MRR</span></div>
        <div class="stat"><b>${a.days_to_renewal}d</b><span>to renewal (${esc(fmtDate(a.renewal_date))})</span></div>
      </div>
    </section>

    <section>
      <h3 class="section-title">Why this score</h3>
      ${a.signals.length ? `<ul class="signals">${a.signals.map((s) => `<li>${esc(s)}</li>`).join("")}</ul>` : `<p class="muted">No negative signals.</p>`}
    </section>

    <section class="brief" aria-live="polite">
      <div class="brief-head">
        <h3 class="section-title">Account brief</h3>
        <button class="btn btn-primary" type="button" id="brief-btn">Generate brief</button>
      </div>
      <div id="brief-out" class="muted small">A one-minute summary for your next call, written from the facts on this page.</div>
    </section>

    <section>
      <h3 class="section-title">Weekly active users</h3>
      <div class="usage-chart">${usageChart(a.usage_weeks)}</div>
    </section>

    <section>
      <h3 class="section-title">Open tickets (${open.length})</h3>
      ${open.length ? `<ul class="tickets">${open.map(ticketItem).join("")}</ul>` : `<p class="muted">None.</p>`}
    </section>

    ${closed.length ? `<section><h3 class="section-title">Recently closed</h3><ul class="tickets">${closed.slice(0, 3).map(ticketItem).join("")}</ul></section>` : ""}
  `;
  $("#brief-btn").addEventListener("click", () => generateBrief(a.id));
}

async function generateBrief(id) {
  const btn = $("#brief-btn");
  const out = $("#brief-out");
  btn.disabled = true;
  btn.textContent = "Writing…";
  out.className = "brief-text skeleton";
  out.textContent = "Generating a brief from the account facts…";
  try {
    const b = await api(`api/accounts/${encodeURIComponent(id)}/brief`, { method: "POST" });
    out.className = "";
    const badge = b.source === "claude" ? `<span class="pill pill-ai">✦ Claude${b.model ? ` · ${esc(b.model)}` : ""}</span>` : `<span class="pill pill-neutral">Rule-based</span>`;
    out.innerHTML = `${badge}<div class="brief-text">${esc(b.text)}</div>${b.note ? `<p class="brief-note">${esc(b.note)}</p>` : ""}`;
    btn.textContent = "Regenerate";
  } catch (e) {
    out.className = "muted small";
    out.textContent = `Could not generate a brief: ${e.message}`;
    btn.textContent = "Try again";
  } finally {
    btn.disabled = false;
  }
}

function closeDrawer() {
  $("#drawer").hidden = true;
  $("#scrim").hidden = true;
  state.lastFocus?.focus?.();
}

// ---------------------------------------------------------------- wiring

async function loadAll() {
  try {
    const summary = await api("api/summary");
    renderSummary(summary);
    await Promise.all([loadAccounts(), loadFocus(), loadSources()]);
  } catch (e) {
    showBanner(`Could not load data: ${e.message}`);
  }
}

function init() {
  $("#label-filter").addEventListener("click", (e) => {
    const b = e.target.closest("button[data-label]");
    if (!b) return;
    state.label = b.dataset.label;
    for (const x of $("#label-filter").querySelectorAll("button")) x.setAttribute("aria-checked", String(x === b));
    loadAccounts().catch((err) => showBanner(err.message));
  });

  let t;
  $("#search").addEventListener("input", (e) => {
    clearTimeout(t);
    t = setTimeout(() => {
      state.q = e.target.value.trim();
      loadAccounts().catch((err) => showBanner(err.message));
    }, 200);
  });

  $("#sort").addEventListener("change", (e) => {
    state.sort = e.target.value;
    loadAccounts().catch((err) => showBanner(err.message));
  });

  $("#rows").addEventListener("click", (e) => {
    const tr = e.target.closest("tr[data-id]");
    if (tr) openDrawer(tr.dataset.id);
  });
  $("#focus").addEventListener("click", (e) => {
    const b = e.target.closest("button[data-id]");
    if (b) openDrawer(b.dataset.id);
  });

  $("#d-close").addEventListener("click", closeDrawer);
  $("#scrim").addEventListener("click", closeDrawer);
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !$("#drawer").hidden) closeDrawer();
  });

  $("#refresh-btn").addEventListener("click", async (e) => {
    const btn = e.currentTarget;
    btn.disabled = true;
    btn.textContent = "Refreshing…";
    try {
      await api("api/refresh", { method: "POST" });
    } catch (err) {
      showBanner(err.message);
    }
    await loadAll();
    btn.disabled = false;
    btn.textContent = "Refresh data";
  });

  loadAll();
}

init();
