import { spawn, type ChildProcess } from "node:child_process";
import { existsSync, mkdirSync } from "node:fs";
import { resolve } from "node:path";
import { expectStatus, type ApiClient } from "../lib/api.ts";
import { repoRoot } from "../lib/env.ts";

export const fakePort = 25901;
export const fakeURL = `http://127.0.0.1:${fakePort}`;
export const label = process.env.PERF_LABEL ?? "local";
export const reps = Number(process.env.REPS ?? 5);
export const seedTurns = Number(process.env.SEED_TURNS ?? 100);
export const hugeTurns = Number(process.env.HUGE_TURNS ?? 500);
export const imgCount = Number(process.env.IMG_COUNT ?? 10);
export const pdfCount = Number(process.env.PDF_COUNT ?? 3);

let fake: ChildProcess | undefined;
export async function startFake(): Promise<void> {
  const binary = resolve(repoRoot, "test/e2e/test-results/fakeanthropic");
  if (!existsSync(binary)) { mkdirSync(resolve(repoRoot, "test/e2e/test-results"), { recursive: true }); const { spawnSync } = await import("node:child_process"); const built=spawnSync("go",["build","-o",binary,"./test/fakeanthropic/cmd"],{cwd:repoRoot,stdio:"inherit"}); if(built.status!==0)throw new Error("fakeanthropic build failed"); }
  fake=spawn(binary,["-port",String(fakePort),"-chunks",process.env.PERF_STREAM_CHUNKS??"1500","-interval-ms",process.env.PERF_STREAM_INTERVAL_MS??"10"],{cwd:repoRoot,stdio:"ignore"});
  await new Promise(r=>setTimeout(r,300));
}
export async function stopFake(): Promise<void> { fake?.kill(); fake=undefined; }

export async function ensurePerfAgent(admin: ApiClient): Promise<{ agentId: string; model: string }> {
  const provider = await admin.post("/api/providers", { id:"perf-fake", type:"anthropic", name:"perf-fake", enabled:true, api_key:"perf-not-a-secret", base_url:fakeURL });
  if (provider.status !== 201 && provider.status !== 409) throw new Error(`provider: ${provider.status}`);
  const agents=expectStatus(await admin.get<{agents:{id:string;name:string}[] }>("/api/agents"),200,"agents");
  const found=agents.agents.find(a=>a.name==="perf-agent");
  if(found)return {agentId:found.id,model:"perf-fake/claude-sonnet-4-6"};
  const created=expectStatus(await admin.post<{id:string}>("/api/agents",{name:"perf-agent",model:"perf-fake/claude-sonnet-4-6",enabled:true}),201,"agent");
  return {agentId:created.id,model:"perf-fake/claude-sonnet-4-6"};
}
export async function newSession(admin: ApiClient, agentId: string): Promise<string> { return expectStatus(await admin.post<{id:string}>(`/api/agents/${agentId}/sessions`,{kind:"chat"}),201,"session").id; }
export async function seed(admin: ApiClient, agentId:string, sessionId:string, turns:number): Promise<void> { for(let i=1;i<=turns;i++){const r=await admin.stream(`/api/agents/${agentId}/sessions/${sessionId}/messages`,{parts:[{type:"text",text:`seed turn ${i}`}]});if(r.status!==200)throw new Error(`seed ${i}: ${r.status}`)} }
export async function send(admin:ApiClient,agentId:string,sessionId:string,text:string):Promise<void>{const r=await admin.stream(`/api/agents/${agentId}/sessions/${sessionId}/messages`,{parts:[{type:"text",text}]});if(r.status!==200)throw new Error(`send: ${r.status}`)}
