import { escapeHTML as esc } from "./markdown.js";

// Preview polling lasts only while this dialog remains open. No credentials or
// remote desktop URL are placed in an image element or iframe.
export function createComputerUI({ bridge, openDialog, field, dialogActions, getBots, getConversation, notify, run }) {
  const call = (kind, extra = {}) => bridge("Computer", { kind, ...extra });
  const body = () => document.getElementById("dialog-body");
  let generation = 0;
  function shutdown() { generation++; }
  async function home() {
    shutdown();
    const items = await call("computers") || [];
    const bots = getBots();
    openDialog("伙伴的电脑", `<p class="form-help">选择这台电脑即可使用内置控制，也可连接已有的云桌面或 VM。电脑默认关闭，伙伴操作仍遵循工具审批。</p><button type="button" class="secondary" data-host>本机驱动与系统权限</button><div class="tool-list">${bots.map((b) => { const c = items.find((v) => v.bot_id === b.id); return `<div class="tool-item"><div><strong>${esc(b.name)}</strong><p class="form-help">${c ? `${esc(c.provider)} · ${c.enabled ? "启用" : "关闭"}` : "尚未配置"}</p></div><button type="button" class="secondary" data-config="${esc(b.id)}">配置</button>${c?.enabled ? `<button type="button" class="secondary" data-preview="${esc(b.id)}">打开电脑</button>` : ""}</div>`; }).join("") || '<p class="form-help">先创建一位伙伴。</p>'}</div>`);
    body().onclick = run(async (e) => {
      if (e.target.closest("[data-host]")) await host();
      const config = e.target.closest("[data-config]"), preview = e.target.closest("[data-preview]");
      if (config) await edit(items.find((v) => v.bot_id === config.dataset.config) || { bot_id: config.dataset.config });
      if (preview) await panel(items.find((v) => v.bot_id === preview.dataset.preview));
    });
  }
  async function host() {
    shutdown();
    const current = generation;
    const state = await call("host-status");
    if (generation !== current) return;
    const p = state.permissions || {};
    openDialog("本机驱动与系统权限", `<p>内置驱动 ${esc(state.version || "")} · ${state.available ? "已安装" : "不可用"} · ${state.running ? "运行中" : "已停止"}</p>${state.error ? `<p class="form-help">${esc(state.error)}</p>` : ""}<p>辅助功能：${p.accessibility ? "已授权" : "未授权"}<br>屏幕录制：${p.screen_recording ? "已授权" : "未授权"}</p><p class="form-help">${p.supported ? "在系统设置中为 bqbot 开启辅助功能和屏幕录制权限；开发运行时请以系统提示中的应用为准。授权后刷新状态并重新启动驱动。若系统要求退出应用，请重新打开 bqbot。" : "内置本机控制目前仅支持 macOS。"}</p><div class="tool-buttons">${p.supported ? '<button type="button" class="secondary" data-host-action="permissions">请求系统权限</button><button type="button" class="secondary" data-permission="accessibility">辅助功能设置</button><button type="button" class="secondary" data-permission="screen_recording">屏幕录制设置</button>' : ""}<button type="button" class="secondary" data-host-action="refresh">刷新状态</button><button type="button" class="secondary" data-host-action="${state.running ? "restart" : "start"}" ${!state.available || !p.accessibility || !p.screen_recording ? "disabled" : ""}>${state.running ? "重新启动驱动" : "启动驱动"}</button><button type="button" class="secondary" data-host-action="stop">停止驱动</button><button type="button" class="secondary" data-host-back>返回电脑列表</button></div>`);
    body().onclick = run(async (e) => {
      const setting = e.target.closest("[data-permission]");
      if (setting) { await call("host-settings", { permission: setting.dataset.permission }); return; }
      if (e.target.closest("[data-host-back]")) { await home(); return; }
      const action = e.target.closest("[data-host-action]");
      if (!action || action.disabled) return;
      action.disabled = true;
      try {
        const kind = action.dataset.hostAction;
        if (kind === "restart") { await call("host-stop"); await call("host-start"); }
        else if (kind !== "refresh") await call(`host-${kind}`);
        if (generation === current) await host();
      } finally { action.disabled = false; }
    });
  }
  async function edit(c) {
    shutdown();
    const legacy = c.provider === "host" && !!c.url;
    openDialog("电脑连接", `<form>${field("类型", "provider", c.provider || "host", { select: [["host", "这台电脑（内置）"], ["boat", "Boat 云桌面"], ["vm", "本地 / 远程 VM（Cua MCP）"]] })}${legacy ? '<label class="check-label" data-legacy><input type="checkbox" name="builtin">将旧本机 HTTP 连接改为内置驱动</label>' : ""}<div data-connection>${field("API / MCP 地址", "url", c.url || "")}${field("凭据环境变量名", "token_env", c.token_env || "")}<div data-boat>${field("Boat Sandbox ID", "sandbox_id", c.sandbox_id || "")}</div></div><label class="check-label"><input type="checkbox" name="enabled" ${c.enabled ? "checked" : ""}>启用此电脑</label><label class="check-label" data-consent><input type="checkbox" name="host_consent" ${c.host_consent ? "checked" : ""}>允许访问这台电脑的屏幕及输入设备</label><p class="form-help" data-host-help>内置本机控制需要系统授权；保存后可在“本机驱动与系统权限”中授权并启动。伙伴操作仍遵循工具审批。</p><p class="form-help" data-remote-help>Boat 使用已有 Sandbox，需安装 scrot 和 xdotool。VM 使用 Cua 的 HTTP MCP 地址；旧本机 HTTP 地址只接受本机回环。</p>${dialogActions("保存电脑")}</form>`, async (data) => {
      const provider = data.get("provider"), builtin = provider === "host" && (!legacy || data.has("builtin"));
      await call("save-computer", { config: { bot_id: c.bot_id, provider, url: builtin ? "" : data.get("url") || "", token_env: builtin ? "" : data.get("token_env") || "", sandbox_id: provider === "boat" ? data.get("sandbox_id") || "" : "", enabled: data.has("enabled"), host_consent: provider === "host" && data.has("host_consent") } });
      notify("电脑配置已保存");
    });
    const root = body(), form = root.querySelector("form");
    const update = () => {
      const provider = form.elements.provider.value;
      const builtin = provider === "host" && (!legacy || form.elements.builtin.checked);
      root.querySelector("[data-connection]").hidden = builtin;
      for (const name of ["url", "token_env", "sandbox_id"]) form.elements[name].disabled = builtin || (name === "sandbox_id" && provider !== "boat");
      form.elements.url.required = !builtin;
      root.querySelector("[data-boat]").hidden = provider !== "boat";
      root.querySelector("[data-consent]").hidden = provider !== "host";
      root.querySelector("[data-host-help]").hidden = !builtin;
      root.querySelector("[data-remote-help]").hidden = builtin;
      if (legacy) root.querySelector("[data-legacy]").hidden = provider !== "host";
      if (provider === "boat" && !form.elements.url.value) form.elements.url.value = "https://boat.dev/api/v1";
    };
    form.onchange = update;
    update();
  }
  async function panel(c) {
    shutdown(); const current = generation;
    const bot = getBots().find((b) => b.id === c.bot_id);
    openDialog(`${bot?.name || "伙伴"}的电脑`, `<p class="form-help" data-computer-status>正在获取画面…</p><img data-computer-frame alt="电脑实时截图" style="width:100%;max-height:55vh;object-fit:contain;border-radius:8px" hidden><label class="check-label"><input type="checkbox" data-control>允许当前面板发送鼠标和键盘输入</label><div class="tool-buttons"><button type="button" class="secondary" data-shot>刷新</button><button type="button" class="secondary" data-capture>发送截图到当前对话</button><button type="button" class="secondary" data-key="Return">Enter</button><button type="button" class="secondary" data-key="Escape">Esc</button><button type="button" class="secondary" data-scroll="up">向上滚动</button><button type="button" class="secondary" data-scroll="down">向下滚动</button></div><form data-type-form>${field("输入文字", "text", "")}${dialogActions("输入到电脑")}</form>`);
    const root = body(), image = root.querySelector("[data-computer-frame]"), status = root.querySelector("[data-computer-status]");
    let pending = false, frame = null;
    const alive = () => current === generation && image.isConnected && root.closest("dialog")?.open;
    const control = async (action) => {
      if (!root.querySelector("[data-control]").checked) throw new Error("先勾选允许当前面板输入");
      await call("action", { bot_id: c.bot_id, action, confirmed: true });
    };
    const refresh = async () => {
      if (!alive() || pending) return;
      pending = true;
      try {
        const next = await call("action", { bot_id: c.bot_id, action: { kind: "screenshot" } });
        if (!alive()) return;
        frame = next;
        if (frame.image && ["image/png", "image/jpeg"].includes(frame.image.mime)) { image.src = `data:${frame.image.mime};base64,${frame.image.data}`; image.hidden = false; }
        status.textContent = "画面每三秒刷新；点击画面可定位鼠标。";
      } catch (e) { if (alive()) status.textContent = e.message; }
      finally { pending = false; }
    };
    const poll = async () => { await refresh(); if (alive()) setTimeout(poll, 3000); };
    root.onclick = run(async (e) => {
      if (e.target.closest("[data-shot]")) await refresh();
      if (e.target.closest("[data-capture]")) {
        const conversation = getConversation?.();
        if (!conversation || conversation.bot_id !== c.bot_id || conversation.room_id || conversation.closed) throw new Error("先选择此伙伴的开放私人对话");
        await call("capture-to-chat", { bot_id: c.bot_id, thread_id: conversation.id });
        notify("截图已发送到对话");
      }
      const key = e.target.closest("[data-key]"), scroll = e.target.closest("[data-scroll]");
      if (key) await control({ kind: "key", key: key.dataset.key });
      if (scroll) await control({ kind: "scroll", direction: scroll.dataset.scroll, x: Math.floor((frame?.width || 0) / 2), y: Math.floor((frame?.height || 0) / 2) });
      if (e.target === image && frame) {
        const rect = image.getBoundingClientRect(), scale = Math.min(rect.width / frame.width, rect.height / frame.height);
        const left = rect.left + (rect.width - frame.width * scale) / 2, top = rect.top + (rect.height - frame.height * scale) / 2;
        const x = Math.floor((e.clientX - left) / scale), y = Math.floor((e.clientY - top) / scale);
        if (x >= 0 && y >= 0 && x < frame.width && y < frame.height) await control({ kind: "click", x, y });
      }
    });
    root.querySelector("form").onsubmit = run(async (e) => { e.preventDefault(); const input = e.target.elements.text; await control({ kind: "type", text: input.value }); input.value = ""; await refresh(); });
    await poll();
  }
  const providers = { host: "这台电脑", boat: "Boat 云桌面", vm: "VM" };
  async function summary(botId) {
    const c = (await call("computers") || []).find((v) => v.bot_id === botId);
    return c ? `${providers[c.provider] || c.provider} · ${c.enabled ? "已启用" : "已关闭"}` : "尚未配置";
  }
  async function configure(botId) {
    const items = await call("computers") || [];
    await edit(items.find((v) => v.bot_id === botId) || { bot_id: botId });
  }
  return { home, shutdown, summary, configure };
}
