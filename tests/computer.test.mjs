import test from "node:test";
import assert from "node:assert/strict";
import { createComputerUI } from "../web/computer.js";

function setup(items = [], status = {}) {
  const calls = [], fields = new Map(), nodes = new Map();
  const form = { elements: {}, querySelector: () => ({}) };
  const root = { querySelector: (key) => {
    if (key === "form") return form;
    if (!nodes.has(key)) nodes.set(key, {});
    return nodes.get(key);
  } };
  let html = "", submit;
  globalThis.document = { getElementById: () => root };
  const ui = createComputerUI({
    bridge: async (_, input) => {
      calls.push(input);
      if (input.kind === "computers") return items;
      if (input.kind === "host-status") return status;
      return {};
    },
    openDialog: (_, markup, onSubmit) => {
      html = markup;
      submit = onSubmit;
      form.elements.builtin = { checked: false };
    },
    field: (_, name, value) => { fields.set(name, value); form.elements[name] = { value }; return ""; },
    dialogActions: () => "", getBots: () => [{ id: "bot", name: "伙伴" }],
    notify: () => {}, run: (fn) => fn,
  });
  const click = async (selector, dataset = {}) => {
    const target = { dataset, disabled: false };
    await root.onclick({ target: { closest: (s) => s === selector ? target : null } });
  };
  return { ui, calls, fields, form, nodes, click, get html() { return html; },
    save: (values) => submit(new Map(Object.entries(values))) };
}

test("new local computer hides connection credentials and saves explicit consent", async () => {
  const env = setup();
  await env.ui.home();
  await env.click("[data-config]", { config: "bot" });
  assert.equal(env.fields.get("provider"), "host");
  assert.equal(env.nodes.get("[data-connection]").hidden, true);
  assert.equal(env.form.elements.url.required, false);
  assert.equal(env.form.elements.token_env.disabled, true);
  await env.save({ provider: "host", enabled: "on", host_consent: "on", url: "stale", token_env: "STALE", sandbox_id: "stale" });
  assert.deepEqual(env.calls.at(-1).config, { bot_id: "bot", provider: "host", url: "", token_env: "", sandbox_id: "", enabled: true, host_consent: true });
});

test("legacy local HTTP connections migrate only after explicit selection", async () => {
  const env = setup([{ bot_id: "bot", provider: "host", url: "http://127.0.0.1:8123", token_env: "CUA_TOKEN" }]);
  await env.ui.home();
  await env.click("[data-config]", { config: "bot" });
  assert.equal(env.nodes.get("[data-connection]").hidden, false);
  await env.save({ provider: "host", url: "http://127.0.0.1:8123", token_env: "CUA_TOKEN" });
  assert.equal(env.calls.at(-1).config.url, "http://127.0.0.1:8123");
  env.form.elements.builtin.checked = true;
  env.form.onchange();
  assert.equal(env.nodes.get("[data-connection]").hidden, true);
  await env.save({ provider: "host", builtin: "on", host_consent: "on" });
  assert.equal(env.calls.at(-1).config.url, "");
  assert.equal(env.calls.at(-1).config.token_env, "");
});

test("host status never requests permissions and restart stops before starting", async () => {
  const env = setup([], { available: true, running: true, version: "0.28.2", permissions: { supported: true, accessibility: true, screen_recording: true } });
  await env.ui.home();
  await env.click("[data-host]");
  assert.equal(env.calls.some((v) => v.kind === "host-permissions"), false);
  await env.click("[data-host-action]", { hostAction: "restart" });
  assert.deepEqual(env.calls.slice(-3).map((v) => v.kind), ["host-stop", "host-start", "host-status"]);
  await env.click("[data-permission]", { permission: "screen_recording" });
  assert.deepEqual(env.calls.at(-1), { kind: "host-settings", permission: "screen_recording" });
  await env.click("[data-host-action]", { hostAction: "permissions" });
  assert.deepEqual(env.calls.slice(-2).map((v) => v.kind), ["host-permissions", "host-status"]);
});

test("unsupported platforms report unavailable and escape diagnostics", async () => {
  const env = setup([], { available: false, error: "<script>failure</script>", permissions: { supported: false } });
  await env.ui.home();
  await env.click("[data-host]");
  assert.match(env.html, /目前仅支持 macOS/);
  assert.doesNotMatch(env.html, /data-host-action="permissions"/);
  assert.match(env.html, /&lt;script&gt;/);
  assert.match(env.html, /data-host-action="start" disabled/);
});

test("local Linux starts without connection fields, remote Bridge retains them", async () => {
  const env = setup();
  await env.ui.configure("bot");
  env.form.elements.provider.value = "local-workspace";
  env.form.elements.enabled = { checked: false };
  env.form.onchange({ target: env.form.elements.provider });
  assert.equal(env.form.elements.enabled.checked, true);
  assert.equal(env.nodes.get("[data-connection]").hidden, true);
  assert.equal(env.form.elements.url.required, false);
  await env.save({ provider: "local-workspace", enabled: "on", url: "stale", token_env: "OLD" });
  assert.deepEqual(env.calls.at(-1).config, { bot_id: "bot", provider: "workspace", managed: true, enabled: true, url: "", token_env: "", sandbox_id: "", host_consent: false });
  env.form.elements.provider.value = "workspace";
  env.form.onchange();
  assert.equal(env.nodes.get("[data-connection]").hidden, false);
  assert.equal(env.form.elements.token_env.required, true);
});

import { desktopPoint, keysym } from "../web/desktop-input.js";

test("contain mapping keeps both desktop edges reachable and skips letterboxes", () => {
  const rect = {left:10, top:20, width:800, height:600};
  assert.deepEqual(desktopPoint(rect,1280,800,10,70), {x:0,y:0});
  assert.deepEqual(desktopPoint(rect,1280,800,809.9,569.9), {x:1279,y:799});
  assert.equal(desktopPoint(rect,1280,800,80,69), null);
  assert.deepEqual(desktopPoint(rect,1280,800,900,700,true), {x:1279,y:799});
  const wide = {left:0,top:0,width:1000,height:400};
  assert.equal(desktopPoint(wide,1280,800,179,30),null);
  assert.deepEqual(desktopPoint(wide,1280,800,180,0),{x:0,y:0});
});
test("keysyms preserve sided modifiers, navigation and printable Unicode", () => {
  assert.equal(keysym("Control",2),0xffe4);
  assert.equal(keysym("ArrowDown"),0xff54);
  assert.equal(keysym("Z"),90);
  assert.equal(keysym("好"),0x1000000+"好".codePointAt(0));
  assert.equal(keysym("Unidentified"),0);
});
