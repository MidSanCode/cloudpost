/* Cloudpost — M3 web console + webmail (vanilla JS, no deps) */
"use strict";

/* ---------------- helpers ---------------- */
const $ = (sel, el = document) => el.querySelector(sel);
const app = $("#app");

async function api(path, opts = {}) {
  const o = { headers: {}, credentials: "same-origin", ...opts };
  if (o.body && typeof o.body !== "string") { o.body = JSON.stringify(o.body); o.headers["Content-Type"] = "application/json"; }
  const res = await fetch(path, o);
  let data = null;
  try { data = await res.json(); } catch { /* empty */ }
  if (!res.ok) {
    const msg = (data && data.error) || res.status + " " + res.statusText;
    if (!o.noRedirect && res.status === 401) { renderLogin(); throw new Error(msg); }
    if (!o.noRedirect && res.status === 400 && msg === "not installed") { renderSetup(); throw new Error(msg); }
    throw new Error(msg);
  }
  return data;
}

function isNotInstalled(e) { return String((e && e.message) || "").includes("not installed"); }

let toastTimer = null;
function toast(msg) {
  const el = $("#snackbar");
  el.textContent = msg; el.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove("show"), 3200);
}

function openDialog(title, bodyHTML, onOk, okLabel = "确定") {
  const dlg = $("#dlg");
  $("#dlg-title").textContent = title;
  $("#dlg-body").innerHTML = bodyHTML;
  $("#dlg-ok").textContent = okLabel;
  const cancel = $("#dlg-cancel");
  const ok = $("#dlg-ok");
  const onCancel = () => { dlg.close(); cleanup(); };
  const onOkWrap = (e) => { e.preventDefault(); if (!onOk || onOk() !== false) { dlg.close(); cleanup(); } };
  function cleanup() { cancel.removeEventListener("click", onCancel); ok.removeEventListener("click", onOkWrap); }
  cancel.addEventListener("click", onCancel);
  ok.addEventListener("click", onOkWrap);
  dlg.showModal();
  return dlg;
}

function fmtSize(n) {
  if (n == null) return "";
  if (n < 1024) return n + " B";
  if (n < 1048576) return (n / 1024).toFixed(1) + " KB";
  return (n / 1048576).toFixed(1) + " MB";
}

const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

function fmtDate(ts) {
  if (!ts) return "";
  const d = new Date(ts * 1000);
  const today = new Date();
  if (d.toDateString() === today.toDateString()) return d.toTimeString().slice(0, 5);
  return d.toLocaleDateString("zh-CN", { month: "numeric", day: "numeric" }) + " " + d.toTimeString().slice(0, 5);
}

function themeToggle() {
  document.body.classList.toggle("dark");
  localStorage.setItem("cp_theme", document.body.classList.contains("dark") ? "dark" : "light");
}
if (localStorage.getItem("cp_theme") === "dark") document.body.classList.add("dark");

const I = {
  mail: '<i class="fa-solid fa-envelope"></i>', inbox: '<i class="fa-solid fa-inbox"></i>',
  send: '<i class="fa-solid fa-paper-plane"></i>', draft: '<i class="fa-solid fa-file-pen"></i>',
  trash: '<i class="fa-solid fa-trash-can"></i>', star: '<i class="fa-solid fa-star"></i>',
  starO: '<i class="fa-regular fa-star"></i>',
  settings: '<i class="fa-solid fa-gear"></i>', users: '<i class="fa-solid fa-users"></i>',
  filter: '<i class="fa-solid fa-filter"></i>', dash: '<i class="fa-solid fa-gauge-high"></i>',
  queue: '<i class="fa-solid fa-envelopes-bulk"></i>', dark: '<i class="fa-solid fa-circle-half-stroke"></i>',
  add: '<i class="fa-solid fa-plus"></i>', refresh: '<i class="fa-solid fa-rotate-right"></i>',
  key: '<i class="fa-solid fa-key"></i>', folder: '<i class="fa-solid fa-folder"></i>',
  cloud: '<i class="fa-solid fa-cloud-arrow-down"></i>', edit: '<i class="fa-solid fa-pen"></i>',
  search: '<i class="fa-solid fa-magnifying-glass"></i>', back: '<i class="fa-solid fa-arrow-left"></i>',
  logout: '<i class="fa-solid fa-right-from-bracket"></i>', paperclip: '<i class="fa-solid fa-paperclip"></i>',
  plug: '<i class="fa-solid fa-plug"></i>', download: '<i class="fa-solid fa-download"></i>',
  globe: '<i class="fa-solid fa-globe"></i>', dns: '<i class="fa-solid fa-sitemap"></i>',
  server: '<i class="fa-solid fa-server"></i>', info: '<i class="fa-solid fa-circle-info"></i>',
};

/* ---------------- DNS records helper ---------------- */
// Renders the DNS record checklist for the given domain into a table.
function dnsRows(domain, host, ip) {
  const d = domain || "your.domain.com";
  const h = host || ("mail." + d);
  const rows = [
    { t: "A", name: h, val: ip, must: true, why: "服务器地址（邮件子域指向本机公网 IP）" },
    { t: "MX", name: d, val: `${h}. (优先级 10)`, must: true, why: "把发往 @d 的邮件路由到本服务器".replace("d", d) },
    { t: "TXT/SPF", name: d, val: "v=spf1 mx -all", must: false, why: "声明只有本服务器可代发该域邮件，防伪造" },
    { t: "PTR(反向)", name: ip, val: h + ".", must: false, why: "由 IP 服务商(云厂商/IDC)设置；缺失时外发邮件易被判垃圾" },
    { t: "DMARC", name: "_dmarc." + d, val: "v=DMARC1; p=none; rua=mailto:postmaster@" + d, must: false, why: "接收伪造邮件的报告，可选但推荐" },
  ];
  return rows;
}

function renderDnsTable(tableId, domain, host, ip) {
  const el = document.getElementById(tableId);
  if (!el) return;
  if (!domain) { el.innerHTML = `<tr><td class="muted">填写域名后显示</td></tr>`; return; }
  const rows = dnsRows(domain, host, ip);
  el.innerHTML = `
  <tr><th style="width:64px">类型</th><th style="width:34%">主机记录</th><th>记录值 (复制)</th><th style="width:34%">作用</th></tr>
  ` + rows.map((r) => `
  <tr>
    <td><span class="chip">${r.t}</span>${r.must ? ' <span class="chip ok">必须</span>' : ""}</td>
    <td><span class="mono">${esc(r.name)}</span></td>
    <td><span class="mono">${esc(r.val)}</span></td>
    <td class="muted">${esc(r.why)}</td>
  </tr>`).join("");
}

// Font Awesome fallback: if the primary CDN (jsDelivr) is unreachable —
// common in some regions — swap in a mirror so icons don't disappear.
(function faFallback() {
  const probe = document.createElement("i");
  probe.className = "fa-solid fa-envelope";
  probe.style.cssText = "position:absolute;left:-9999px;visibility:hidden";
  document.body.appendChild(probe);
  const ff = getComputedStyle(probe).fontFamily || "";
  probe.remove();
  if (/font awesome/i.test(ff)) return;
  const l = document.createElement("link");
  l.rel = "stylesheet";
  l.href = "https://registry.npmmirror.com/@fortawesome/fontawesome-free/6.7.2/files/css/all.min.css";
  document.head.appendChild(l);
})();

/* ---------------- boot ---------------- */
async function boot() {
  // Single silent probe: setup state first, then which session is alive.
  // Avoids 401 noise (no /api/status probe) and login-page flashes.
  let st = null, who = null;
  try { st = await api("/api/setup/status?_=" + Date.now(), { noRedirect: true }); } catch { /* ignore */ }
  if (st && st.installed === false) return renderSetup();
  try { who = await api("/api/whoami?_=" + Date.now(), { noRedirect: true }); } catch { /* ignore */ }
  if (who && who.admin) return renderConsole("overview");
  if (who && who.mail) return renderMailShell();
  return renderLogin();
}

