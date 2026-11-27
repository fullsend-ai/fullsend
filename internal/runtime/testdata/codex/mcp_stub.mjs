// A dependency-free stdio MCP server for regen-mcp.sh: newline-delimited
// JSON-RPC 2.0 answering initialize, tools/list and tools/call (any other
// request gets method-not-found), with three tools that cover the
// mcp_tool_call outcomes parseCodexStream reads:
//
//   lookup  succeeds with one text block
//   fail    returns a result flagged isError
//   broken  answers with a JSON-RPC error instead of a result
//
// It is test tooling only: nothing in fullsend runs it.
import { createInterface } from "node:readline";

const keyInput = {
  type: "object",
  properties: { key: { type: "string" } },
  required: ["key"],
};

const tools = [
  { name: "lookup", description: "Look up the value stored under a key.", inputSchema: keyInput },
  { name: "fail", description: "Look up a key in a backend that is down.", inputSchema: keyInput },
  { name: "broken", description: "Call a handler that throws.", inputSchema: keyInput },
];

function reply(id, payload) {
  process.stdout.write(JSON.stringify({ jsonrpc: "2.0", id, ...payload }) + "\n");
}

function call(params) {
  const key = String(params?.arguments?.key ?? "");
  switch (params?.name) {
    case "lookup":
      return { result: { content: [{ type: "text", text: `value for ${key}: 42` }] } };
    case "fail":
      return { result: { content: [{ type: "text", text: `backend unavailable for ${key}` }], isError: true } };
    case "broken":
      return { error: { code: -32603, message: `handler crashed on ${key}` } };
    default:
      return { error: { code: -32602, message: `unknown tool ${params?.name}` } };
  }
}

createInterface({ input: process.stdin }).on("line", (line) => {
  let msg;
  try {
    msg = JSON.parse(line);
  } catch {
    return;
  }
  if (msg.id === undefined || msg.id === null) return; // a notification
  switch (msg.method) {
    case "initialize":
      reply(msg.id, {
        result: {
          protocolVersion: msg.params?.protocolVersion ?? "2025-06-18",
          capabilities: { tools: {} },
          serverInfo: { name: "stub", version: "1.0.0" },
        },
      });
      break;
    case "tools/list":
      reply(msg.id, { result: { tools } });
      break;
    case "tools/call":
      reply(msg.id, call(msg.params));
      break;
    default:
      reply(msg.id, { error: { code: -32601, message: `method not found: ${msg.method}` } });
  }
});
