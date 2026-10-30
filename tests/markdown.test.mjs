import test from "node:test";
import assert from "node:assert/strict";
import { markdown } from "../web/markdown.js";

test("agent HTML stays text and dangerous links never become anchors", () => {
  const result = markdown(
    '<img src=x onerror="alert(1)">\n\n[bad](javascript:alert(1))\n\n[safe](https://example.com/?q="onclick="bad)',
  );
  assert.ok(!result.includes("<img"));
  assert.ok(!result.includes('href="javascript:'));
  assert.ok(result.includes("&lt;img"));
  assert.ok(result.includes("q=&quot;onclick=&quot;bad"));
});
test("unfinished streamed code fences remain escaped code", () => {
  const result = markdown("```html\n<script>alert(1)</script>");
  assert.ok(
    result.includes(
      "<pre><code>&lt;script&gt;alert(1)&lt;/script&gt;</code></pre>",
    ),
  );
  assert.ok(!result.includes("<script>"));
});
test("messages render headings, lists, code and tables", () => {
  const result = markdown(
    '## Plan\n\n- **First**\n- `second`\n\n| Item | Status |\n| --- | --- |\n| UI | Done |\n\n```go\nfmt.Println("hi")\n```',
  );
  assert.ok(result.includes("<h2>Plan</h2>"));
  assert.ok(result.includes("<li><strong>First</strong></li>"));
  assert.ok(result.includes("<td>Done</td>"));
  assert.ok(result.includes("fmt.Println(&quot;hi&quot;)"));
});