/* ---------------- setup wizard ---------------- */
function renderSetup() {
  let step = 0;
  const data = { admin_password: "", domain: "", hostname: "", web_port: 8080, smtp_port: 2525, pop3_port: 1110, imap_port: 1143, relay_host: "", relay_port: 587, relay_user: "", relay_pass: "" };
  const steps = [
    {
      html: () => `
        <div class="field"><label>管理员密码</label><input type="password" id="s-pw" placeholder="至少 6 位"></div>
        <div class="field"><label>确认密码</label><input type="password" id="s-pw2" placeholder="再次输入"></div>
        <div class="hint muted">管理员密码用于登录网页控制台。</div>`,
      collect: () => {
        const pw = $("#s-pw").value, pw2 = $("#s-pw2").value;
        if (pw !== pw2) { toast("两次输入的密码不一致"); return false; }
        data.admin_password = pw; return true;
      },
    },
    {
      html: () => `
        <div class="field"><label>邮件域名 (Primary Domain)</label><input id="s-domain" placeholder="mail.example.com">
          <div class="hint">收件地址形如 alice@该域名；本机 SMTP 将接收发往该域名的邮件。</div></div>
        <div class="field"><label>服务器主机名 (可选)</label><input id="s-hostname" placeholder="留空自动取主机名"></div>
        <div class="dns-panel">
          <div class="dns-title">${I.dns} 需要配置的 DNS 记录 <span class="muted" style="font-weight:400">· 随域名实时生成</span></div>
          <table id="dns-tbl">
            <tr><td class="muted">${I.info} 填写域名后，这里会列出 MX / SPF / A 等记录清单，下一步可查看完整可复制版本。</td></tr>
          </table>
          <div class="dns-note">
            ${I.info} 若暂时只用客户端直连本机测试，可以跳过 DNS；要接收互联网来信，请至少完成
            <b>A 记录</b> 与 <b>MX 记录</b>。DNS 生效可能需要几分钟到 48 小时。
          </div>
        </div>`,
      wire: () => {
        const upd = () => {
          const d = $("#s-domain").value.trim().toLowerCase();
          const h = $("#s-hostname").value.trim() || ("mail." + (d || "example.com"));
          renderDnsTable("dns-tbl", d, h, "<服务器公网IP>");
        };
        $("#s-domain").addEventListener("input", upd);
        $("#s-hostname").addEventListener("input", upd);
        upd();
      },
      collect: () => {
        data.domain = $("#s-domain").value.trim().toLowerCase();
        data.hostname = $("#s-hostname").value.trim();
        if (!/^[a-z0-9.-]+\.[a-z]{2,}$/i.test(data.domain)) { toast("请输入有效域名，如 mail.example.com"); return false; }
        return true;
      },
    },
    {
      html: () => `
        <div class="dns-panel">
          <div class="dns-title">${I.dns} DNS 配置清单 <span class="muted" style="font-weight:400">· 在你的域名服务商处添加</span></div>
          <table id="dns-tbl2"></table>
          <div class="dns-note">
            ${I.info} 配置后可用 <span class="mono">nslookup -type=MX ${esc(data.domain)}</span> 验证是否生效；
            公网收信还需确认服务器 25 端口入站未被运营商/云厂商封锁，且 IP 不在垃圾邮件黑名单中。
            安装完成后，控制台「总览」页会随实际配置再次显示这份清单。
          </div>
        </div>
        <div class="hint muted" style="margin-top:10px">DNS 可以稍后配置，不阻塞安装；下一步可修改各服务端口。</div>`,
      wire: () => renderDnsTable("dns-tbl2", data.domain, data.hostname || ("mail." + data.domain), "<服务器公网IP>"),
      collect: () => true,
    },
    {
      html: () => `
        <div class="row2">
          <div class="field"><label>Web 端口</label><input type="number" id="s-web" value="${data.web_port}"></div>
          <div class="field"><label>SMTP 端口</label><input type="number" id="s-smtp" value="${data.smtp_port}"></div>
          <div class="field"><label>POP3 端口</label><input type="number" id="s-pop3" value="${data.pop3_port}"></div>
          <div class="field"><label>IMAP 端口</label><input type="number" id="s-imap" value="${data.imap_port}"></div>
        </div>
        <div class="field"><label>SMTP 中继主机 (可选)</label><input id="s-relay" placeholder="如 smtp.exmail.qq.com（直投 MX 留空）"></div>`,
      collect: () => {
        data.web_port = parseInt($("#s-web").value) || data.web_port;
        data.smtp_port = parseInt($("#s-smtp").value) || data.smtp_port;
        data.pop3_port = parseInt($("#s-pop3").value) || data.pop3_port;
        data.imap_port = parseInt($("#s-imap").value) || data.imap_port;
        data.relay_host = $("#s-relay").value.trim();
        return true;
      },
    },
  ];
  function render() {
    app.innerHTML = `
    <div class="center-screen"><div class="auth-card">
      <h1><span class="logo">${I.mail}</span>Cloudpost 邮件中心</h1>
      <div class="sub">安装向导 · 第 ${step + 1} / ${steps.length} 步</div>
      <div class="stepper">${steps.map((_, i) => `<div class="step ${i <= step ? "on" : ""}"></div>`).join("")}</div>
      ${steps[step].html()}
      <div class="flex spread mt">
        <button class="btn text" id="w-back" ${step === 0 ? "disabled" : ""}>${I.back} 上一步</button>
        <button class="btn filled" id="w-next">${step === steps.length - 1 ? "完成安装" : "下一步"}</button>
      </div>
    </div></div>`;
    if (steps[step].wire) steps[step].wire();
    $("#w-back").onclick = () => { if (step > 0) { step--; render(); } };
    $("#w-next").onclick = async () => {
      if (steps[step].collect() === false) return;
      if (step < steps.length - 1) { step++; render(); return; }
      try {
        await api("/api/setup", { method: "POST", body: data });
        toast("安装完成！请使用管理员密码登录");
        renderLogin(true);
      } catch (e) {
        if (String(e.message).includes("already installed")) { toast("服务器已完成安装，请直接登录"); renderLogin(); return; }
        toast(e.message);
      }
    };
  }
  render();
}

/* ---------------- login ---------------- */
function renderLogin(afterSetup = false) {
  app.innerHTML = `
  <div class="center-screen"><div class="auth-card">
    <h1><span class="logo">${I.mail}</span>Cloudpost 邮件中心</h1>
    <div class="sub">${afterSetup ? "安装完成，请登录继续" : "登录以继续"}</div>
    <div class="flex mb" style="gap:0;border-bottom:1px solid var(--md-outline-variant)">
      <button class="btn text" id="tab-admin" style="border-bottom:2px solid var(--md-primary);border-radius:0">管理员</button>
      <button class="btn text" id="tab-mail" style="border-radius:0">邮箱用户</button>
    </div>
    <div id="login-body"></div>
    <div class="flex spread mt">
      <button class="icon-btn" id="theme" title="切换主题">${I.dark}</button>
    </div>
  </div></div>`;
  let mode = "admin";
  const body = $("#login-body");
  function draw() {
    if (mode === "admin") {
      body.innerHTML = `
      <div class="field"><label>管理员密码</label><input type="password" id="l-pw" placeholder="安装时设置的管理员密码"></div>
      <button class="btn filled" id="l-go" style="width:100%">登 录</button>`;
      $("#l-go").onclick = async () => {
        try { await api("/api/login", { method: "POST", body: { password: $("#l-pw").value } }); renderConsole("overview"); }
        catch (e) { if (!isNotInstalled(e)) toast(e.message); } // not installed → api() already switched to the wizard
      };
    } else {
      body.innerHTML = `
      <div class="field"><label>邮箱地址</label><input id="l-addr" placeholder="alice@example.com"></div>
      <div class="field"><label>邮箱密码</label><input type="password" id="l-pw"></div>
      <button class="btn filled" id="l-go" style="width:100%">打开邮箱</button>`;
      $("#l-go").onclick = async () => {
        try { await api("/api/mail/login", { method: "POST", body: { address: $("#l-addr").value, password: $("#l-pw").value } }); renderMailShell(); }
        catch (e) { toast(e.message); }
      };
    }
    $("#l-go").addEventListener("keydown", (e) => e.key === "Enter" && $("#l-go").click());
  }
  $("#tab-admin").onclick = () => { mode = "admin"; $("#tab-admin").style.borderBottom = "2px solid var(--md-primary)"; $("#tab-mail").style.borderBottom = "none"; draw(); };
  $("#tab-mail").onclick = () => { mode = "mail"; $("#tab-mail").style.borderBottom = "2px solid var(--md-primary)"; $("#tab-admin").style.borderBottom = "none"; draw(); };
  $("#theme").onclick = themeToggle;
  draw();
  // Self-heal: if the server is not installed (e.g. factory reset in another
  // tab, or a stale cached boot decision), jump to the setup wizard.
  api("/api/setup/status?_=" + Date.now()).then((st) => {
    if (st && st.installed === false) renderSetup();
  }).catch(() => { /* network hiccup; stay on login */ });
}

/* ---------------- console shell ---------------- */
const NAV = [
  ["overview", I.dash, "总览"],
  ["mail", I.mail, "邮件"],
  ["accounts", I.users, "账号"],
  ["filters", I.filter, "过滤规则"],
  ["queue", I.queue, "发送队列"],
  ["settings", I.settings, "设置"],
];

