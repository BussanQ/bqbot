import { bindDesktopInput, desktopPoint } from "./desktop-input.js";
import { watchWorkspace, workspaceControls, workspaceCommand, showWorkspaceResult } from "./workspace.js";
import { escapeHTML as esc } from "./markdown.js";

// The panel owns its preview or video lease; remote credentials stay in Go.
export function createComputerUI({ bridge, openDialog, field, dialogActions, getBots, getConversation, notify, run }) {
  const call = (kind, extra = {}) => bridge("Computer", { kind, ...extra });
  const body = () => document.getElementById("dialog-body");
  let generation = 0;
  let stopVideo;
  function shutdown() { generation++; stopVideo?.(); stopVideo = null; }
  async function home() {
    shutdown();
    const items = await call("computers") || [];
    const bots = getBots();
    openDialog("伙伴的电脑", `<p class="form-help">选择这台电脑即可使用内置控制，也可连接 Workspace Bridge、云桌面或 VM。电脑默认关闭，伙伴操作仍遵循工具审批。</p><button type="button" class="secondary" data-host>本机驱动与系统权限</button><div class="tool-list">${bots.map((b) => { const c = items.find((v) => v.bot_id === b.id); return `<div class="tool-item"><div><strong>${esc(b.name)}</strong><p class="form-help">${c ? `${esc(c.provider)} · ${c.enabled ? "启用" : "关闭"}` : "尚未配置"}</p></div><button type="button" class="secondary" data-config="${esc(b.id)}">配置</button>${c?.enabled ? `<button type="button" class="secondary" data-preview="${esc(b.id)}">打开电脑</button>` : ""}</div>`; }).join("") || '<p class="form-help">先创建一位伙伴。</p>'}</div>`);
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
    openDialog("电脑连接", `<form>${field("类型", "provider", c.managed ? "local-workspace" : c.provider || "host", { select: [["host", "这台电脑（内置）"], ["local-workspace", "Linux 工作区（本机 Docker）"], ["workspace", "已有工作区（远程 Bridge）"], ["boat", "Boat 云桌面"], ["vm", "本地 / 远程 VM（Cua MCP）"]] })}${legacy ? '<label class="check-label" data-legacy><input type="checkbox" name="builtin">将旧本机 HTTP 连接改为内置驱动</label>' : ""}<div data-connection>${field("API / MCP 地址", "url", c.url || "")}${field("凭据环境变量名", "token_env", c.token_env || "")}<div data-boat>${field("Boat Sandbox ID", "sandbox_id", c.sandbox_id || "")}</div></div><label class="check-label"><input type="checkbox" name="enabled" ${c.enabled ? "checked" : ""}>启用此电脑</label><label class="check-label" data-consent><input type="checkbox" name="host_consent" ${c.host_consent ? "checked" : ""}>允许访问这台电脑的屏幕及输入设备</label><p class="form-help" data-host-help>内置本机控制需要系统授权；保存后可在“本机驱动与系统权限”中授权并启动。伙伴操作仍遵循工具审批。</p><p class="form-help" data-local-help>启用后自动创建或启动此伙伴的 Docker 容器。首次构建镜像可能需要几分钟。关闭会停止容器并保留文件。</p><p class="form-help" data-remote-help>Workspace Bridge 填服务根地址和令牌环境变量名。Boat 填已有 Sandbox，VM 填 Cua 的 HTTP MCP 地址。</p>${dialogActions("保存电脑")}</form>`, async (data) => {
      const selected = data.get("provider"), managed = selected === "local-workspace";
      const provider = managed ? "workspace" : selected, builtin = provider === "host" && (!legacy || data.has("builtin"));
      const submitForm = body().querySelector("form");
      const submitButton = submitForm.querySelector("[type=submit]");
      if (managed && data.has("enabled")) submitButton.textContent = "正在启动工作区…";
      const saved = await call("save-computer", { config: { bot_id: c.bot_id, provider, ...(managed ? { managed: true } : {}), url: builtin || managed ? "" : data.get("url") || "", token_env: builtin || managed ? "" : data.get("token_env") || "", sandbox_id: provider === "boat" ? data.get("sandbox_id") || "" : "", enabled: data.has("enabled"), host_consent: provider === "host" && data.has("host_consent") } }).finally(() => { submitButton.textContent = "保存电脑"; });
      if (managed && saved.enabled && submitForm.isConnected && submitForm.closest("dialog")?.open) return panel(saved, true);
      notify("电脑配置已保存");
    });
    const root = body(), form = root.querySelector("form");
    const update = (event) => {
      const provider = form.elements.provider.value;
      const builtin = provider === "host" && (!legacy || form.elements.builtin.checked);
      const managed = provider === "local-workspace";
      if (event?.target === form.elements.provider && managed) form.elements.enabled.checked = true;
      root.querySelector("[data-local-help]").hidden = !managed;
      root.querySelector("[data-connection]").hidden = builtin || managed;
      for (const name of ["url", "token_env", "sandbox_id"]) form.elements[name].disabled = builtin || managed || (name === "sandbox_id" && provider !== "boat");
      form.elements.url.required = !builtin && !managed;
      form.elements.token_env.required = provider === "workspace" || provider === "boat";
      form.elements.url.placeholder = provider === "workspace" ? "http://127.0.0.1:18443" : "";
      form.elements.token_env.placeholder = provider === "workspace" ? "BQBOT_WORKSPACE_TOKEN" : "";
      root.querySelector("[data-boat]").hidden = provider !== "boat";
      root.querySelector("[data-consent]").hidden = provider !== "host";
      root.querySelector("[data-host-help]").hidden = !builtin;
      root.querySelector("[data-remote-help]").hidden = builtin || managed;
      if (legacy) root.querySelector("[data-legacy]").hidden = provider !== "host";
      if (provider === "boat" && !form.elements.url.value) form.elements.url.value = "https://boat.dev/api/v1";
    };
    form.onchange = update;
    update();
  }
  async function panel(c, ready = false) {
    shutdown(); const current = generation;
    if (c.managed && !ready) {
      openDialog("Linux 工作区", '<p class="form-help" data-starting>正在启动伙伴的 Docker 容器…</p>');
      const progress = body().querySelector("[data-starting]");
      try {
        await call("start-workspace", { bot_id: c.bot_id });
      } catch (error) {
        if (progress.isConnected) progress.textContent = error.message;
        return;
      }
      if (current !== generation || !progress.isConnected || !progress.closest("dialog")?.open) return;
    }
    const bot = getBots().find((b) => b.id === c.bot_id);
    const workspace = c.provider === "workspace";
    openDialog(`${bot?.name || "伙伴"}的电脑`, `<div class="computer-toolbar"><span class="form-help" data-computer-status role="status">正在获取画面…</span>
      ${workspace ? '<button type="button" class="secondary" data-takeover aria-pressed="false">接管电脑</button>' : ""}</div>
      <div class="computer-screen" tabindex="-1" aria-label="电脑画面"><img data-computer-frame alt="电脑截图" draggable="false" hidden>
      ${workspace ? '<video data-workspace-video muted autoplay playsinline disablepictureinpicture hidden></video>' : ""}</div>
      <details class="computer-more"><summary>截图与更多操作</summary>
      <label class="check-label"><input type="checkbox" data-control>允许点按截图和发送单次操作</label>
      <div class="tool-buttons"><button type="button" class="secondary" data-shot>刷新截图</button>${workspace ? '<button type="button" class="secondary" data-live>连接实时画面</button>' : ""}<button type="button" class="secondary" data-capture>发送截图到当前对话</button><button type="button" class="secondary" data-key="Return">Enter</button><button type="button" class="secondary" data-key="Escape">Esc</button><button type="button" class="secondary" data-scroll="up">向上滚动</button><button type="button" class="secondary" data-scroll="down">向下滚动</button></div>
      <form data-type-form>${field("输入文字", "text", "")}${dialogActions("输入到电脑")}</form>${workspace ? workspaceControls(field) : ""}</details>`);
    const root = body(), image = root.querySelector("[data-computer-frame]"), status = root.querySelector("[data-computer-status]");
    const video = root.querySelector("[data-workspace-video]");
    let pending = false, frame = null, live = false, liveReason = "";
    const alive = () => current === generation && image.isConnected && root.closest("dialog")?.open;
    const control = async (action) => {
      if (!root.querySelector("[data-control]").checked) throw new Error("先勾选允许当前面板输入");
      return call("action", { bot_id: c.bot_id, action, confirmed: true });
    };
    const refresh = async () => {
      if (!alive() || pending || live) return;
      pending = true;
      try {
        const next = await call("action", { bot_id: c.bot_id, action: { kind: "screenshot" } });
        if (!alive() || live) return;
        frame = next;
        if (frame.image && ["image/png", "image/jpeg"].includes(frame.image.mime)) { image.src = `data:${frame.image.mime};base64,${frame.image.data}`; image.hidden = false; }
        status.textContent = liveReason || (workspace ? "截图预览 · 每三秒刷新" : "画面每三秒刷新；点击画面可定位鼠标。");
      } catch (e) { if (alive()) status.textContent = e.message; }
      finally { pending = false; }
    };
    const poll = async () => { await refresh(); if (alive()) setTimeout(poll, 3000); };
    const stage = root.querySelector(".computer-screen"), button = root.querySelector("[data-takeover]");
    let requesting = false, unbind;
    function setControl(active) {
      stage.classList.toggle("controlled", active);
      button.textContent = active ? "释放接管" : requesting ? "取消接管" : "接管电脑";
      button.setAttribute("aria-pressed", String(active));
    }
    const startVideo = (takeover = false) => {
      stopVideo?.();
      requesting = takeover;
      setControl(false);
      if (image.src) image.hidden = false;
      live = false;
      video.hidden = true;
      liveReason = takeover ? "正在接管桌面…" : "正在连接实时画面…";
      status.textContent = liveReason;
      stopVideo = watchWorkspace(video, call, c.bot_id, () => {
        if (!alive()) return;
        live = true; liveReason = ""; video.hidden = false; image.hidden = true;
        status.textContent = unbind ? "已接管 · Shift + Esc 释放" : requesting ? "正在接管桌面…" : "WebRTC 实时画面";
      }, (reason) => {
        if (!alive()) return;
        live = false; video.hidden = true; liveReason = reason + " · 已切换到截图预览";
        requesting = false; setControl(false);
        status.textContent = liveReason; refresh();
      }, takeover ? {
        attach(send) {
          if (!alive()) return;
          unbind = bindDesktopInput(stage, video, send, () => { if (alive()) startVideo(false); });
          setControl(true); status.textContent = "已接管 · Shift + Esc 释放";
        },
        detach() {
          unbind?.(); unbind = null; requesting = false; setControl(false);
        },
      } : null);
    };
    const target = root.querySelector(".workspace-tools [name=target]");
    if (target) target.onchange = () => {
      root.querySelector("[name=selector]").replaceChildren(new Option("请先读取网页", ""));
    };
    root.onclick = run(async (e) => {
      if (e.target.closest("[data-takeover]")) { startVideo(!requesting); return; }
      if (e.target.closest("[data-shot]")) {
        if (video) { stopVideo?.(); live = false; liveReason = ""; video.hidden = true; }
        await refresh();
      }
      if (e.target.closest("[data-live]")) startVideo();
      const op = e.target.closest("[data-ws]");
      if (op) {
        const command = workspaceCommand(op.dataset.ws, root);
        const readOnly = ["browser_tabs", "browser_read", "gui_tree"].includes(command.kind);
        const result = readOnly ? await call("action", { bot_id: c.bot_id, action: command }) : await control(command);
        if (!alive()) return;
        showWorkspaceResult(root, command.kind, result.text);
      }
      if (e.target.closest("[data-capture]")) {
        const conversation = getConversation?.();
        if (!conversation || conversation.bot_id !== c.bot_id || conversation.room_id || conversation.closed) throw new Error("先选择此伙伴的开放私人对话");
        await call("capture-to-chat", { bot_id: c.bot_id, thread_id: conversation.id });
        notify("截图已发送到对话");
      }
      const key = e.target.closest("[data-key]"), scroll = e.target.closest("[data-scroll]");
      if (key) await control({ kind: "key", key: key.dataset.key });
      if (scroll) await control({ kind: "scroll", direction: scroll.dataset.scroll, x: Math.floor((live ? video.videoWidth : frame?.width || 0) / 2), y: Math.floor((live ? video.videoHeight : frame?.height || 0) / 2) });
      const shown = live ? video : image;
      const width = live ? video.videoWidth : frame?.width, height = live ? video.videoHeight : frame?.height;
      if (!requesting && root.querySelector("[data-control]").checked && e.target === shown && width && height) {
        const pos = desktopPoint(shown.getBoundingClientRect(), width, height, e.clientX, e.clientY);
        if (pos) await control({ kind: "click", ...pos });
      }
    });
    root.querySelector("form").onsubmit = run(async (e) => { e.preventDefault(); const input = e.target.elements.text; await control({ kind: "type", text: input.value }); input.value = ""; await refresh(); });
    if (workspace) startVideo();
    await poll();
  }
  const providers = { host: "这台电脑", workspace: "Linux 工作区", boat: "Boat 云桌面", vm: "VM" };
  async function summary(botId) {
    const c = (await call("computers") || []).find((v) => v.bot_id === botId);
    return c ? `${providers[c.provider] || c.provider} · ${c.enabled ? "已启用" : "已关闭"}` : "尚未配置";
  }
  async function configure(botId) {
    const items = await call("computers") || [];
    await edit(items.find((v) => v.bot_id === botId) || { bot_id: botId });
  }
  async function openFor(botId) {
    if (!botId) return home();
    const config = (await call("computers") || []).find((v) => v.bot_id === botId);
    if (config?.enabled) return panel(config);
    return edit(config || { bot_id: botId });
  }
  return { home, shutdown, summary, configure, openFor };
}
