import assert from "node:assert/strict";
import {spawnSync} from "node:child_process";
import {mkdtempSync, writeFileSync} from "node:fs";
import {tmpdir} from "node:os";
import {join} from "node:path";
import {test} from "node:test";
import {childArgs, childTools} from "./fullsend-agent.js";

test("real pi: nested permissions do not activate inactive tools", {skip: !process.env.FULLSEND_TEST_PI_BIN, timeout: 120000}, () => {
  const home = mkdtempSync(join(tmpdir(), "child-loadout-"));
  const ext = join(home, "fixture.js");
  writeFileSync(ext, `
import {fauxProvider, fauxAssistantMessage, fauxToolCall} from "@earendil-works/pi-ai";
export default function(pi) {
  const p = fauxProvider({provider:"faux",models:[{id:"one"}]});
  p.setResponses([
    fauxAssistantMessage([fauxToolCall("mcp__docs__nested", {})], {stopReason:"toolUse"}),
    fauxAssistantMessage([fauxToolCall("executor", {})], {stopReason:"toolUse"}),
    fauxAssistantMessage("done")
  ]);
  pi.registerProvider(p.provider);
  pi.registerTool({name:"mcp__docs__nested",label:"nested",description:"nested",exposure:process.env.EXPOSURE,defaultActive:false,
    parameters:{type:"object",properties:{}},
    async execute(){ console.error("EXECUTED:nested"); return {content:[{type:"text",text:"ok"}]}; }});
  pi.registerTool({name:"executor",label:"executor",description:"executor",parameters:{type:"object",properties:{}},
    async execute(id,p,s,u,ctx){
      console.error("ACTIVE:"+JSON.stringify(pi.getActiveTools()));
      await ctx.executeTool("mcp__docs__nested", {});
      return {content:[{type:"text",text:"ok"}]};
    }});
}`);
  for (const exposure of ["codemode", "deferred"]) {
    for (const toolMode of ["default", "extensions-only", "explicit"]) {
      const agent = {toolMode, tools: toolMode === "explicit" ? ["executor", "mcp__docs__*"] : [], extensions:[ext], sessionsDir:home};
      const active = ["executor"];
      const tools = childTools(agent, undefined, active, [{name:"mcp__docs__nested", exposure}]);
      for (const guarded of [false, true]) {
        const args = childArgs(agent, {seq:1,modelSpec:"faux/one",tools,activeTools:guarded ? active : tools});
        const result = spawnSync(process.env.FULLSEND_TEST_PI_BIN,args,{
          cwd:home,input:"probe",encoding:"utf8",timeout:45000,
          env:{PATH:process.env.PATH,HOME:home,PI_CODING_AGENT_DIR:home,EXPOSURE:exposure}
        });
        assert.equal(result.status,0,result.stderr);
        if (guarded) assert.match(result.stderr,/ACTIVE:\["executor"\]/);
        assert.equal(result.stderr.split("EXECUTED:nested").length-1,guarded ? 1 : 2,result.stderr);
      }
    }
  }
});
