// Observation only: policy decisions come from the production adapter.
import { appendFileSync, readFileSync } from "node:fs";

export default async () => {
  const record = (phase, input, args, output = "") => {
    appendFileSync(
      process.env.FULLSEND_PROBE_EVENTS,
      JSON.stringify({
        phase,
        tool: input.tool,
        sessionID: input.sessionID,
        callID: input.callID,
        description: args.description,
        sawOriginal: output.includes("FULLSEND_PROBE_ORIGINAL_") || output.includes("ghp_"),
      }) + "\n",
    );
  };

  return {
    tool: {
      probe_error: {
        description: "Test-only tool that throws before producing a result",
        args: {},
        async execute() {
          throw new Error(readFileSync(process.env.FULLSEND_PROBE_ERROR_FILE, "utf8"));
        },
      },
    },
    "tool.execute.before": async (input, output) => {
      record("before", input, output.args);
    },
    "tool.execute.after": async (input, output) => {
      record("after", input, input.args, output.output ?? "");
    },
  };
};
