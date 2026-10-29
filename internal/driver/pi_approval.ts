// Loaded explicitly by the RPC driver, even when other extensions are disabled.
export default function (pi: any) {
  pi.on("tool_call", async (event: any, ctx: any) => {
    if (!ctx.hasUI) return { block: true, reason: "Approval bridge unavailable" };
    try {
      const approved = await ctx.ui.confirm("bqbot.tool.v1", JSON.stringify({
        id: event.toolCallId, kind: "tool", tool: event.toolName, input: event.input,
      }));
      if (approved !== true) return { block: true, reason: "Tool call denied" };
    } catch {
      return { block: true, reason: "Approval bridge failed" };
    }
  });
  pi.on("session_start", (_event: any, ctx: any) => {
    ctx.ui.notify("bqbot.approval.ready.v1", "info");
  });
}
