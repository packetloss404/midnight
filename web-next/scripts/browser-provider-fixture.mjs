import { createServer } from "node:http";
import { once } from "node:events";

// This upstream protocol double replaces only the paid model. Browser traffic
// still crosses the real authenticated API, tool registry, stores and SQL.
export async function startBrowserProvider() {
  const server = createServer(async (request, response) => {
    try {
      if (request.method !== "POST" || request.url !== "/v1/messages" || request.headers["x-api-key"] !== "browser-fixture-key") {
        response.writeHead(404).end(); return;
      }
      const chunks = [];
      let size = 0;
      for await (const chunk of request) {
        size += chunk.length;
        if (size > 2_000_000) throw new Error("Request too large");
        chunks.push(chunk);
      }
      const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
      const last = body.messages?.at(-1);
      const blocks = typeof last?.content === "string" ? [{ type: "text", text: last.content }] : (last?.content ?? []);
      const toolResult = blocks.find((block) => block.type === "tool_result");
      const prompt = blocks.filter((block) => block.type === "text").map((block) => block.text).join("\n");
      const marker = "MIDNIGHT_BROWSER_TOOL ";
      let tool;
      if (!toolResult && prompt.includes(marker)) {
        tool = JSON.parse(prompt.slice(prompt.indexOf(marker) + marker.length));
        if (!["get_service", "set_variable"].includes(tool.name) || !tool.input || typeof tool.input !== "object") throw new Error("Unsupported fixture tool");
      }
      const content = tool ? [{ type: "tool_use", id: "tool_browser_fixture", name: tool.name, input: tool.input }] :
        [{ type: "text", text: toolResult ? `Browser fixture tool completed: ${JSON.stringify(toolResult)}` : `Browser fixture reply: ${prompt}` }];
      const message = { id: "msg_browser_fixture", type: "message", role: "assistant", model: body.model, content, stop_reason: tool ? "tool_use" : "end_turn", stop_sequence: null, usage: { input_tokens: 10, output_tokens: 10 } };
      if (!body.stream) {
        response.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify(message)); return;
      }
      response.writeHead(200, { "Content-Type": "text/event-stream" });
      const frame = (type, value) => response.write(`event: ${type}\ndata: ${JSON.stringify({ type, ...value })}\n\n`);
      frame("message_start", { message: { ...message, content: [], stop_reason: null, usage: { input_tokens: 10, output_tokens: 0 } } });
      frame("content_block_start", { index: 0, content_block: tool ? { ...content[0], input: {} } : { type: "text", text: "" } });
      frame("content_block_delta", { index: 0, delta: tool ? { type: "input_json_delta", partial_json: JSON.stringify(tool.input) } : { type: "text_delta", text: content[0].text } });
      frame("content_block_stop", { index: 0 });
      frame("message_delta", { delta: { stop_reason: message.stop_reason, stop_sequence: null }, usage: { output_tokens: 10 } });
      frame("message_stop", {});
      response.end();
    } catch {
      if (!response.headersSent) response.writeHead(400, { "Content-Type": "application/json" });
      response.end(JSON.stringify({ type: "error", error: { type: "invalid_request_error", message: "Browser provider fixture rejected request" } }));
    }
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  return { url: `http://127.0.0.1:${address.port}`, close: () => server.close() };
}