function renderConsole(view) {
  app.innerHTML = `
  <div class="shell">
    <nav class="rail">
      <div class="brand">${I.mail}</div>
      ${NAV.map(([id, ic, label]) => `<button class="rail-item" data-v="${id}"><span class="ic">${ic}</span>${label}</button>`).join("")}
      <div style="flex:1"></div>
      <button class="rail-item" id="c-logout"><span class="ic">${I.logout}</span>退出</button>
    </nav>
    <div class="main">
      <div class="topbar">
        <h2 id="v-title"></h2>
        <button class="icon-btn" id="c-theme" title="切换主题">${I.dark}</button>
      </div>
      <div class="content" id="v-body"></div>
    </div>
  </div>`;
  $(".rail").querySelectorAll(".rail-item[data-v]").forEach((b) => {
    b.onclick = () => {
      $(".rail").querySelectorAll(".rail-item").forEach((x) => x.classList.remove("active"));
      b.classList.add("active");
      drawView(b.dataset.v);
    };
  });
  $("#c-theme").onclick = themeToggle;
  $("#c-logout").onclick = async () => { await api("/api/logout", { method: "POST" }); renderLogin(); };
  const target = $(`.rail-item[data-v="${view}"]`);
  if (target) target.click();
}

let cachedAccounts = null;
async function loadAccounts(force) {
  if (!cachedAccounts || force) cachedAccounts = await api("/api/accounts");
  return cachedAccounts;
}

function drawView(view) {
  $("#v-title").textContent = NAV.find((n) => n[0] === view)[2];
  const body = $("#v-body");
  if (view === "overview") return viewOverview(body);
  if (view === "mail") return viewAdminMail(body);
  if (view === "accounts") return viewAccounts(body);
  if (view === "filters") return viewFilters(body);
  if (view === "queue") return viewQueue(body);
  if (view === "settings") return viewSettings(body);
}

async function viewOverview(el) {
  el.innerHTML = `<div class="empty">加载中…</div>`;
  const st = await api("/api/status");
  const c = st.config || {};
  el.innerHTML = `
  <div class="grid-cards">
    <div class="stat"><div class="num">${st.accounts_local}</div><div class="lbl">本地邮箱账号</div></div>
    <div class="stat"><div class="num">${st.accounts_remote}</div><div class="lbl">远程拉取账号</div></div>
    <div class="stat"><div class="num">${st.messages}</div><div class="lbl">邮件总数</div></div>
    <div class="stat"><div class="num">${st.queue_pending}<span class="muted" style="font-size:14px"> / ${st.queue_failed} 失败</span></div><div class="lbl">待发送队列</div></div>
  </div>
  <div class="card mt">
    <h3>服务状态</h3>
    <table class="tbl">
      <tr><td>Web 控制台</td><td><span class="chip ok">运行中</span></td><td>端口 ${c.web_port ?? "—"}</td></tr>
      <tr><td>SMTP（收信/发信提交）</td><td><span class="chip ok">运行中</span></td><td>端口 ${c.smtp_port ?? "—"}</td></tr>
      <tr><td>POP3</td><td><span class="chip ok">运行中</span></td><td>端口 ${c.pop3_port ?? "—"}</td></tr>
      <tr><td>IMAP</td><td><span class="chip ok">运行中</span></td><td>端口 ${c.imap_port ?? "—"}</td></tr>
    </table>
    <div class="muted mt">邮件域名：${esc(c.primary_domain ?? "—")} · 主机名：${esc(c.hostname ?? "—")} · 运行时长 ${Math.floor(st.uptime_sec / 60)} 分钟 · 版本 v${esc(st.version)}</div>
  </div>
  <div class="card mt">
    <h3>${I.dns} DNS 配置指引</h3>
    <div class="dns-panel" style="margin-top:8px">
      <table id="dns-tbl-console"></table>
      <div class="dns-note">
        ${I.info} 在域名服务商处添加以上记录后，互联网来信才能路由到本服务器。
        验证命令：<span class="mono">nslookup -type=MX ${esc(c.primary_domain || "你的域名")}</span>。
        使用中继发信时，SPF 记录应按中继服务商的要求补充 include。
      </div>
    </div>
  </div>
  <div class="card mt">
    <h3>下一步</h3>
    <div class="flex">
      <button class="btn tonal" id="ov-acc">${I.add} 创建本地邮箱</button>
      <button class="btn outlined" id="ov-remote">${I.cloud} 添加远程拉取</button>
    </div>
    <div class="muted mt">用邮件客户端（Thunderbird / Foxmail / Outlook）连接本服务器：<br>
      SMTP: ${esc(c.hostname || "服务器IP")}:${c.smtp_port} · POP3: ${c.pop3_port} · IMAP: ${c.imap_port} · 登录名 = 完整邮箱地址</div>
  </div>`;
  renderDnsTable("dns-tbl-console", c.primary_domain, c.hostname, "<服务器公网IP>");
  $("#ov-acc").onclick = () => accountDialog("local");
  $("#ov-remote").onclick = () => accountDialog("remote");
}

/* ---------------- admin: mail preview ---------------- */
function viewAdminMail(el) {
  el.innerHTML = `
  <div class="card" style="max-width:520px">
    <h3>${I.key} 打开邮箱</h3>
    <p class="muted">管理员可免密打开任意邮箱（含远程拉取账号自身的存储）。远程账号若设置了「投递到本地邮箱」，邮件在该目标邮箱的收件箱。</p>
    <div class="field"><label>邮箱</label><select id="mp-acc"></select></div>
    <button class="btn filled" id="mp-go">打开邮箱</button>
  </div>`;
  loadAccounts().then((accs) => {
    $("#mp-acc").innerHTML = accs.map((a) => `<option value="${esc(a.address)}">${esc(a.address)}${a.kind === "remote" ? "（远程拉取）" : ""}</option>`).join("");
  });
  $("#mp-go").onclick = async () => {
    try {
      await api("/api/mail/login", { method: "POST", body: { address: $("#mp-acc").value, password: "" } });
      renderMailShell(true);
    } catch (e) { toast(e.message); }
  };
}

/* ---------------- accounts ---------------- */
async function viewAccounts(el) {
  el.innerHTML = `<div class="empty">加载中…</div>`;
  const accs = await loadAccounts(true);
  const rows = accs.map((a) => {
    const remote = a.kind === "remote";
    return `<tr>
      <td><b>${esc(a.address)}</b><br><span class="muted">${esc(a.display_name || "")}</span></td>
      <td><span class="chip">${remote ? "远程" : "本地"}</span></td>
      <td>${remote ? esc((a.remote_proto || "").toUpperCase()) + " · " + esc(a.remote_host) + (a.remote_port ? ":" + a.remote_port : "") : "—"}</td>
      <td>${remote ? (a.remote_target ? esc(a.remote_target) : "<span class='muted'>未指定</span>") : "—"}</td>
      <td>${remote ? (a.last_fetch_at ? new Date(a.last_fetch_at * 1000).toLocaleString("zh-CN") : "从未") + " " + (a.last_fetch_ok ? '<span class="chip ok">正常</span>' : `<span class="chip err">${esc(a.last_error || "失败")}</span>`) : "—"}</td>
      <td class="flex">
        ${remote ? `<button class="btn small text" data-f="viewmail" data-id="${a.id}">${I.mail} 查看邮件</button>` : ""}
        ${remote ? `<button class="btn small text" data-f="fetch" data-id="${a.id}">${I.refresh} 立即拉取</button>` : ""}
        ${remote ? `<button class="btn small text" data-f="test" data-id="${a.id}">测试</button>` : ""}
        <button class="btn small text" data-f="edit" data-id="${a.id}">${I.edit}</button>
        ${!remote ? `<button class="btn small text" data-f="tokens" data-id="${a.id}">${I.key} 令牌</button>` : ""}
        ${!remote ? `<button class="btn small text" data-f="folders" data-id="${a.id}">${I.folder}</button>` : ""}
        <button class="btn small text" data-f="del" data-id="${a.id}" style="color:var(--md-error)">删除</button>
      </td>
    </tr>`;
  }).join("");
  el.innerHTML = `
  <div class="flex spread mb">
    <div class="flex">
      <button class="btn filled" id="a-local">${I.add} 本地邮箱</button>
      <button class="btn tonal" id="a-remote">${I.cloud} 远程拉取账号</button>
    </div>
  </div>
  <div class="card" style="padding:0 8px"><table class="tbl">
    <thead><tr><th>账号</th><th>类型</th><th>远程服务</th><th>投递目标</th><th>最近拉取</th><th>操作</th></tr></thead>
    <tbody>${rows || ""}</tbody></table></div>`;
  el.querySelectorAll("button[data-f]").forEach((b) => {
    b.onclick = async () => {
      const acc = accs.find((a) => a.id == b.dataset.id);
      if (b.dataset.f === "edit") return accountDialog(acc.kind, acc);
      if (b.dataset.f === "del") return openDialog("删除账号", `<p>确定删除 <b>${esc(acc.address)}</b>？其所有邮件将被移除。</p>`, async () => {
        await api("/api/accounts/" + acc.id, { method: "DELETE" }); loadAccounts(true); drawView("accounts"); toast("已删除");
      }, "删除");
      if (b.dataset.f === "fetch") {
        await api(`/api/accounts/${acc.id}/fetch`, { method: "POST" }); toast("已在后台开始拉取，稍后刷新查看");
      }
      if (b.dataset.f === "test") {
        const res = await api("/api/remote/test", { method: "POST", body: accToReq(acc) });
        toast((res.ok ? "✔ " : "✘ ") + res.detail);
      }
      if (b.dataset.f === "folders") return foldersDialog(acc);
      if (b.dataset.f === "tokens") return tokensDialog(acc);
      if (b.dataset.f === "viewmail") {
        try {
          await api("/api/mail/login", { method: "POST", body: { address: acc.address, password: "" } });
          renderMailShell(true);
        } catch (e) { toast(e.message); }
        return;
      }
    };
  });
  $("#a-local").onclick = () => accountDialog("local");
  $("#a-remote").onclick = () => accountDialog("remote");
}

