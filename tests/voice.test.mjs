import test from "node:test";
import assert from "node:assert/strict";
import { createVoiceUI } from "../web/voice.js";

test("voice reads only newly completed replies and stops on conversation changes", async () => {
  const calls = [];
  let conversation = { id: "one", busy: false, messages: [{ id: "old", role: "assistant", text: "old reply" }] };
  const ui = createVoiceUI({
    bridge: async (method, input) => {
      calls.push(input);
      if (input.kind === "settings") return { settings: { enabled: true, auto_read: true } };
      return { state: "idle" };
    },
    getConversation: () => conversation,
    notify: () => {},
  });
  await ui.onConversation(conversation);
  assert.equal(calls.some((x) => x.kind === "speak"), false);
  conversation = { ...conversation, busy: true, messages: [...conversation.messages, { id: "new", role: "assistant", text: "partial" }] };
  await ui.onConversation(conversation);
  assert.equal(calls.some((x) => x.kind === "speak"), false);
  conversation = { ...conversation, busy: false, messages: [conversation.messages[0], { id: "new", role: "assistant", text: "complete" }] };
  await ui.onConversation(conversation);
  assert.deepEqual(calls.filter((x) => x.kind === "speak").map((x) => x.text), ["complete"]);
  await ui.onConversation(conversation);
  assert.equal(calls.filter((x) => x.kind === "speak").length, 1);
  conversation = { id: "two", busy: false, messages: [{ id: "other", role: "assistant", text: "another old reply" }] };
  await ui.onConversation(conversation);
  assert.equal(calls.at(-1).kind, "stop");
  assert.equal(calls.filter((x) => x.kind === "speak").length, 1);
  ui.shutdown();
});

test("unsupported speech recognition fails explicitly", () => {
  const ui = createVoiceUI({ bridge: async () => ({}), getConversation: () => ({ id: "one" }), notify: () => {} });
  assert.throws(() => ui.dictate(), /不支持听写/);
  ui.shutdown();
});
