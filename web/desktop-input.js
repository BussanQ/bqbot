// Coordinates are relative to the contained framebuffer, excluding its letterbox.
export function desktopPoint(rect, width, height, x, y, clamp = false) {
  if (!width || !height || !rect.width || !rect.height) return null;
  const scale = Math.min(rect.width / width, rect.height / height);
  x = Math.floor((x - rect.left - (rect.width - width * scale) / 2) / scale);
  y = Math.floor((y - rect.top - (rect.height - height * scale) / 2) / scale);
  if (!clamp && (x < 0 || x >= width || y < 0 || y >= height)) return null;
  return { x: Math.max(0, Math.min(width - 1, x)), y: Math.max(0, Math.min(height - 1, y)) };
}

const named = { Backspace: 0xff08, Tab: 0xff09, Enter: 0xff0d, Escape: 0xff1b,
  Home: 0xff50, ArrowLeft: 0xff51, ArrowUp: 0xff52, ArrowRight: 0xff53, ArrowDown: 0xff54,
  PageUp: 0xff55, PageDown: 0xff56, End: 0xff57, Insert: 0xff63, Delete: 0xffff,
  CapsLock: 0xffe5, Shift: 0xffe1, Control: 0xffe3, Meta: 0xffeb, Alt: 0xffe9 };
export function keysym(key, location) {
  if (Object.hasOwn(named, key)) return named[key] + (location === 2 && ["Shift","Control","Alt","Meta"].includes(key) ? 1 : 0);
  if (/^F([1-9]|1[0-9]|2[0-4])$/.test(key)) return 0xffbd + Number(key.slice(1));
  if ([...key].length !== 1) return 0;
  const cp = key.codePointAt(0);
  return cp < 32 ? 0 : cp <= 255 ? cp : 0x1000000 + cp;
}

// Listeners exist only while the authenticated input channel owns the desktop.
export function bindDesktopInput(stage, video, send, release) {
  const ab = new AbortController(), held = new Map();
  let pointer = null, move, raf;
  const on = (el, event, fn, opts = {}) => el.addEventListener(event, fn, { ...opts, signal: ab.signal });
  const point = (e, clamp) => desktopPoint(stage.getBoundingClientRect(), video.videoWidth, video.videoHeight, e.clientX, e.clientY, clamp);
  const mask = (b) => (b & 1) | ((b & 2) << 1) | ((b & 4) >> 1);
  function flush() {
    cancelAnimationFrame(raf); raf = null;
    if (move) { send(move); move = null; }
  }
  function reset() {
    cancelAnimationFrame(raf); raf = null; move = null; held.clear();
    if (pointer !== null && stage.hasPointerCapture(pointer)) stage.releasePointerCapture(pointer);
    pointer = null; send({ kind: "reset" });
  }
  on(stage, "pointerdown", (e) => {
    const pos = point(e);
    if (!pos || !e.isPrimary || e.button > 2) return;
    e.preventDefault(); stage.focus({preventScroll:true}); flush();
    pointer = e.pointerId; stage.setPointerCapture(pointer);
    send({ kind:"pointer", ...pos, mask:mask(e.buttons) });
  });
  on(stage, "pointermove", (e) => {
    const pos = point(e, pointer === e.pointerId);
    if (!pos || !e.isPrimary) return;
    move = { kind:"pointer", ...pos, mask:pointer === e.pointerId ? mask(e.buttons) : 0 };
    if (raf == null) raf = requestAnimationFrame(flush);
  });
  on(stage, "pointerup", (e) => {
    if (pointer !== e.pointerId) return;
    flush(); const pos = point(e,true);
    if (pos) send({ kind:"pointer", ...pos, mask:mask(e.buttons) });
    if (!e.buttons) { pointer = null; if (stage.hasPointerCapture(e.pointerId)) stage.releasePointerCapture(e.pointerId); }
  });
  on(stage, "lostpointercapture", () => { if (pointer !== null) reset(); });
  on(stage, "pointercancel", reset);
  on(stage, "contextmenu", (e) => e.preventDefault());
  on(stage, "wheel", (e) => {
    const pos = point(e);
    if (!pos) return;
    e.preventDefault(); flush();
    const vertical = Math.abs(e.deltaY) >= Math.abs(e.deltaX);
    const bit = vertical ? (e.deltaY < 0 ? 8 : 16) : (e.deltaX < 0 ? 32 : 64);
    const buttons = mask(e.buttons);
    send({kind:"pointer", ...pos, mask:buttons | bit}); send({kind:"pointer", ...pos, mask:buttons});
  }, {passive:false});
  on(stage, "keydown", (e) => {
    if (e.key === "Escape" && e.shiftKey) { e.preventDefault(); e.stopPropagation(); release(); return; }
    const k = held.get(e.code || e.key) || keysym(e.key, e.location);
    if (!k || e.isComposing) return;
    e.preventDefault(); e.stopPropagation();
    held.set(e.code || e.key, k); send({kind:"key",key:k,down:true});
  });
  on(stage, "keyup", (e) => {
    const id = e.code || e.key, k = held.get(id);
    if (!k) return;
    e.preventDefault(); e.stopPropagation(); held.delete(id); send({kind:"key",key:k,down:false});
  });
  on(stage, "blur", reset);
  on(window, "blur", release);
  on(document, "visibilitychange", () => { if (document.hidden) release(); });
  stage.focus({preventScroll:true});
  return () => { ab.abort(); reset(); };
}
