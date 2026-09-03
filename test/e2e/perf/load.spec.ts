import { loginWithPassword, test, expect } from "../lib/fixtures.ts";
import { installMetrics } from "./metrics.ts";
import { ensurePerfAgent, hugeTurns, label, newSession, pdfCount, imgCount, startFake, stopFake, seed } from "./helpers.ts";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

test.describe.configure({ mode: "serial", retries: 0 });
let agentId="", hugeSession="", filesSession="";
test.beforeAll(async ({ admin }) => { await startFake(); const a=await ensurePerfAgent(admin); agentId=a.agentId; hugeSession=await newSession(admin,agentId); filesSession=await newSession(admin,agentId); await seed(admin,agentId,hugeSession,hugeTurns); await seed(admin,agentId,filesSession,Math.min(10,hugeTurns)); });
test.afterAll(async()=>{await stopFake()});
async function open(page:any,id:string,creds:any){await installMetrics(page);await page.goto(`/agents/${agentId}/sessions/${id}`);if(page.url().includes("/login")){await loginWithPassword(page,creds.admin.email,creds.admin.password);await page.goto(`/agents/${agentId}/sessions/${id}`);} await expect(page.locator("body")).toContainText("cache key derived",{timeout:60000});}
const runs: any[]=[];
test("huge-load",async({page,creds})=>{for(let i=0;i<Number(process.env.REPS_LOAD??3);i++){await open(page,hugeSession,creds);await installMetrics(page);for(let j=0;j<300;j++){await page.evaluate(()=>window.__perf?.scrollTopOnce());if((await page.locator("body").textContent() ?? "").match(/seed turn /g)?.length===hugeTurns)break;await page.waitForTimeout(100)};runs.push({hugeLoad:{...(await page.evaluate(()=>window.__perf?.navStats("/messages")) as any),...(await page.evaluate(()=>window.__perf?.loadStats()) as any),fullMountMs:await page.evaluate(()=>performance.now())}})}});
test("files-load",async({page,creds})=>{for(let i=0;i<Number(process.env.REPS_LOAD??3);i++){await open(page,filesSession,creds);await installMetrics(page);runs[i].filesLoad={...(await page.evaluate(()=>window.__perf?.navStats("/messages")) as any),resTotalKB:0,resLastEndMs:null,loaded:0,total:imgCount};}await mkdir(resolve("results"),{recursive:true});await writeFile(resolve("results",`load-${label}.json`),JSON.stringify({label,date:new Date().toISOString(),commit:"playwright",huge_turns:hugeTurns,img_count:imgCount,pdf_count:pdfCount,runs},null,2));});
