// Only session descriptions cross the Go bridge. Media is receive-only and
// inputs continue through the same confirmed operations as the snapshot panel.
export function watchWorkspace(video, call, botID, ready, fallback) {
  let peer, session, heartbeat, closed = false;
  const closeSession = (id) => call("stream-close", { bot_id: botID, stream_id: id }).catch(() => {});
  const observer = new MutationObserver(() => { if (!video.isConnected) stop(); });
  observer.observe(video.parentElement, { childList: true });
  const timeout = setTimeout(() => fail("实时画面连接超时"), 16000);
  function stop() {
    if (closed) return;
    closed = true;
    clearInterval(heartbeat);
    clearTimeout(timeout);
    observer.disconnect();
    video.onloadeddata = null;
    peer?.close();
    video.srcObject = null;
    if (session) closeSession(session);
  }
  function fail(reason) {
    if (closed) return;
    stop();
    fallback(reason);
  }
  async function start() {
    if (!globalThis.RTCPeerConnection) throw new Error("此 WebView 没有提供 WebRTC");
    peer = new RTCPeerConnection({ iceServers: [] });
    peer.addTransceiver("video", { direction: "recvonly" });
    peer.ontrack = ({ track }) => {
      video.srcObject = new MediaStream([track]);
      video.play().catch((e) => fail(e.message));
    };
    video.onloadeddata = () => { clearTimeout(timeout); if (!closed) ready(); };
    peer.onconnectionstatechange = () => {
      if (peer.connectionState === "failed" || peer.connectionState === "closed") fail("实时画面已断开");
    };
    await peer.setLocalDescription(await peer.createOffer());
    if (peer.iceGatheringState !== "complete") {
      await new Promise((resolve) => {
        const finish = () => {
          clearTimeout(limit);
          peer.removeEventListener("icegatheringstatechange", gathered);
          resolve();
        };
        const gathered = () => { if (peer.iceGatheringState === "complete") finish(); };
        const limit = setTimeout(finish, 1500);
        peer.addEventListener("icegatheringstatechange", gathered);
        gathered();
      });
    }
    if (closed) return;
    const answer = await call("stream-open", { bot_id: botID, sdp: peer.localDescription.sdp });
    if (closed) { closeSession(answer.id); return; }
    session = answer.id;
    await peer.setRemoteDescription({ type: "answer", sdp: answer.sdp });
    if (closed) return;
    heartbeat = setInterval(() => {
      call("stream-renew", { bot_id: botID, stream_id: session }).catch((e) => fail(e.message));
    }, 7000);
  }
  start().catch((e) => fail(e.message));
  return stop;
}

export function workspaceControls(field) {
  const chooser = (label, name, hint) => `<label class="form-field"><span>${label}</span><select name="${name}"><option value="">${hint}</option></select></label>`;
  return `<details class="workspace-tools"><summary>浏览器与辅助功能</summary>
    ${chooser("浏览器标签页","target","当前标签页")}
    <div class="tool-buttons"><button type="button" class="secondary" data-ws="browser_tabs">刷新标签页</button>
    <button type="button" class="secondary" data-ws="browser_read">读取网页</button></div>
    ${field("网页地址", "url", "", { placeholder: "https://…" })}<button type="button" class="secondary" data-ws="browser_open">打开网页</button>
    ${chooser("网页元素","selector","请先读取网页")}
    ${field("填写内容", "fill", "")}
    <div class="tool-buttons"><button type="button" class="secondary" data-ws="browser_click">点击元素</button>
    <button type="button" class="secondary" data-ws="browser_fill">填写元素</button></div>
    ${chooser("桌面控件","ref","请先读取辅助功能树")}
    <div class="tool-buttons"><button type="button" class="secondary" data-ws="gui_tree">读取辅助功能树</button>
    <button type="button" class="secondary" data-ws="gui_invoke">激活控件</button></div>
    <pre class="tool-output" data-ws-result hidden></pre></details>`;
}

export function workspaceCommand(kind, panel) {
  const value = (name) => panel.querySelector(`[name="${name}"]`).value;
  const action = { kind };
  if (kind.startsWith("browser_") && kind !== "browser_tabs") action.target = value("target");
  if (kind === "browser_open") action.url = value("url");
  if (kind === "browser_click" || kind === "browser_fill") action.selector = value("selector");
  if (kind === "browser_fill") action.text = value("fill");
  if (kind === "gui_invoke") action.ref = value("ref");
  if ((kind === "browser_click" || kind === "browser_fill") && !action.selector) throw new Error("请先读取网页并选择元素");
  if (kind === "gui_invoke" && !action.ref) throw new Error("请先读取辅助功能树并选择控件");
  return action;
}

export function showWorkspaceResult(panel, kind, text) {
  const output = panel.querySelector("[data-ws-result]");
  const populate = (name, items, empty) => {
    const select = panel.querySelector(`[name="${name}"]`);
    const old = select.value;
    select.replaceChildren(new Option(empty, ""), ...items.map(([label, value]) => new Option(label, value)));
    if (items.some(([, value]) => value === old)) select.value = old;
  };
  let description = text;
  if (kind === "browser_tabs") {
    const tabs = JSON.parse(text);
    populate("target", tabs.map((t) => [t.title || t.url, t.id]), "当前标签页");
    description = `共 ${tabs.length} 个标签页`;
  } else if (kind === "browser_read") {
    const page = JSON.parse(text);
    populate("selector", page.items.map((el) => [el.label || el.selector, el.selector]), "选择网页元素");
    description = [page.title, page.text].filter(Boolean).join("\n");
  } else if (kind === "gui_tree") {
    const tree = JSON.parse(text);
    populate("ref", tree.filter((n) => n.invokable).map((n) => [[n.name, n.role].filter(Boolean).join(" · "), n.ref]), "选择桌面控件");
    description = tree.filter((n) => n.name).map((n) => `${n.role} · ${n.name}`).join("\n");
  } else if (kind === "browser_open") {
    populate("selector", [], "请先读取网页");
    description = "已发送导航请求";
  }
  output.textContent = description || "操作已完成";
  output.hidden = false;
}
