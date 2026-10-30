import { escapeHTML as esc } from "./markdown.js";

// All operations cross the closed Go bridge; the page never receives service credentials.
export function createToolsUI({ bridge, openDialog, field, dialogActions, getBots, notify, run }) {
  const call = (kind, extra = {}) => bridge("Phase4", { kind, ...extra });
  const body = () => document.getElementById("dialog-body");
  const button = (id, label, danger = false) => `<button type="button" class="${danger ? "danger" : "secondary"}" data-tools="${id}">${label}</button>`;
  const listen = (handlers) => {
    body().onclick = run(async (event) => {
      const target = event.target.closest("[data-tools]");
      if (!target || target.disabled) return;
      const fn = handlers[target.dataset.tools];
      if (!fn) return;
      target.disabled = true;
      try { await fn(target); } finally { target.disabled = false; }
    });
  };
  const botScope = (ids = []) => `<p class="form-help">可使用的伙伴（不选则全部）</p>${getBots().map((b) => `<label class="check-label"><input name="bot_ids" type="checkbox" value="${esc(b.id)}" ${ids.includes(b.id) ? "checked" : ""}>${esc(b.name)}</label>`).join("")}`;
  const enabled = (value) => `<label class="check-label"><input type="checkbox" name="enabled" ${value ? "checked" : ""}>启用</label>`;

  async function home() {
    openDialog("工具与自动化", `<div class="tool-sections"><div class="tool-section"><h3>MCP 工具服务器</h3><p class="form-help">连接本地命令或远程服务，为伙伴添加工具。</p>${button("mcp", "管理 MCP 服务器")}</div><div class="tool-section"><h3>连接应用</h3><p class="form-help">通过 Composio 授权应用，密钥保存在系统钥匙串。</p>${button("composio", "配置 Composio")}</div><div class="tool-section"><h3>日程与 Webhook</h3><p class="form-help">按时间或外部请求运行任务，查看每次运行的结果。</p>${button("routines", "管理自动化")}</div></div>`);
    listen({ mcp: servers, composio, routines });
  }

  async function composio() {
    const state = await call("composio"), config = state.settings || {};
    openDialog("Composio 应用连接", `<form><p class="connection-state">API 密钥：${state.configured ? "已配置" : "未配置"}</p>${enabled(config.enabled)}${field("用户 ID", "user_id", config.user_id, { required: true })}${field("授权配置 ID", "auth_config_id", config.auth_config_id, { placeholder: "Composio 中创建的 auth config ID" })}${field("MCP 地址", "mcp_url", config.mcp_url, { placeholder: "https://mcp.composio.dev/…" })}${botScope(config.bot_ids)}<div class="tool-buttons">${button("key", state.configured ? "更换 API 密钥" : "设置 API 密钥")}${state.configured ? button("delete-key", "移除密钥", true) : ""}${button("connect", "授权应用")}${button("accounts", "查看连接")}</div><p class="form-help">先保存配置，再授权应用。MCP 地址使用 Composio 控制台创建的服务器地址。</p>${dialogActions("保存配置")}</form>`, async (data) => {
      await call("save-composio", { config: { enabled: data.has("enabled"), user_id: data.get("user_id"), auth_config_id: data.get("auth_config_id"), mcp_url: data.get("mcp_url"), bot_ids: data.getAll("bot_ids") } });
      notify("Composio 配置已保存");
    });
    listen({
      key: () => {
        openDialog("设置 Composio API 密钥", `<form><label class="form-field"><span>API 密钥</span><input type="password" name="key" required autocomplete="off" spellcheck="false"></label><p class="form-help">保存后页面只显示“已配置”。</p>${dialogActions("保存到系统钥匙串")}</form>`, async (data, form) => {
          const key = data.get("key");
          form.elements.key.value = "";
          await call("save-composio-key", { key });
          await composio();
        });
      },
      "delete-key": async () => { await call("delete-composio-key"); await composio(); },
      connect: async () => { const link = await call("connect-composio"); await bridge("OpenLink", link.redirect_url); },
      accounts: async () => {
        const accounts = await call("composio-accounts") || [];
        openDialog("已连接的应用", `<div class="tool-list">${accounts.map((a) => `<div class="tool-item"><strong>${esc(a.toolkit?.slug || a.id)}</strong><span class="form-help">${esc(a.status)}</span></div>`).join("") || '<p class="form-help">还没有应用连接。</p>'}</div>${button("back", "返回连接设置")}`);
        listen({ back: composio });
      },
    });
  }

  async function servers() {
    const items = await call("mcp-servers") || [];
    openDialog("MCP 工具服务器", `<p class="form-help">服务器提供的工具会加入所选伙伴的后续轮次。</p><div class="tool-list">${items.map((s) => `<div class="tool-item"><div><strong>${esc(s.name)}</strong><p class="form-help">${esc(s.transport)} · ${s.enabled ? "启用" : "暂停"}</p></div>${button("edit:" + esc(s.id), "编辑")}${button("delete:" + esc(s.id), "删除", true)}</div>`).join("") || '<p class="form-help">尚未添加服务器。</p>'}</div>${button("add", "添加服务器")}`);
    const handlers = { add: () => editServer() };
    for (const s of items) {
      handlers["edit:" + s.id] = () => editServer(s);
      handlers["delete:" + s.id] = async () => { await call("delete-mcp-server", { id: s.id }); await servers(); };
    }
    listen(handlers);
  }
  async function editServer(s = {}) {
    openDialog(s.id ? "编辑 MCP 服务器" : "添加 MCP 服务器", `<form>${field("名称", "name", s.name, { required: true })}${enabled(s.enabled !== false)}${field("连接方式", "transport", s.transport || "stdio", { select: [["stdio", "本地命令（stdio）"], ["http", "HTTP"], ["sse", "SSE"]] })}<div data-stdio>${field("命令", "command", s.command, { placeholder: "例如 /usr/local/bin/my-mcp" })}${field("参数（每行一个）", "args", (s.args || []).join("\n"), { textarea: true })}${field("环境变量名称映射（JSON）", "env", JSON.stringify(s.env || {}, null, 2), { textarea: true })}</div><div data-remote>${field("服务器地址", "url", s.url, { placeholder: "https://example.com/mcp" })}${field("请求头环境变量映射（JSON）", "headers", JSON.stringify(s.headers || {}, null, 2), { textarea: true })}</div>${botScope(s.bot_ids)}${dialogActions("保存服务器")}</form>`, async (data) => {
      const transport = data.get("transport");
      const config = { id: s.id || "", name: data.get("name"), enabled: data.has("enabled"), transport, bot_ids: data.getAll("bot_ids") };
      if (transport === "stdio") Object.assign(config, { command: data.get("command"), args: data.get("args").split("\n").filter(Boolean), env: JSON.parse(data.get("env") || "{}") });
      else Object.assign(config, { url: data.get("url"), headers: JSON.parse(data.get("headers") || "{}") });
      await call("save-mcp-server", { config });
      await servers();
    });
    const form = body().querySelector("form");
    const update = () => {
      form.querySelector("[data-stdio]").hidden = form.elements.transport.value !== "stdio";
      form.querySelector("[data-remote]").hidden = form.elements.transport.value === "stdio";
    };
    form.elements.transport.onchange = update;
    update();
  }

  async function routines() {
    const items = await call("routines") || [];
    openDialog("日程与 Webhook", `<div class="tool-list">${items.map((r) => `<div class="tool-item"><div><strong>${esc(r.name)}</strong><p class="form-help">${esc(r.schedule?.kind || r.kind || "")} · ${r.enabled ? "启用" : "暂停"}</p></div>${button("edit:" + esc(r.id), "编辑")}${button("run:" + esc(r.id), "运行")}${button("history:" + esc(r.id), "历史")}${button("delete:" + esc(r.id), "删除", true)}</div>`).join("") || '<p class="form-help">还没有自动任务。</p>'}</div><div class="tool-buttons">${button("add", "创建任务")}${button("webhook", "Webhook 接收器")}</div>`);
    const handlers = { add: () => editRoutine(), webhook };
    for (const r of items) {
      handlers["edit:" + r.id] = () => editRoutine(r);
      handlers["delete:" + r.id] = async () => { await call("delete-routine", { id: r.id }); await routines(); };
      handlers["run:" + r.id] = async () => { await call("run-routine", { id: r.id }); notify("运行请求已受理"); };
      handlers["history:" + r.id] = () => history(r.id);
    }
    listen(handlers);
  }
  async function editRoutine(r = {}) {
    // The service validates times and resolves the bot's normal thread and permissions.
    openDialog(r.id ? "编辑自动任务" : "创建自动任务", `<form>${field("任务名称", "name", r.name, { required: true })}${enabled(r.enabled !== false)}${field("执行伙伴", "bot_id", r.bot_id || getBots()[0]?.id, { select: getBots().map((b) => [b.id, b.name]) })}${field("任务指令", "prompt", r.prompt, { textarea: true, required: true })}${field("触发方式", "kind", r.kind || "once", { select: [["once", "一次性"], ["weekdays", "每个工作日"], ["interval", "按间隔"], ["webhook", "Webhook"]] })}${field("执行时间", "at", r.at, { placeholder: "RFC3339，例如 2026-11-01T09:00:00+08:00" })}${field("工作日时间", "time", r.time || "09:00", { placeholder: "09:00" })}${field("时区", "timezone", r.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone)}${field("间隔秒数", "interval_seconds", r.interval_seconds || 3600)}<p class="form-help">上一次任务未完成时跳过本次触发。工作日为周一至周五。</p>${r.id && r.kind === "webhook" ? `<p class="form-help">POST http://127.0.0.1:8800/hooks/${esc(r.id)}，JSON 请求体：{"text":"可选补充信息"}</p>` : ""}${dialogActions("保存任务")}</form>`, async (data) => {
      const config = { id: r.id || "", name: data.get("name"), enabled: data.has("enabled"), bot_id: data.get("bot_id"), prompt: data.get("prompt"), kind: data.get("kind") };
      if (config.kind === "once") config.at = new Date(data.get("at")).toISOString();
      if (config.kind === "weekdays") Object.assign(config, { time: data.get("time"), timezone: data.get("timezone") });
      if (config.kind === "interval") config.interval_seconds = Number(data.get("interval_seconds"));
      await call("save-routine", { config });
      await routines();
    });
    const form = body().querySelector("form");
    form.elements.at.type = "datetime-local";
    const at = r.at && !r.at.startsWith("0001") ? new Date(r.at) : new Date(Date.now() + 3600000);
    form.elements.at.value = new Date(at.getTime() - at.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
    form.elements.time.type = "time";
    form.elements.interval_seconds.type = "number";
    form.elements.interval_seconds.min = "1";
    const update = () => {
      for (const [name, kind] of [["at", "once"], ["time", "weekdays"], ["timezone", "weekdays"], ["interval_seconds", "interval"]]) {
        const input = form.elements[name];
        input.closest("label").hidden = form.elements.kind.value !== kind;
        input.required = form.elements.kind.value === kind;
      }
    };
    form.elements.kind.onchange = update;
    update();
  }
  async function history(id) {
    const rows = await call("routine-history", { id }) || [];
    openDialog("任务运行历史", `<div class="tool-list">${rows.map((r) => `<div class="tool-item"><div><strong>${esc(r.status)}</strong><p class="form-help">${esc(new Date(r.started_at || r.at).toLocaleString())}</p><p>${esc(r.error || r.result || "")}</p></div></div>`).join("") || '<p class="form-help">还没有运行记录。</p>'}</div><div class="tool-buttons">${button("refresh", "刷新")}${button("back", "返回任务列表")}</div>`);
    listen({ refresh: () => history(id), back: routines });
  }
  async function webhook() {
    const state = await call("webhook-state");
    openDialog("Webhook 接收器", `<p class="connection-state">${state.running ? "运行中" : "未开启"} · 密钥${state.configured ? "已配置" : "未配置"}</p>${state.error ? `<p class="form-help">${esc(state.error)}</p>` : ""}<p class="form-help">首次使用请先生成密钥，再开启接收器。</p><p class="form-help">地址：${esc(state.url || "http://127.0.0.1:8800")}。仅本机访问，请求需携带 Bearer 密钥。</p><div class="tool-buttons">${button("enable", "开启接收器")}${button("disable", "关闭接收器")}${button("rotate", "生成新密钥")}</div>`);
    listen({
      enable: async () => { await call("enable-webhook"); await webhook(); },
      disable: async () => { await call("disable-webhook"); await webhook(); },
      rotate: async () => {
        const value = await call("rotate-webhook-key");
        openDialog("Webhook 密钥", `<p class="form-help">此密钥仅展示一次，请妥善保存。</p><pre class="secret-display">${esc(value.token)}</pre>${button("copy", "复制密钥")}${button("back", "返回接收器")}`);
        listen({ copy: async () => { await bridge("Copy", value.token); notify("密钥已复制"); }, back: webhook });
      },
    });
  }
  return { home };
}
