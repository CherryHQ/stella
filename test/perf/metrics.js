// Injected into the page by test/perf/run.sh (via `tap browser evaluate`).
// Installs window.__perf: frame/long-task collectors plus scenario helpers.
// Everything returns plain JSON-serializable objects.
(() => {
  if (window.__perf) return "already-installed";

  const perf = {
    frames: [],
    longTasks: [],
    t0: 0,
    rafId: 0,
    obs: null,

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
      this.obs = new PerformanceObserver((list) => {
        for (const e of list.getEntries()) this.longTasks.push(e.duration);
      });
      this.obs.observe({ type: "longtask" });
      return "started";
    },

    stop() {
      cancelAnimationFrame(this.rafId);
      if (this.obs) this.obs.disconnect();
      const dur = performance.now() - this.t0;
      const fr = this.frames.slice(1); // drop the first interval (raf warmup)
      const sorted = [...fr].sort((a, b) => a - b);
      const p = (q) => (sorted.length ? sorted[Math.min(sorted.length - 1, Math.floor(q * sorted.length))] : 0);
      const lt = this.longTasks;
      return {
        durationMs: Math.round(dur),
        frames: fr.length,
        avgFrameMs: fr.length ? +(fr.reduce((a, b) => a + b, 0) / fr.length).toFixed(2) : 0,
        p95FrameMs: +p(0.95).toFixed(2),
        maxFrameMs: fr.length ? +Math.max(...fr).toFixed(2) : 0,
        jankFramesPct: fr.length ? +((fr.filter((f) => f > 33.4).length / fr.length) * 100).toFixed(1) : 0,
        longTasks: lt.length,
        longTaskTotalMs: Math.round(lt.reduce((a, b) => a + b, 0)),
        longTaskMaxMs: Math.round(lt.length ? Math.max(...lt) : 0),
      };
    },

    // Buffered long tasks since navigation — used by the load scenario, where
    // injection necessarily happens after the work being measured.
    loadStats() {
      const nav = performance.getEntriesByType("navigation")[0];
      const o = new PerformanceObserver(() => {});
      o.observe({ type: "longtask", buffered: true });
      const lt = o.takeRecords();
      o.disconnect();
      return {
        domNodes: document.querySelectorAll("*").length,
        domContentLoadedMs: nav ? Math.round(nav.domContentLoadedEventEnd - nav.startTime) : -1,
        sinceNavMs: Math.round(performance.now()),
        bufferedLongTasks: lt.length,
        bufferedLongTaskTotalMs: Math.round(lt.reduce((a, e) => a + e.duration, 0)),
        jsHeapMB: performance.memory ? +(performance.memory.usedJSHeapSize / 1048576).toFixed(1) : -1,
      };
    },

    // Synthetic typing: per-keystroke synchronous cost. React flushes discrete
    // input events synchronously, so the dispatch duration approximates the
    // keystroke-to-commit handler cost.
    typeInto(selector, text) {
      const el = document.querySelector(selector);
      if (!el) return { error: "no element for " + selector };
      const setter = Object.getOwnPropertyDescriptor(
        window.HTMLTextAreaElement.prototype, "value").set;
      el.focus();
      const times = [];
      let val = el.value;
      for (const ch of text) {
        val += ch;
        const t = performance.now();
        setter.call(el, val);
        el.dispatchEvent(new InputEvent("input", { bubbles: true, data: ch, inputType: "insertText" }));
        times.push(performance.now() - t);
      }
      const sorted = [...times].sort((a, b) => a - b);
      const p = (q) => sorted[Math.min(sorted.length - 1, Math.floor(q * sorted.length))];
      return {
        keys: times.length,
        avgKeyMs: +(times.reduce((a, b) => a + b, 0) / times.length).toFixed(2),
        p95KeyMs: +p(0.95).toFixed(2),
        maxKeyMs: +Math.max(...times).toFixed(2),
      };
    },

    clearComposer(selector) {
      const el = document.querySelector(selector);
      if (!el) return { error: "no element for " + selector };
      const setter = Object.getOwnPropertyDescriptor(
        window.HTMLTextAreaElement.prototype, "value").set;
      setter.call(el, "");
      el.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "deleteContentBackward" }));
      return "cleared";
    },

    // Sets the composer text and presses Enter — the UI-path send that drives
    // useChat streaming.
    send(selector, text) {
      const el = document.querySelector(selector);
      if (!el) return { error: "no element for " + selector };
      const setter = Object.getOwnPropertyDescriptor(
        window.HTMLTextAreaElement.prototype, "value").set;
      el.focus();
      setter.call(el, text);
      el.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertText" }));
      el.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter", code: "Enter" }));
      return "sent";
    },

    streamDone(sentinel) {
      return document.body.innerText.includes(sentinel);
    },

    // Finds the transcript's scroll container (largest scrollable div that
    // contains the seeded reply text) and scrolls it to top, triggering
    // load-older pagination. Returns scrollHeight so the caller can loop
    // until it stops growing.
    scrollTopOnce() {
      if (!this._scrollEl || !this._scrollEl.isConnected) {
        const els = [...document.querySelectorAll("div")].filter(
          (d) => d.scrollHeight > d.clientHeight + 200 && d.innerText.includes("cache key derived"),
        );
        els.sort((a, b) => b.scrollHeight - a.scrollHeight);
        this._scrollEl = els[0] || null;
      }
      if (!this._scrollEl) return { error: "no scroll container" };
      this._scrollEl.scrollTop = 0;
      return { scrollHeight: this._scrollEl.scrollHeight };
    },

    scrollBottom() {
      if (!this._scrollEl) return "no-el";
      this._scrollEl.scrollTop = this._scrollEl.scrollHeight;
      return "bottom";
    },
  };

  window.__perf = perf;
  return "installed";
})();
