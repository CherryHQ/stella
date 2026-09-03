import { loginWithPassword, test, expect } from "../lib/fixtures.ts";
import { installMetrics } from "./metrics.ts";
import { ensurePerfAgent, label, newSession, reps, seed, send, startFake, stopFake } from "./helpers.ts";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

test.describe.configure({ mode: "serial", retries: 0 });
let agentId="", sessionId="";

test.beforeAll(async ({ admin }) => { await startFake(); const a=await ensurePerfAgent(admin); agentId=a.agentId; sessionId=await newSession(admin,agentId); await seed(admin,agentId,sessionId,Number(process.env.SEED_TURNS??100)); });
test.afterAll(async () => { await stopFake(); });
async function open(page:any, creds:any){ await installMetrics(page); await page.goto(`/agents/${agentId}/sessions/${sessionId}`); if(page.url().includes("/login")){await loginWithPassword(page,creds.admin.email,creds.admin.password);await page.goto(`/agents/${agentId}/sessions/${sessionId}`);}  await expect(page.locator("body")).toContainText("cache key derived",{timeout:60000}); }
async function loadHistory(page:any){ await installMetrics(page); for(let i=0;i<80;i++){await page.evaluate(()=>window.__perf?.scrollTopOnce()); if(await page.locator("body").textContent().then((x:string|null)=>(x?.match(/seed turn /g)?.length??0)>=Number(process.env.SEED_TURNS??100)))break; await page.waitForTimeout(100);} await page.evaluate(()=>window.__perf?.scrollBottom()); }
const results:any[]=[];
test("long-history", async ({ page, creds }) => { for(let rep=0;rep<reps;rep++){await open(page,creds);await page.waitForTimeout(500);await loadHistory(page);results.push({longHistory:await page.evaluate(()=>window.__perf?.loadStats())});} });
test("streaming", async ({ page, admin, creds }) => { for(let rep=0;rep<reps;rep++){await open(page,creds);await loadHistory(page);await page.evaluate(()=>window.__perf?.start()); const nonce=`n${Date.now()}`; await page.locator("textarea").fill(`stream ${nonce}`); await page.locator("textarea").press("Enter"); await expect(page.locator("body")).toContainText(`END-OF-STREAM ${nonce}`,{timeout:180000}); results[rep].streaming=await page.evaluate(()=>window.__perf?.stop()); } void admin; });
test("typing", async ({ page, creds }) => { for(let rep=0;rep<reps;rep++){await open(page,creds);await loadHistory(page);const typing=await page.evaluate(()=>window.__perf?.typeInto("textarea","the quick brown fox ".repeat(6)));results[rep].typing=typing;await page.evaluate(()=>window.__perf?.clearComposer("textarea"));} const out={label,date:new Date().toISOString(),commit:"playwright",reps,runs:results};await mkdir(resolve("results"),{recursive:true});await writeFile(resolve("results",`${label}.json`),JSON.stringify(out,null,2)); });
