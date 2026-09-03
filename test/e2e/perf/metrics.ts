// The collector is serialized into the browser and intentionally uses browser globals.
// @ts-nocheck
import type { Page } from "@playwright/test";

declare global {
  interface Window {
    __perf?: PerfCollector;
  }
}

export interface PerfCollector {
  start(): void;
  stop(): Record<string, number>;
  loadStats(): Record<string, number | null>;
  typeInto(selector: string, text: string): Record<string, number>;
  clearComposer(selector: string): void;
  scrollTopOnce(): void;
  scrollBottom(): void;
  navStats(pattern: string): Record<string, number | null>;
  imgProgress(): { total: number; loaded: number; };
}

// Install before navigation so the collector observes the page-load path.
export async function installMetrics(page: Page): Promise<void> {
  await page.addInitScript({ content: `(${installMetricsInPage.toString()})()` });
}

function installMetricsInPage(): void {
  const collector = {
    frames: [],
    longTasks: [],
    t0: 0,
    rafId: 0,
    observer: null,
    scrollElement: null,

    start() {
      this.frames = [];
      this.longTasks = [];
      this.t0 = performance.now();
      let last = this.t0;
      const tick = (now) => {
        this.frames.push(now - last);
        last = now;
        this.rafId = requestAnimationFrame(tick);
      };
      this.rafId = requestAnimationFrame(tick);
      this.observer = new PerformanceObserver((list) => {
        for (const entry of list.getEntries()) this.longTasks.push(entry.duration);
      });
      this.observer.observe({ type: "longtask" });
    },

    stop() {
      cancelAnimationFrame(this.rafId);
      this.observer?.disconnect();
      const frames = this.frames.slice(1);
      const sorted = [...frames].sort((a, b) => a - b);
      const percentile = (quantile) => sorted.length ? sorted[Math.min(sorted.length - 1, Math.floor(quantile * sorted.length))] : 0;
      return {
        durationMs: Math.round(performance.now() - this.t0),
        frames: frames.length,
        avgFrameMs: frames.length ? +(frames.reduce((a, b) => a + b, 0) / frames.length).toFixed(2) : 0,
        p95FrameMs: +percentile(0.95).toFixed(2),
        maxFrameMs: frames.length ? +Math.max(...frames).toFixed(2) : 0,
        jankFramesPct: frames.length ? +((frames.filter((frame) => frame > 33.4).length / frames.length) * 100).toFixed(1) : 0,
        longTasks: this.longTasks.length,
        longTaskTotalMs: Math.round(this.longTasks.reduce((a, b) => a + b, 0)),
        longTaskMaxMs: Math.round(this.longTasks.length ? Math.max(...this.longTasks) : 0),
      };
    },

    loadStats() {
      const navigation = performance.getEntriesByType("navigation")[0];
      const paint = performance.getEntriesByType("paint").find((entry) => entry.name === "first-contentful-paint");
      return {
        domNodes: document.querySelectorAll("*").length,
        domContentLoadedMs: navigation ? Math.round(navigation.domContentLoadedEventEnd - navigation.startTime) : -1,
        sinceNavMs: Math.round(performance.now()),
        bufferedLongTasks: 0,
        bufferedLongTaskTotalMs: 0,
        jsHeapMB: performance.memory ? +(performance.memory.usedJSHeapSize / 1048576).toFixed(1) : -1,
        fcpMs: paint ? Math.round(paint.startTime) : null,
      };
    },

    typeInto(selector, text) {
      const element = document.querySelector(selector);
      if (!element) return { error: 1 };
      const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value").set;
      element.focus();
      let value = element.value;
      const durations = [];
      for (const character of text) {
        value += character;
        const started = performance.now();
        setter.call(element, value);
        element.dispatchEvent(new InputEvent("input", { bubbles: true, data: character, inputType: "insertText" }));
        durations.push(performance.now() - started);
      }
      const sorted = [...durations].sort((a, b) => a - b);
      const percentile = (quantile) => sorted[Math.min(sorted.length - 1, Math.floor(quantile * sorted.length))];
      return {
        keys: durations.length,
        avgKeyMs: +(durations.reduce((a, b) => a + b, 0) / durations.length).toFixed(2),
        p95KeyMs: +percentile(0.95).toFixed(2),
        maxKeyMs: +Math.max(...durations).toFixed(2),
      };
    },

    clearComposer(selector) {
      const element = document.querySelector(selector);
      if (!element) return;
      const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value").set;
      setter.call(element, "");
      element.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "deleteContentBackward" }));
    },

    scrollTopOnce() {
      if (!this.scrollElement || !this.scrollElement.isConnected) {
        const elements = [...document.querySelectorAll(".stella-transcript-scroll")].filter((element) =>
          element.scrollHeight > element.clientHeight
        );
        elements.sort((a, b) => b.scrollHeight - a.scrollHeight);
        this.scrollElement = elements[0] ?? null;
      }
      if (this.scrollElement) this.scrollElement.scrollTop = 0;
    },

    scrollBottom() {
      if (this.scrollElement) this.scrollElement.scrollTop = this.scrollElement.scrollHeight;
    },

    navStats(pattern) {
      const resources = performance.getEntriesByType("resource").filter((entry) => entry.name.includes(pattern));
      const ends = resources.map((entry) => entry.responseEnd);
      return {
        resCount: resources.length,
        resTotalKB: +(resources.reduce((sum, entry) => sum + entry.transferSize, 0) / 1024).toFixed(0),
        resLastEndMs: ends.length ? Math.round(Math.max(...ends)) : null,
      };
    },

    imgProgress() {
      const images = [...document.images].filter((image) => image.src.includes("file-content"));
      return { total: images.length, loaded: images.filter((image) => image.complete && image.naturalWidth > 0).length };
    },
  };
  window.__perf = collector;
}
