// The collector is serialized into the browser and intentionally mirrors plain JS.
// @ts-nocheck
import type { Page } from "@playwright/test";

declare global {
  interface Window { __perf?: { start(): void; stop(): unknown; loadStats(): unknown; typeInto(selector: string, text: string): unknown; clearComposer(selector: string): void; scrollTopOnce(): void; scrollBottom(): void; navStats(pattern: string): unknown } }
}

export async function installMetrics(page: Page): Promise<void> {
  await page.addInitScript({ content: `(${installMetricsInPage.toString()})()` });
}

function installMetricsInPage(): void {
  const perf: any = {
    frames: [], longTasks: [], t0: 0, rafId: 0, obs: null, scrollEl: null,
    start() { this.frames=[]; this.longTasks=[]; this.t0=performance.now(); let last=this.t0; const tick=(now: number)=>{this.frames.push(now-last);last=now;this.rafId=requestAnimationFrame(tick)};this.rafId=requestAnimationFrame(tick);this.obs=new PerformanceObserver(list=>{for(const e of list.getEntries())this.longTasks.push(e.duration)});this.obs.observe({type:"longtask"}); },
    stop() { cancelAnimationFrame(this.rafId); if(this.obs)this.obs.disconnect(); const fr=this.frames.slice(1),s=[...fr].sort((a,b)=>a-b),p=(q: number)=>s.length?s[Math.min(s.length-1,Math.floor(q*s.length))]:0; return {durationMs:Math.round(performance.now()-this.t0),frames:fr.length,avgFrameMs:fr.length?+(fr.reduce((a,b)=>a+b,0)/fr.length).toFixed(2):0,p95FrameMs:+p(.95).toFixed(2),maxFrameMs:fr.length?+Math.max(...fr):0,jankFramesPct:fr.length?+((fr.filter(f=>f>33.4).length/fr.length)*100).toFixed(1):0,longTasks:this.longTasks.length,longTaskTotalMs:Math.round(this.longTasks.reduce((a,b)=>a+b,0)),longTaskMaxMs:Math.round(this.longTasks.length?Math.max(...this.longTasks):0)}; },
    loadStats() { const nav=performance.getEntriesByType("navigation")[0] as PerformanceNavigationTiming|undefined, fcp=performance.getEntriesByType("paint").find(p=>p.name==="first-contentful-paint"); return {domNodes:document.querySelectorAll("*").length,domContentLoadedMs:nav?Math.round(nav.domContentLoadedEventEnd-nav.startTime):-1,sinceNavMs:Math.round(performance.now()),bufferedLongTasks:0,bufferedLongTaskTotalMs:0,jsHeapMB:(performance as any).memory? +((performance as any).memory.usedJSHeapSize/1048576).toFixed(1):-1,fcpMs:fcp?Math.round(fcp.startTime):null}; },
    typeInto(selector: string,text: string) { const el=document.querySelector(selector);if(!el)return {error:"no element"};const setter=Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype,"value").set;el.focus();let val=el.value,times=[];for(const ch of text){val+=ch;const t=performance.now();setter.call(el,val);el.dispatchEvent(new InputEvent("input",{bubbles:true,data:ch,inputType:"insertText"}));times.push(performance.now()-t)}const s=[...times].sort((a,b)=>a-b),p=(q: number)=>s[Math.min(s.length-1,Math.floor(q*s.length))];return {keys:times.length,avgKeyMs:+(times.reduce((a,b)=>a+b,0)/times.length).toFixed(2),p95KeyMs:+p(.95).toFixed(2),maxKeyMs:+Math.max(...times).toFixed(2)}; },
    clearComposer(selector: string){const el=document.querySelector(selector);if(!el)return;const setter=Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype,"value").set;setter.call(el,"");el.dispatchEvent(new InputEvent("input",{bubbles:true}))},
    scrollTopOnce(): void {if(!this.scrollEl||!this.scrollEl.isConnected){const els=[...document.querySelectorAll(".stella-transcript-scroll")].filter(d=>d.scrollHeight>d.clientHeight);els.sort((a,b)=>b.scrollHeight-a.scrollHeight);this.scrollEl=els[0]||null}if(this.scrollEl)this.scrollEl.scrollTop=0},
    scrollBottom(): void {if(this.scrollEl)this.scrollEl.scrollTop=this.scrollEl.scrollHeight},
    navStats(pattern: string){const res=performance.getEntriesByType("resource").filter(r=>r.name.includes(pattern)),ends=res.map(r=>r.responseEnd);return {resCount:res.length,resTotalKB:+(res.reduce((s,r)=>s+r.transferSize,0)/1024).toFixed(0),resLastEndMs:ends.length?Math.round(Math.max(...ends)):null}},
  }; window.__perf=perf;
}
