// Dictation is available only when the system WebView exposes SpeechRecognition.
// It starts from an explicit user gesture and stops during synthesis/playback.
export function createVoiceUI({ bridge, openDialog, field, dialogActions, notify, run, getConversation, sendText, setDraft }) {
  const call = (kind, extra = {}) => bridge("Voice", { kind, ...extra });
  const Recognition = globalThis.SpeechRecognition || globalThis.webkitSpeechRecognition;
  let settings = {}, loaded = false, seen = new Set(), conversationID = "";
  let recognition = null, callMode = false, pendingReply = false, speaking = false, timer = null, disposed = false;
  let lastConversation = null, generation = 0;
  const stopRecognition = () => {
    if (recognition) { const current = recognition; recognition = null; current.onend = null; current.abort(); }
  };
  async function stop() {
    generation++;
    callMode = false;
    pendingReply = false;
    speaking = false;
    stopRecognition();
    clearTimeout(timer);
    timer = null;
    await call("stop");
  }
  async function load() {
    const state = await call("settings");
    settings = state.settings || {};
    loaded = true;
    return state;
  }
  async function home() {
    const state = await load();
    openDialog("语音与听写", `<form><label class="check-label"><input type="checkbox" name="enabled" ${settings.enabled ? "checked" : ""}>启用语音合成</label>${field("语音提供商", "provider", settings.provider || "say", { select: [["say", "macOS 本地语音"], ["elevenlabs", "ElevenLabs"], ["fish", "Fish Audio"], ["xai", "xAI"], ["chatterbox", "本地 Chatterbox"]] })}${field("声音名称或 ID", "voice", settings.voice)}${field("模型（可留空）", "model", settings.model)}${field("API 密钥环境变量名", "key_env", settings.key_env)}${field("Chatterbox 本地地址", "url", settings.url, { placeholder: "http://127.0.0.1:8000" })}${field("语言", "language", settings.language || "zh-CN")}<label class="check-label"><input type="checkbox" name="auto_read" ${settings.auto_read ? "checked" : ""}>自动朗读当前对话的新回复</label><p class="form-help">云端合成会发送朗读文本。Chatterbox 需另行运行兼容 /v1/audio/speech 的本地服务。API 密钥${state.configured ? "已设置" : "未设置"}。</p><p class="form-help">${Recognition ? "听写由系统 WebView 的语音识别服务处理，可能联网。通话时识别结果会自动发送到当前对话。" : "此 WebView 不支持语音识别；可使用系统键盘听写输入文字。"}</p><div class="tool-buttons"><button type="button" class="secondary" data-voice="read">朗读最近回复</button><button type="button" class="secondary" data-voice="dictate" ${Recognition ? "" : "disabled"}>听写到输入框</button><button type="button" class="secondary" data-voice="call" ${Recognition ? "" : "disabled"}>${callMode ? "结束通话" : "开始通话"}</button><button type="button" class="secondary" data-voice="stop">停止语音</button></div>${dialogActions("保存语音设置")}</form>`, async (data) => {
      await stop();
      const config = { enabled: data.has("enabled"), provider: data.get("provider"), voice: data.get("voice"), model: data.get("model"), key_env: data.get("key_env"), url: data.get("url"), language: data.get("language"), auto_read: data.has("auto_read") };
      await call("save", { config });
      settings = config;
      notify("语音设置已保存");
    });
    document.getElementById("dialog-body").onclick = run(async (event) => {
      const target = event.target.closest("[data-voice]");
      if (!target || target.disabled) return;
      if (target.dataset.voice === "read") {
        const c = getConversation();
        const message = [...(c?.messages || [])].reverse().find((m) => m.role === "assistant" && m.text);
        if (!message || c.busy) throw new Error("等待完整回复后再朗读");
        await speak(message.text);
      } else if (target.dataset.voice === "dictate") dictate();
      else if (target.dataset.voice === "call") { await toggleCall(); target.textContent = callMode ? "结束通话" : "开始通话"; }
      else await stop();
    });
  }
  function startRecognition(send) {
    if (!Recognition) throw new Error("此 WebView 不支持听写，请使用系统键盘听写");
    stopRecognition();
    const captured = getConversation();
    if (!captured || captured.closed) throw new Error("请先选择可用对话");
    const current = new Recognition();
    recognition = current;
    current.lang = settings.language || "zh-CN";
    current.continuous = false;
    current.interimResults = false;
    current.onresult = async (event) => {
      if (recognition !== current || getConversation()?.id !== captured.id) return;
      const text = Array.from(event.results).filter((r) => r.isFinal).map((r) => r[0].transcript).join(" ").trim();
      if (!text) return;
      stopRecognition();
      try {
        if (send) { pendingReply = true; await sendText(text, captured); }
        else if (setDraft) setDraft(text);
        else throw new Error("听写输入框尚未接入");
      } catch (error) { pendingReply = false; callMode = false; notify(error.message); }
    };
    current.onerror = (event) => {
      if (event.error !== "aborted") { callMode = false; notify(`听写失败：${event.error}`); }
    };
    current.onend = () => {
      if (recognition === current) recognition = null;
      // A fresh click resumes after silence; never leave an invisible open microphone.
      if (!pendingReply && !speaking) callMode = false;
    };
    current.start();
    notify(send ? "通话已开始：说话后会自动发送；可在语音设置中停止" : "开始听写");
  }
  function dictate() { callMode = false; startRecognition(false); }
  async function toggleCall() {
    if (callMode) { await stop(); return; }
    if (!loaded) await load();
    if (!settings.enabled) throw new Error("请先启用并保存语音设置");
    const c = getConversation();
    if (!c || c.busy) throw new Error("请等待当前回复完成");
    callMode = true;
    pendingReply = false;
    try { startRecognition(true); } catch (error) { callMode = false; throw error; }
  }
  async function pollPlayback(active) {
    try {
      const status = await call("status");
      if (disposed || active !== generation) return;
      if (status.state === "playing" || status.state === "synthesizing") {
        timer = setTimeout(() => pollPlayback(active), 250);
        return;
      }
      speaking = false;
      if (status.error) { callMode = false; notify(status.error); }
      if (callMode && !pendingReply && !getConversation()?.busy) startRecognition(true);
    } catch (error) { speaking = false; callMode = false; notify(error.message); }
  }
  async function speak(text) {
    const active = ++generation;
    if (!loaded) await load();
    if (active !== generation || disposed) return;
    if (!settings.enabled) throw new Error("请先启用语音");
    stopRecognition();
    clearTimeout(timer);
    speaking = true;
    try {
      await call("speak", { text });
      if (active !== generation || disposed) return;
      timer = setTimeout(() => pollPlayback(active), 100);
    }
    catch (error) { speaking = false; callMode = false; throw error; }
  }
  async function onConversation(state) {
    const c = state && Object.hasOwn(state, "conversation") ? state.conversation : state;
    if (!c) { if (conversationID) await stop(); conversationID = ""; seen.clear(); return; }
    lastConversation = c;
    if (c.id !== conversationID) {
      const wasActive = conversationID !== "";
      conversationID = c.id;
      seen = new Set((c.messages || []).map((m) => m.id));
      if (wasActive) await stop();
      return;
    }
    if (!loaded) { await load(); if (lastConversation !== c) return; }
    if (c.busy || c.queued > 0) return;
    const fresh = (c.messages || []).filter((m) => !seen.has(m.id));
    for (const m of c.messages || []) seen.add(m.id);
    const reply = [...fresh].reverse().find((m) => m.role === "assistant" && m.text);
    if (pendingReply && fresh.some((m) => m.role === "error")) { await stop(); return; }
    if (reply && settings.enabled && (settings.auto_read || callMode)) {
      pendingReply = false;
      await speak(reply.text);
    }
  }
  function shutdown() { disposed = true; stop().catch(() => {}); }
  return { home, onConversation, shutdown, dictate, toggleCall, speak, stop };
}