function accToReq(a) {
  return { kind: a.kind, address: a.address, display_name: a.display_name, remote_proto: a.remote_proto, remote_host: a.remote_host, remote_port: a.remote_port, remote_tls: a.remote_tls, remote_user: a.remote_user, remote_pass: "", has_remote_pass: a.has_remote_pass, remote_folder: a.remote_folder, remote_target: a.remote_target, fetch_interval_min: a.fetch_interval_min, fetch_enabled: a.fetch_enabled, fetch_keep_on_server: a.fetch_keep_on_server };
}

async function accountDialog(kind, existing) {
  const isRemote = kind === "remote";
  const locals = (await loadAccounts()).filter((a) => a.kind === "local");
  const v = (k, d = "") => esc((existing && existing[k]) ?? d);
  let html = "";
  if (!isRemote) {
    html = `
    <div class="field"><label>邮箱地址</label><input id="e-addr" value="${v("address")}" placeholder="alice@${v("domain", "")}"></div>
    <div class="field"><label>显示名称</label><input id="e-name" value="${v("display_name")}"></div>
    <div class="field"><label>密码 ${existing ? "(留空不修改)" : ""}</label><input type="password" id="e-pw" placeholder="${existing ? "••••••" : "用于 SMTP/POP3/IMAP/网页登录"}"></div>
    <div class="field"><label>邮件签名（写邮件时自动附加到末尾，留空关闭）</label><textarea id="e-sig" style="min-height:80px;font-size:13px">${v("signature")}</textarea>
      <div class="hint">支持变量：{{date}} 日期 · {{time}} 时间 · {{datetime}} 日期时间 · {{from}} 显示名 · {{address}} 邮箱地址 · {{subject}} 主题。例：发自 {{address}} · {{datetime}}</div></div>`;
  } else {
    html = `
    <div class="field"><label>远程邮箱地址</label><input id="e-addr" value="${v("address")}" placeholder="someone@gmail.com"></div>
    <div class="row2">
      <div class="field"><label>协议</label><select id="e-proto"><option value="imap" ${existing && existing.remote_proto === "imap" ? "selected" : ""}>IMAP（推荐）</option><option value="pop3" ${existing && existing.remote_proto === "pop3" ? "selected" : ""}>POP3</option></select></div>
      <div class="field"><label>加密</label><select id="e-tls"><option value="ssl" ${(!existing || existing.remote_tls === "ssl") ? "selected" : ""}>SSL/TLS</option><option value="starttls" ${existing && existing.remote_tls === "starttls" ? "selected" : ""}>STARTTLS</option><option value="none" ${existing && existing.remote_tls === "none" ? "selected" : ""}>无</option></select></div>
    </div>
    <div class="row2">
      <div class="field"><label>服务器</label><input id="e-host" value="${v("remote_host")}" placeholder="imap.gmail.com"></div>
      <div class="field"><label>端口 (留空自动)</label><input type="number" id="e-port" value="${v("remote_port")}" placeholder="993"></div>
    </div>
    <div class="row2">
      <div class="field"><label>登录用户名</label><input id="e-user" value="${v("remote_user")}"></div>
      <div class="field"><label>密码${existing && v("has_remote_pass") ? "（已保存，留空保持不变）" : ""}</label><input type="password" id="e-pass" value="" placeholder="${existing && v("has_remote_pass") ? "••••••••" : ""}"></div>
    </div>
    <div class="field"><label>远程文件夹 (IMAP)</label><input id="e-rfolder" value="${v("remote_folder", "INBOX")}"></div>
    <div class="field"><label>投递到本地邮箱</label><select id="e-target">
      <option value="">独立存放（仅管理员「查看邮件」可见）</option>
      ${locals.map((a) => `<option value="${esc(a.address)}" ${existing ? (existing.remote_target === a.address ? "selected" : "") : (locals[0] && locals[0].address === a.address ? "selected" : "")}>${esc(a.address)}</option>`).join("")}
    </select><div class="hint">拉取的邮件将进入该本地邮箱的收件箱（过滤规则随后生效）。独立存放的邮件不进入任何本地邮箱，请用账号行的「查看邮件」打开。</div></div>
    <div class="row2">
      <div class="field"><label>拉取间隔(分钟)</label><input type="number" id="e-interval" value="${v("fetch_interval_min", 15)}"></div>
      <div class="field"><label>在服务器保留邮件</label><label class="switch"><input type="checkbox" id="e-keep" ${!existing || existing.fetch_keep_on_server ? "checked" : ""}><span class="track"></span></label></div>
    </div>
    <div class="field"><label>启用拉取</label><label class="switch"><input type="checkbox" id="e-enabled" ${!existing || existing.fetch_enabled ? "checked" : ""}><span class="track"></span></label></div>`;
  }
  openDialog(existing ? "编辑账号" : (isRemote ? "添加远程拉取账号" : "创建本地邮箱"), html, async () => {
    const get = (id) => $("#" + id) ? $("#" + id).value : undefined;
    const body = {
      kind, address: get("e-addr"), display_name: get("e-name"), password: get("e-pw") || "",
      signature: get("e-sig") ?? undefined,
      remote_proto: get("e-proto"), remote_host: get("e-host"), remote_port: parseInt(get("e-port")) || 0,
      remote_tls: get("e-tls"), remote_user: get("e-user"), remote_pass: get("e-pass") || "",
      remote_folder: get("e-rfolder") || "INBOX", remote_target: get("e-target") || "",
      fetch_interval_min: parseInt(get("e-interval")) || 15,
      fetch_enabled: $("#e-enabled") ? $("#e-enabled").checked : undefined,
      fetch_keep_on_server: $("#e-keep") ? $("#e-keep").checked : undefined,
    };
    if (!body.address) { toast("地址必填"); return false; }
    try {
      if (existing) await api("/api/accounts/" + existing.id, { method: "PUT", body });
      else await api("/api/accounts", { method: "POST", body });
      cachedAccounts = null; drawView("accounts"); toast("已保存");
    } catch (e) { toast(e.message); return false; }
  }, "保存");
}

// Access-token manager for a local mailbox: create (secret shown once),
// list, and revoke tokens used for external REST automation.
async function tokensDialog(acc) {
  const draw = async () => {
    const tokens = await api(`/api/accounts/${acc.id}/tokens`);
    const rows = tokens.map((t) => `<tr>
      <td>#${t.id} ${esc(t.label || "")}</td>
      <td class="muted">${new Date(t.created_at * 1000).toLocaleString("zh-CN")}</td>
      <td class="muted">${t.last_used_at ? new Date(t.last_used_at * 1000).toLocaleString("zh-CN") : "从未使用"}</td>
      <td>${t.revoked ? '<span class="chip err">已吊销</span>' : `<button class="btn small text" data-revoke="${t.id}" style="color:var(--md-error)">吊销</button>`}</td>
    </tr>`).join("");
    openDialog(`${I.key} 访问令牌 · ${esc(acc.address)}`, `
      <div class="muted mb">供外部程序调用本邮箱的 REST API（/api/mail/*）：请求头携带
      <span class="mono">Authorization: Bearer &lt;令牌&gt;</span>。令牌只在创建时显示一次，请立即保存；泄露或不用时随时吊销。</div>
      <div class="flex mb"><input id="tk-label" placeholder="令牌备注（如 CI 脚本）" style="flex:1;padding:10px;border:1px solid var(--md-outline);border-radius:8px;background:var(--md-surface-container-highest);color:var(--md-on-surface)">
      <button class="btn tonal" id="tk-add">${I.add} 生成令牌</button></div>
      <div id="tk-secret"></div>
      <table class="tbl"><thead><tr><th>令牌</th><th>创建时间</th><th>最近使用</th><th></th></tr></thead>
      <tbody>${rows || `<tr><td colspan="4" class="muted">暂无令牌</td></tr>`}</tbody></table>`,
      () => {}, "关闭");
    $("#tk-add").onclick = async () => {
      try {
        const res = await api(`/api/accounts/${acc.id}/tokens`, { method: "POST", body: { label: $("#tk-label").value } });
        $("#tk-secret").innerHTML = `<div class="dns-note" style="border:1px solid var(--md-outline-variant);border-radius:8px;padding:10px">
          ${I.info} 令牌已生成（仅此一次显示，请立即复制）：<br><span class="mono" style="word-break:break-all">${esc(res.secret)}</span>
          <button class="btn small text" id="tk-copy">复制</button></div>`;
        $("#tk-copy").onclick = () => { navigator.clipboard?.writeText(res.secret); toast("已复制"); };
      } catch (e) { toast(e.message); }
    };
    document.querySelectorAll("#dlg-body [data-revoke]").forEach((b) => {
      b.onclick = async () => {
        try { await api(`/api/accounts/${acc.id}/tokens/${b.dataset.revoke}`, { method: "DELETE" }); toast("已吊销"); $("#dlg").close(); tokensDialog(acc); }
        catch (e) { toast(e.message); }
      };
    });
  };
  await draw();
}

