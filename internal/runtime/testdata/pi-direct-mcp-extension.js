import { fauxAssistantMessage, fauxProvider, fauxToolCall } from "@earendil-works/pi-ai";
import { writeFileSync } from "node:fs";
export default function(pi) {
  const faux = fauxProvider({provider: "faux", models: [{id: "faux-1"}]});
  faux.setResponses([
    fauxAssistantMessage([fauxToolCall("mcp__docs__search", {}), fauxToolCall("mcp__docs__write", {})], {stopReason: "toolUse"}),
    fauxAssistantMessage("done"),
  ]);
  pi.registerProvider(faux.provider);
  pi.on("session_start", () => writeFileSync(process.env.TEST_SNAPSHOT, JSON.stringify({active: pi.getActiveTools(), registered: pi.getAllTools()})));
  for (const name of ["mcp__docs__search", "mcp__docs__write"]) {
    pi.registerTool({name, label: name, description: name, exposure: "direct", parameters: {type: "object", properties: {}},
      async execute() { console.error("EXECUTED:" + name); return {content: [{type: "text", text: "docs"}]}; }
    });
  }
}
