import assert from "node:assert/strict";
import {spawnSync} from "node:child_process";
import {existsSync, mkdtempSync, rmSync, writeFileSync} from "node:fs";
import {tmpdir} from "node:os";
import {join} from "node:path";
import {fileURLToPath} from "node:url";
import {test} from "node:test";
import {childArgs, childTools} from "./fullsend-agent.js";

test("real pi: child activation failures exit before any model request", {skip: !process.env.FULLSEND_TEST_PI_BIN, timeout: 120000}, (t) => {
  const home = mkdtempSync(join(tmpdir(), "child-loadout-fatal-"));
  t.after(() => rmSync(home, {recursive:true,force:true}));
  const ext = join(home, "provider.js");
  const marker = join(home, "requests");
  writeFileSync(ext, `
import {appendFileSync} from "node:fs";
import {fauxProvider, fauxAssistantMessage} from "@earendil-works/pi-ai";
export default function(pi) {
  const p = fauxProvider({provider:"faux",models:[{id:"one"}]});
  p.setResponses([fauxAssistantMessage("done")]);
  const stream = p.provider.streamSimple;
  p.provider.streamSimple = (...args) => {
    appendFileSync(process.env.REQUEST_MARKER, "request\\n");
    return stream(...args);
  };
  pi.registerProvider(p.provider);
}
`);
  const wrapper = join(home, "guard.js");
  writeFileSync(wrapper, `
import guard from ${JSON.stringify(new URL("./fullsend-agent.js", import.meta.url).href)};
export default function(pi) {
  guard(new Proxy(pi, {get(target, key) {
    if (key === "setActiveTools" && process.env.GUARD_FAULT === "noop") return () => {};
    if (key === "setActiveTools" && process.env.GUARD_FAULT === "throw") return () => {throw new Error("injected setter failure");};
    return Reflect.get(target, key);
  }}));
}
`);
  const cases = [
    {flag:"[]",ok:true}, {flag:'["read"]',ok:true},
    {flag:"{",ok:false}, {flag:"null",ok:false}, {flag:"[42]",ok:false},
    {flag:'["not_a_registered_tool"]',ok:false},
    {flag:'["read"]',fault:"noop",ok:false},
    {flag:'["read"]',fault:"throw",ok:false},
  ];
  for (const {flag,fault,ok} of cases) {
    rmSync(marker, {force:true});
    const result = spawnSync(process.env.FULLSEND_TEST_PI_BIN, [
      "--print","--mode","json","--no-approve","--no-extensions","--no-prompt-templates","--no-themes",
      "--tools","read,bash","-e",ext,"-e",fault ? wrapper : fileURLToPath(new URL("./fullsend-agent.js",import.meta.url)),
      "--fullsend-child-active-tools",flag,"--model","faux/one",
    ], {
      cwd:home,input:"probe",encoding:"utf8",timeout:15000,
      env:{PATH:process.env.PATH,HOME:home,PI_CODING_AGENT_DIR:home,REQUEST_MARKER:marker,GUARD_FAULT:fault ?? ""},
    });
    assert.ifError(result.error);
    assert.equal(result.signal,null,result.stderr);
    if (ok) {
      assert.equal(result.status,0,result.stderr);
      assert.ok(existsSync(marker),"control must observe an actual model request");
    } else {
      assert.equal(result.status,1,result.stderr);
      assert.match(result.stderr,/cannot restore child active tools/);
      assert.equal(existsSync(marker),false,"failed activation must stop before a model request");
    }
  }
});

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
    for (const toolMode of ["default", "explicit"]) {
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