async function foldersDialog(acc) {  const folders = await api(`/api/accounts/${acc.id}/folders`);
  const rows = folders.map((f) => `<tr><td>${esc(f.name)}</td><td>${f.count}</td><td>${f.unseen ? `<span class="chip">${f.unseen} 未读</span>` : ""}</td>
    <td>${f.name !== "INBOX" ? `<button class="btn small text" data-del="${f.id}" style="color:var(--md-error)">删除</button>` : ""}</td></tr>`).join("");
  openDialog(`文件夹 · ${esc(acc.address)}`, `
    <table class="tbl"><thead><tr><th>名称</th><th>邮件</th><th></th><th></th></tr></thead><tbody>${rows}</tbody></table>
    <div class="flex mt"><input id="nf-name" placeholder="新文件夹名称" style="flex:1;padding:10px;border:1px solid var(--md-outline);border-radius:8px;background:var(--md-surface-container-highest);color:var(--md-on-surface)">
    <button class="btn tonal" id="nf-add">${I.add} 添加</button></div>`,
    () => {}, "关闭");
  $("#nf-add").onclick = async () => {
    try { await api(`/api/accounts/${acc.id}/folders`, { method: "POST", body: { name: $("#nf-name").value } }); toast("已添加"); $("#dlg").close(); foldersDialog(acc); }
    catch (e) { toast(e.message); }
  };
  document.querySelectorAll("[data-del]").forEach((b) => {
    b.onclick = async () => {
      try { await api(`/api/accounts/${acc.id}/folders/${b.dataset.del}`, { method: "DELETE" }); toast("已删除"); $("#dlg").close(); foldersDialog(acc); }
      catch (e) { toast(e.message); }
    };
  });
}

/* ---------------- filters ---------------- */
async function viewFilters(el) {
  el.innerHTML = `<div class="empty">加载中…</div>`;
  const accs = (await loadAccounts()).filter((a) => a.kind === "local");
  if (!accs.length) { el.innerHTML = `<div class="empty">请先创建本地邮箱账号</div>`; return; }
  const selId = localStorage.getItem("cp_filter_acc") || accs[0].id;
  el.innerHTML = `
  <div class="flex mb">
    <select id="f-acc" style="padding:10px;border-radius:8px;border:1px solid var(--md-outline);background:var(--md-surface-container-high);color:var(--md-on-surface)">
      ${accs.map((a) => `<option value="${a.id}" ${a.id == selId ? "selected" : ""}>${esc(a.address)}</option>`).join("")}
    </select>
    <button class="btn filled" id="f-add">${I.add} 新建规则</button>
    <span class="muted">规则按顺序匹配，第一条命中的规则生效。</span>
  </div>
  <div class="card" style="padding:0 8px"><table class="tbl" id="f-tbl"></table></div>`;
  $("#f-acc").onchange = () => { localStorage.setItem("cp_filter_acc", $("#f-acc").value); drawView("filters"); };
  $("#f-add").onclick = () => filterDialog($("#f-acc").value);
  const filters = await api(`/api/accounts/${$("#f-acc").value}/filters`);
  const ACTION = { move: "移动到", move_account: "移动到邮箱", markread: "标记已读", star: "加星标", trash: "移入回收站", discard: "直接丢弃" };
  const FIELD = { from: "发件人", to: "收件人", subject: "主题", body: "内容", any: "任意字段" };
  const OP = { contains: "包含", equals: "等于", starts: "开头是", ends: "结尾是", regex: "正则匹配" };
  $("#f-tbl").innerHTML = `
  <thead><tr><th>启用</th><th>名称</th><th>条件</th><th>动作</th><th>操作</th></tr></thead>
  <tbody>${filters.map((f) => `<tr>
    <td><label class="switch"><input type="checkbox" data-tg="${f.id}" ${f.enabled ? "checked" : ""}><span class="track"></span></label></td>
    <td><b>${esc(f.name)}</b></td>
    <td>${FIELD[f.cond_field] || f.cond_field} ${OP[f.cond_op] || f.cond_op} “${esc(f.cond_value)}”</td>
    <td>${ACTION[f.action] || f.action}${f.action_arg ? " → " + esc(f.action_arg) : ""}</td>
    <td><button class="btn small text" data-ed="${f.id}">${I.edit}</button>
        <button class="btn small text" data-dl="${f.id}" style="color:var(--md-error)">删除</button></td>
  </tr>`).join("") || `<tr><td colspan="5" class="empty">暂无规则</td></tr>`}</tbody>`;
  el.querySelectorAll("[data-tg]").forEach((c) => {
    c.onchange = async () => {
      const f = filters.find((x) => x.id == c.dataset.tg);
      f.enabled = c.checked;
      await api(`/api/filters/${f.account_id}/${f.id}`, { method: "PUT", body: f });
    };
  });
  el.querySelectorAll("[data-ed]").forEach((b) => {
    b.onclick = () => filterDialog($("#f-acc").value, filters.find((x) => x.id == b.dataset.ed));
  });
  el.querySelectorAll("[data-dl]").forEach((b) => {
    b.onclick = async () => {
      await api(`/api/filters/${$("#f-acc").value}/${b.dataset.dl}`, { method: "DELETE" });
      drawView("filters"); toast("已删除");
    };
  });
}

/* ---------------- factory reset (two-step confirm) ---------------- */
async function resetFlow() {
  let token = null, stats = null;
  // Step 1: warn + require typing RESET; prepares the one-time token.
  openDialog("强制重置 · 确认 1/2", `
    <p>即将把 cloudpost <b>恢复到未安装状态</b>：</p>
    <ul style="line-height:1.9;margin:8px 0 12px;padding-left:20px">
      <li>删除<b>全部邮箱账号</b>与密码</li>
      <li>删除<b>所有邮件</b>（含原始文件）</li>
      <li>删除过滤规则、发送队列、远程拉取状态</li>
      <li>删除安装配置（域名/端口/中继），回到安装向导</li>
    </ul>
    <div class="field"><label>输入 <span class="mono">RESET</span> 以继续</label><input id="rz-1" autocomplete="off" placeholder="RESET"></div>`,
    async () => {
      if ($("#rz-1").value.trim() !== "RESET") { toast("请输入 RESET 以确认"); return false; }
      try {
        stats = await api("/api/reset/prepare", { method: "POST", body: {} });
        token = stats.token;
      } catch (e) { toast(e.message); return false; }
      // Step 2: show the actual impact + second typed phrase.
      openDialog("强制重置 · 确认 2/2（最后机会）", `
        <p style="color:var(--md-error)"><b>此操作不可撤销。</b>当前将删除：</p>
        <p class="mono" style="background:var(--md-surface-container-highest);padding:10px;border-radius:8px">
          邮箱账号 ${stats.accounts} 个 · 邮件 ${stats.messages} 封 · 待发队列 ${stats.queue} 条
        </p>
        <p>协议服务（SMTP/POP3/IMAP）保持运行；完成后网页将回到安装向导。</p>
        <div class="field"><label>输入 <span class="mono">删除数据</span> 执行</label><input id="rz-2" autocomplete="off" placeholder="删除数据"></div>`,
        async () => {
          if ($("#rz-2").value.trim() !== "删除数据") { toast("请输入 删除数据 以执行"); return false; }
          try {
            await api("/api/reset/confirm", { method: "POST", body: { token, phrase: "RESET" } });
            toast("已重置，正在回到安装向导…");
            setTimeout(() => location.reload(), 1200);
          } catch (e) { toast(e.message); return false; }
        }, "永久删除");
    }, "继续");
}

function filterDialog(accID, existing) {
  const localsPromise = loadAccounts().then((as) => as.filter((a) => a.kind === "local"));
  const html = `
  <div class="field"><label>规则名称</label><input id="r-name" value="${esc(existing?.name || "")}"></div>
  <div class="row3">
    <div class="field"><label>条件字段</label><select id="r-field">
      ${["from:发件人", "to:收件人", "subject:主题", "body:内容", "any:任意"].map((x) => { const [v, l] = x.split(":"); return `<option value="${v}" ${existing?.cond_field === v ? "selected" : ""}>${l}</option>`; }).join("")}
    </select></div>
    <div class="field"><label>匹配方式</label><select id="r-op">
      ${["contains:包含", "equals:等于", "starts:开头是", "ends:结尾是", "regex:正则"].map((x) => { const [v, l] = x.split(":"); return `<option value="${v}" ${existing?.cond_op === v ? "selected" : ""}>${l}</option>`; }).join("")}
    </select></div>
    <div class="field"><label>匹配值</label><input id="r-val" value="${esc(existing?.cond_value || "")}"></div>
  </div>
  <div class="row2">
    <div class="field"><label>动作</label><select id="r-action">
      ${["move:移动到文件夹", "move_account:移动到邮箱(跨账号)", "markread:标记已读", "star:加星标", "trash:移入回收站", "discard:直接丢弃"].map((x) => { const [v, l] = x.split(":"); return `<option value="${v}" ${existing?.action === v ? "selected" : ""}>${l}</option>`; }).join("")}
    </select></div>
    <div class="field"><label>目标（文件夹名 或 本地邮箱地址）</label><input id="r-arg" value="${esc(existing?.action_arg || "")}" placeholder="Newsletter 或 bob@example.com"></div>
  </div>
  <div class="hint muted mb">例：远程拉取的邮件里「收件人 包含 @partner.example」→ 动作「移动到邮箱」→ 填本地邮箱地址，邮件将自动进入该邮箱的收件箱。</div>
  <div class="field"><label>启用</label><label class="switch"><input type="checkbox" id="r-en" ${!existing || existing.enabled ? "checked" : ""}><span class="track"></span></label></div>`;
  openDialog(existing ? "编辑规则" : "新建规则", html, async () => {
    const body = {
      name: $("#r-name").value, cond_field: $("#r-field").value, cond_op: $("#r-op").value,
      cond_value: $("#r-val").value, action: $("#r-action").value, action_arg: $("#r-arg").value, enabled: $("#r-en").checked,
    };
    if (!body.name || !body.cond_value) { toast("名称与匹配值必填"); return false; }
    if (body.action === "move_account") {
      const locals = await localsPromise;
      if (!locals.some((a) => a.address.toLowerCase() === body.action_arg.trim().toLowerCase())) {
        toast("「移动到邮箱」需要填写已存在的本地邮箱地址"); return false;
      }
    }
    try {
      if (existing) await api(`/api/filters/${accID}/${existing.id}`, { method: "PUT", body });
      else await api(`/api/accounts/${accID}/filters`, { method: "POST", body });
      drawView("filters"); toast("已保存");
    } catch (e) { toast(e.message); return false; }
  }, "保存");
}

/* ---------------- queue + settings ---------------- */
async function viewQueue(el) {
  const items = await api("/api/queue");
  el.innerHTML = `
  <div class="flex spread mb"><button class="btn tonal" id="q-flush">${I.refresh} 立即重试</button></div>
  <div class="card" style="padding:0 8px"><table class="tbl">
  <thead><tr><th>#</th><th>发件人</th><th>收件人</th><th>状态</th><th>尝试</th><th>下次尝试</th><th>错误</th></tr></thead>
  <tbody>${items.map((q) => `<tr><td>${q.id}</td><td>${esc(q.from)}</td><td>${esc(q.recipients)}</td>
    <td><span class="chip ${q.status === "sent" ? "ok" : q.status === "failed" ? "err" : ""}">${q.status}</span></td>
    <td>${q.attempts}</td><td>${q.next_try_at ? new Date(q.next_try_at * 1000).toLocaleString("zh-CN") : "—"}</td>
    <td class="muted">${esc((q.last_error || "").slice(0, 60))}</td></tr>`).join("") || `<tr><td colspan="7" class="empty">队列为空</td></tr>`}</tbody></table></div>`;
  $("#q-flush").onclick = async () => { await api("/api/queue/flush", { method: "POST" }); toast("已触发重试"); drawView("queue"); };
}

async function drawDKIM() {
  const box = $("#dkim-box");
  if (!box) return;
  let dk;
  try { dk = await api("/api/dkim"); } catch (e) { box.textContent = e.message; return; }
  box.innerHTML = `
  <div class="muted mb">对发自本域（${esc(dk.domain)}）的外发邮件附加 DKIM 签名，接收方通过 DNS 里的公钥验证，降低被判垃圾邮件的概率。私钥保存在本机配置中。</div>
  <div class="row2">
    <div class="field"><label>Selector（DNS 选择器）</label><input id="dk-selector" value="${esc(dk.selector)}" placeholder="mail"></div>
    <div class="field"><label>启用签名</label><label class="switch"><input type="checkbox" id="dk-enabled" ${dk.enabled ? "checked" : ""}><span class="track"></span></label></div>
  </div>
  <div class="flex mb">
    <button class="btn tonal" id="dk-gen">${I.key} 生成 2048 位密钥</button>
    ${dk.has_key ? `<span class="chip ok">已配置密钥</span>` : `<span class="chip">尚无密钥</span>`}
  </div>
  ${dk.dns_record ? `
  <div class="dns-panel">
    <div class="dns-title">${I.dns} 需要发布的 DNS 记录（TXT）</div>
    <div class="mono" style="word-break:break-all">${esc(dk.dns_record)}</div>
    <div class="dns-note">主机记录：<span class="mono">${esc(dk.dns_host)}</span> · 生成或更换密钥后请在 DNS 服务商同步更新此记录，生效可能需要几分钟到 48 小时。</div>
  </div>` : ""}
  <div class="field mt"><label>导入已有私钥（PEM，可选，留空保持不变）</label><textarea id="dk-pem" style="min-height:90px;font-family:monospace;font-size:12px" placeholder="-----BEGIN RSA PRIVATE KEY-----"></textarea></div>
  <button class="btn filled" id="dk-save">保存 DKIM 设置</button>`;
  $("#dk-gen").onclick = async () => {
    try {
      const sel = $("#dk-selector").value.trim();
      await api("/api/dkim/generate", { method: "POST", body: { selector: sel } });
      toast("密钥已生成，请发布下方 DNS 记录"); drawDKIM();
    } catch (e) { toast(e.message); }
  };
  $("#dk-save").onclick = async () => {
    try {
      await api("/api/dkim", { method: "POST", body: {
        enabled: $("#dk-enabled").checked,
        selector: $("#dk-selector").value.trim(),
        private_key: $("#dk-pem").value.trim(),
      } });
      toast("已保存"); drawDKIM();
    } catch (e) { toast(e.message); }
  };
}

async function viewSettings(el) {
  const s = await api("/api/settings");
  const eff = (k) => s["effective_" + k] || s[k.replace("_port", "") + "_port"] || "—";
  el.innerHTML = `
  <div class="card" style="max-width:760px">
    <h3>服务端口</h3>
    <div class="muted mb">保存后 SMTP/POP3/IMAP 立即在运行中的进程上重绑端口，无需重启；Web 端口修改后会自动切换到新地址。</div>
    <div class="row2">
      <div class="field"><label>Web 端口 <span class="muted">当前 ${eff("web_port")}</span></label><input type="number" id="st-web" value="${s.web_port || ""}" placeholder="8080"></div>
      <div class="field"><label>SMTP 端口 <span class="muted">当前 ${eff("smtp_port")}</span></label><input type="number" id="st-smtp" value="${s.smtp_port || ""}" placeholder="2525"></div>
      <div class="field"><label>POP3 端口 <span class="muted">当前 ${eff("pop3_port")}</span></label><input type="number" id="st-pop3" value="${s.pop3_port || ""}" placeholder="1110"></div>
      <div class="field"><label>IMAP 端口 <span class="muted">当前 ${eff("imap_port")}</span></label><input type="number" id="st-imap" value="${s.imap_port || ""}" placeholder="1143"></div>
    </div>
    <button class="btn filled" id="st-ports-save">应用端口</button>
  </div>
  <div class="card mt" style="max-width:760px">
    <h3>站点</h3>
    <div class="row2">
      <div class="field"><label>邮件域名</label><input id="st-domain" value="${esc(s.primary_domain)}"></div>
      <div class="field"><label>主机名</label><input id="st-host" value="${esc(s.hostname)}"></div>
    </div>
    <h3 class="mt">SMTP 中继（发往外部域名的邮件）</h3>
    <div class="muted mb">留空中继则直接投递到对方 MX 记录。</div>
    <div class="row2">
      <div class="field"><label>中继主机</label><input id="st-relay" value="${esc(s.relay_host)}"></div>
      <div class="field"><label>端口</label><input type="number" id="st-rport" value="${s.relay_port || 587}"></div>
      <div class="field"><label>用户名</label><input id="st-ruser" value="${esc(s.relay_user)}"></div>
      <div class="field"><label>密码</label><input type="password" id="st-rpass" value="${esc(s.relay_pass)}"></div>
    </div>
    <button class="btn filled" id="st-save">保存设置</button>
  </div>
  <div class="card mt" style="max-width:760px">
    <h3>DKIM 签名（外发邮件防伪造）</h3>
    <div id="dkim-box" class="muted">加载中…</div>
  </div>
  <div class="card mt" style="max-width:760px">
    <h3>修改管理员密码</h3>
    <div class="row2">
      <div class="field"><label>当前密码</label><input type="password" id="pw-old"></div>
      <div class="field"><label>新密码</label><input type="password" id="pw-new"></div>
    </div>
    <button class="btn tonal" id="pw-go">修改密码</button>
  </div>
  <div class="card mt danger-zone" style="max-width:760px">
    <h3 style="color:var(--md-error)">${I.trash} 危险区</h3>
    <div class="muted mb">强制重置会<b>永久删除</b>所有邮箱账号、全部邮件、过滤规则、发送队列与安装配置，服务器将立即回到安装向导模式。此操作不可撤销，协议服务保持运行。</div>
    <button class="btn filled btn-danger" id="st-reset">强制重置项目…</button>
  </div>`;
  $("#st-reset").onclick = resetFlow;
  drawDKIM();
  $("#st-ports-save").onclick = async () => {
    const ports = {};
    for (const [id, key] of [["st-web", "web_port"], ["st-smtp", "smtp_port"], ["st-pop3", "pop3_port"], ["st-imap", "imap_port"]]) {
      const v = parseInt($("#" + id).value);
      if (v > 0) ports[key] = v;
    }
    if (!Object.keys(ports).length) return toast("未修改任何端口");
    try {
      const res = await api("/api/settings", { method: "POST", body: ports });
      const errs = res.errors || [];
      const appliedN = Object.keys(res.applied || {}).length;
      if (res.web_new_port) {
        const url = location.protocol + "//" + location.hostname + ":" + res.web_new_port;
        toast("Web 端口已切换，2 秒后跳转 " + url + (errs.length ? "；" + errs.join("；") : ""));
        setTimeout(() => { location.href = url; }, 2000);
      } else if (appliedN) {
        toast("已即时生效：" + Object.keys(res.applied).join(", ") + (errs.length ? "；" + errs.join("；") : ""));
      } else if (errs.length) {
        toast(errs.join("；"));
      } else {
        toast("已保存");
      }
      drawView("settings");
    } catch (e) { toast(e.message); }
  };
  $("#st-save").onclick = async () => {
    try {
      await api("/api/settings", { method: "POST", body: {
        primary_domain: $("#st-domain").value, hostname: $("#st-host").value,
        relay_host: $("#st-relay").value, relay_port: parseInt($("#st-rport").value) || 0,
        relay_user: $("#st-ruser").value, relay_pass: $("#st-rpass").value,
      } });
      toast("已保存");
    } catch (e) { toast(e.message); }
  };
  $("#pw-go").onclick = async () => {
    try { await api("/api/password", { method: "POST", body: { old: $("#pw-old").value, new: $("#pw-new").value } }); toast("密码已修改"); }
    catch (e) { toast(e.message); }
  };
}

/* ---------------- webmail ---------------- */
function renderMailShell(fromAdmin = false) {
  let acc = null;
  api("/api/mail/me").then((a) => { acc = a; draw(); });
  let folders = [], curFolder = null, msgs = [], curMsg = null;
  const state = { offset: 0, total: 0, q: "" };
  function draw() {
    app.innerHTML = `
    <div class="main">
      <div class="topbar mail-topbar">
        <button class="icon-btn" id="m-back" title="${fromAdmin ? "返回控制台" : "退出"}">${fromAdmin ? I.back : I.logout}</button>
        <h2>${I.mail} <span class="mail-addr">${esc(acc.address)}</span></h2>
        <div class="search-field" id="m-search-wrap">
          <span class="ic">${I.search}</span>
          <input id="m-search" placeholder="搜索主题/发件人…">
        </div>
        <button class="icon-btn" id="m-refresh" title="刷新">${I.refresh}</button>
        <button class="icon-btn" id="m-theme" title="切换亮暗色">${I.dark}</button>
        <button class="icon-btn" id="m-sched" title="定时箱（延时发送）">${I.queue}</button>
        <button class="btn filled small" id="m-compose">${I.edit} 写邮件</button>
      </div>
      <div class="mail-layout">
        <div class="folder-list" id="m-folders"></div>
        <div class="msg-list" id="m-list"></div>
        <div class="msg-view" id="m-view"><div class="empty">${I.mail}<br>选择一封邮件查看</div></div>
      </div>
    </div>`;
    $("#m-back").onclick = async () => {
      await api("/api/mail/logout", { method: "POST" }).catch(() => {});
      fromAdmin ? renderConsole("mail") : renderLogin();
    };
    $("#m-theme").onclick = themeToggle;
    $("#m-sched").onclick = scheduledDialog;
    $("#m-refresh").onclick = refreshFolders;
    $("#m-compose").onclick = composeDialog;
    $("#m-search").addEventListener("keydown", (e) => {
      if (e.key === "Enter") { state.q = $("#m-search").value; state.offset = 0; loadMsgs(true); }
    });
    refreshFolders();
  }
  async function refreshFolders() {
    folders = await api("/api/mail/folders");
    $("#m-folders").innerHTML = folders.map((f) => `
      <button class="folder-item ${curFolder == f.id ? "active" : ""}" data-f="${f.id}">
        <span>${folderIcon(f.name)}</span>${esc(f.name)}
        ${f.unseen ? `<span class="badge">${f.unseen}</span>` : ""}
      </button>`).join("");
    $("#m-folders").querySelectorAll("[data-f]").forEach((b) => {
      b.onclick = () => {
        curFolder = parseInt(b.dataset.f); curMsg = null; state.offset = 0; state.q = ""; $("#m-search").value = "";
        document.querySelector(".mail-layout")?.classList.remove("show-msg");
        refreshFolders(); loadMsgs(true);
      };
    });
    if (!curFolder && folders.length) { curFolder = folders[0].id; $("#m-folders").querySelector(`[data-f="${curFolder}"]`)?.classList.add("active"); loadMsgs(true); }
  }
  function folderIcon(name) {
    const n = name.toLowerCase();
    if (n === "inbox") return I.inbox;
    if (n === "sent") return I.send;
    if (n === "drafts") return I.draft;
    if (n === "trash") return I.trash;
    return I.folder;
  }
  async function loadMsgs(reset) {
    if (!curFolder) return;
    let data;
    if (state.q) data = await api(`/api/mail/folders/${curFolder}/messages`, { method: "POST", body: { field: "", query: state.q } });
    else data = await api(`/api/mail/folders/${curFolder}/messages?offset=${state.offset}&limit=50`);
    msgs = reset ? (data.messages || []) : msgs.concat(data.messages || []);
    state.total = data.total;
    const unread = (m) => !m.flags?.includes("\\Seen");
    $("#m-list").innerHTML = `
    <div class="list-head flex spread">
      <span class="muted">${state.total} 封</span>
      <button class="btn small text" id="m-readall">全部已读</button>
    </div>
    ${msgs.map((m) => `
      <div class="msg-item ${curMsg === m.id ? "active" : ""} ${unread(m) ? "unseen" : ""}" data-m="${m.id}">
        <div class="top"><span class="from">${esc(m.from_name || m.from_addr || "(无发件人)")}</span><span class="date">${fmtDate(m.sent_at)}</span></div>
        <div class="subj">${m.flags?.includes("\\Flagged") ? I.star + " " : ""}${esc(m.subject || "(无主题)")}</div>
        <div class="snip">${esc(m.snippet)}</div>
      </div>`).join("")}
    ${msgs.length < state.total && !state.q ? `<div style="padding:12px;text-align:center"><button class="btn text" id="m-more">加载更多</button></div>` : ""}`;
    $("#m-list").querySelectorAll("[data-m]").forEach((d) => {
      d.onclick = () => openMsg(parseInt(d.dataset.m));
    });
    $("#m-readall").onclick = async () => { await api(`/api/mail/folders/${curFolder}/readall`, { method: "POST" }); refreshFolders(); loadMsgs(true); };
    $("#m-more") && ($("#m-more").onclick = () => { state.offset += 50; loadMsgs(false); });
  }
  async function openMsg(id, allowRemote = false) {
    curMsg = id;
    const data = await api(`/api/mail/folders/${curFolder}/messages/${id}${allowRemote ? "?allow_remote=1" : ""}`);
    const m = data.message;
    const mailLayout = document.querySelector(".mail-layout");
    mailLayout && mailLayout.classList.add("show-msg");
    $("#m-view").innerHTML = `
    <h3>${esc(m.subject || "(无主题)")}</h3>
    <div class="msg-meta">
      <button class="icon-btn m-back-msg" id="m-back-msg" title="返回列表">${I.back}</button>
      <span class="avatar">${esc((m.from_name || m.from_addr || "?")[0].toUpperCase())}</span>
      <div><b>${esc(m.from_name || "")}</b> &lt;${esc(m.from_addr)}&gt;<br>
      <span class="muted">收件人: ${esc((m.to_addrs || []).join(", "))} · ${new Date(m.sent_at * 1000).toLocaleString("zh-CN")}</span></div>
      <div style="flex:1"></div>
      <button class="icon-btn" data-a="star" title="${m.flags?.includes("\\Flagged") ? "取消星标" : "加星标"}">${m.flags?.includes("\\Flagged") ? I.star : I.starO}</button>
      <button class="icon-btn" data-a="unread" title="标记未读">${I.mail}</button>
      <button class="icon-btn" data-a="move" title="移动到…">${I.folder}</button>
      <button class="icon-btn" data-a="del" title="删除">${I.trash}</button>
      <a class="btn small text" href="/api/mail/folders/${curFolder}/messages/${id}/raw" download="${id}.eml">原始邮件</a>
    </div>
    ${data.has_remote_content && !data.remote_allowed ? `
    <div class="remote-warn" id="m-remote-warn">
      ${I.info} <span>为防止跟踪和攻击，此邮件的远程内容已阻止加载。</span>
      <button class="btn small tonal" id="m-allow-remote">显示远程内容</button>
    </div>` : ""}
    ${(data.attachments || []).length ? `
    <div class="attach-bar">
      <span class="muted">${I.paperclip} ${data.attachments.length} 个附件 · 默认不加载，点击下载</span>
      <button class="btn small tonal" id="m-att-load">显示附件</button>
      <div id="m-att-list"></div>
    </div>` : ""}
    <div class="msg-body" id="m-body"></div>`;
    const allowBtn = $("#m-allow-remote");
    allowBtn && (allowBtn.onclick = () => openMsg(id, true));
    $("#m-back-msg").onclick = () => {
      mailLayout && mailLayout.classList.remove("show-msg");
      curMsg = null;
      $("#m-view").innerHTML = `<div class="empty">选择一封邮件查看</div>`;
    };
    const attBtn = $("#m-att-load");
    attBtn && (attBtn.onclick = () => {
      $("#m-att-list").innerHTML = (data.attachments || []).map((a) => `
        <a class="att-chip" href="/api/mail/folders/${curFolder}/messages/${id}/attachments/${a.part}" download="${esc(a.filename)}" title="下载 ${esc(a.filename)}">
          ${I.paperclip} ${esc(a.filename)} <span class="muted">${fmtSize(a.size)}</span>
        </a>`).join("");
      attBtn.style.display = "none";
    });
    const body = $("#m-body");
    if (data.html) {
      const f = document.createElement("iframe");
      // allow-same-origin (WITHOUT allow-scripts) lets the app measure the
      // mail body height; scripts/forms/popups stay blocked by the sandbox.
      f.setAttribute("sandbox", "allow-same-origin");
      body.appendChild(f);
      f.srcdoc = data.html;
      f.style.width = "100%"; f.style.border = "none"; f.style.minHeight = "320px";
      const fit = () => {
        try {
          const d = f.contentDocument;
          const h = Math.max(d.body?.scrollHeight || 0, d.documentElement?.scrollHeight || 0);
          if (h > 0) f.style.height = (h + 40) + "px";
        } catch { /* sandboxed; keep min-height */ }
      };
      f.onload = () => { fit(); setTimeout(fit, 600); setTimeout(fit, 1800); };
    } else {
      body.textContent = data.text || "(空邮件)";
    }
    $("#m-view").querySelectorAll("[data-a]").forEach((b) => {
      b.onclick = async () => {
        const a = b.dataset.a;
        if (a === "star") await api(`/api/mail/folders/${curFolder}/messages/${id}/flags`, { method: "POST", body: { mode: m.flags?.includes("\\Flagged") ? "remove" : "add", flags: ["\\Flagged"] } });
        if (a === "unread") await api(`/api/mail/folders/${curFolder}/messages/${id}/flags`, { method: "POST", body: { mode: "remove", flags: ["\\Seen"] } });
        if (a === "del") {
          await api(`/api/mail/folders/${curFolder}/messages/${id}/delete`, { method: "POST" });
          toast("已删除"); loadMsgs(true);
          document.querySelector(".mail-layout")?.classList.remove("show-msg");
          curMsg = null;
          $("#m-view").innerHTML = `<div class="empty">选择一封邮件查看</div>`;
          refreshFolders(); return;
        }
        if (a === "move") {
          openDialog("移动到文件夹", `<div class="field"><label>文件夹名称</label><input id="mv-name" value="Trash"></div>`, async () => {
            await api(`/api/mail/folders/${curFolder}/messages/${id}/move`, { method: "POST", body: { folder: $("#mv-name").value } });
            loadMsgs(true); refreshFolders(); toast("已移动");
          }, "移动");
          return;
        }
        loadMsgs(true);
      };
    });
    // Opening a mail marks it read: update the list item in place (keeps
    // scroll position) and refresh folder unread badges.
    const item = $(`#m-list [data-m="${id}"]`);
    if (item) item.classList.remove("unseen");
    refreshFolders();
  }
  function composeDialog() {
    const cfgDomain = acc.address.split("@")[1];
    const atts = []; // {filename, content(b64)}
    const html = `
      <div class="field"><label>收件人</label><input id="c-to" placeholder="name@example.com, 多个用逗号分隔"></div>
      <div class="field"><label>抄送</label><input id="c-cc"></div>
      <div class="field"><label>主题</label><input id="c-subj"></div>
      <div class="field"><label>正文</label><textarea id="c-body" style="min-height:160px"></textarea></div>
      <div class="field"><label>附件</label><input type="file" id="c-att" multiple style="font-size:13px"><div id="c-att-list" class="muted" style="font-size:12px;margin-top:6px"></div></div>
      <div class="row2">
        <div class="field"><label>延时发送（分钟，0=立即）</label><input type="number" id="c-delay" value="0" min="0"></div>
        <div class="field"><label>&nbsp;</label><span class="muted" style="font-size:12px">延时期内可在「定时箱」撤回</span></div>
      </div>
      ${acc.signature ? `<div class="muted" style="font-size:12px">${I.info} 已启用签名（可在账号编辑中修改）</div>` : ""}
      <div class="muted">外部收件人通过 ${cfgDomain ? "SMTP 队列" : "SMTP"} 投递（直投 MX 或中继）；同域名收件人即时送达。</div>`;
    openDialog("写邮件", html, async () => {
      const delay = parseInt($("#c-delay").value) || 0;
      try {
        const res = await api("/api/mail/compose", { method: "POST", body: {
          to: $("#c-to").value, cc: $("#c-cc").value, subject: $("#c-subj").value, body: $("#c-body").value,
          attachments: atts, delay_min: delay,
        } });
        toast(res.scheduled ? "已定时，可在「定时箱」撤回" : "已发送");
        refreshFolders();
      } catch (e) { toast(e.message); return false; }
    }, "发送");
    // Wire attachments after the dialog is in the DOM.
    $("#c-att").onchange = () => {
      const files = Array.from($("#c-att").files || []);
      let pending = files.length;
      if (!pending) { atts.length = 0; $("#c-att-list").textContent = ""; return; }
      atts.length = 0;
      files.forEach((f) => {
        if (f.size > 20 * 1048576) { toast(`${f.name} 超过 20MB`); pending--; return; }
        const fr = new FileReader();
        fr.onload = () => {
          atts.push({ filename: f.name, content: String(fr.result).split(",")[1] || "" });
          if (--pending === 0) $("#c-att-list").textContent = atts.map((a) => a.filename).join("、");
        };
        fr.readAsDataURL(f);
      });
    };
  }

  // Pending delayed sends with cancel (撤回).
  async function scheduledDialog() {
    const draw = async () => {
      const list = await api("/api/mail/scheduled");
      const rows = list.map((m) => `<tr>
        <td>${esc(m.subject || "(无主题)")}</td>
        <td class="muted">${esc(m.recipients.join(", "))}</td>
        <td>${new Date(m.send_at * 1000).toLocaleString("zh-CN")}</td>
        <td><button class="btn small text" data-cancel="${m.id}" style="color:var(--md-error)">撤回</button></td>
      </tr>`).join("");
      openDialog(`${I.queue} 定时箱（延时发送）`, `
        <table class="tbl"><thead><tr><th>主题</th><th>收件人</th><th>发送时间</th><th></th></tr></thead>
        <tbody>${rows || `<tr><td colspan="4" class="muted">暂无定时邮件</td></tr>`}</tbody></table>`,
        () => {}, "关闭");
      document.querySelectorAll("#dlg-body [data-cancel]").forEach((b) => {
        b.onclick = async () => {
          try { await api(`/api/mail/scheduled/${b.dataset.cancel}`, { method: "DELETE" }); toast("已撤回，邮件不会发送"); $("#dlg").close(); scheduledDialog(); }
          catch (e) { toast(e.message); }
        };
      });
    };
    await draw();
  }
}

boot();
