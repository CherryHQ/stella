import { type ChildProcess, spawn, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { deflateSync } from "node:zlib";
import { type ApiClient, expectStatus } from "../lib/api.ts";
import { repoRoot } from "../lib/env.ts";

export const fakePort = 25901;
export const fakeURL = `http://127.0.0.1:${fakePort}`;
export const label = process.env.PERF_LABEL ?? "local";
export const reps = Number(process.env.REPS ?? 5);
export const seedTurns = Number(process.env.SEED_TURNS ?? 100);
export const hugeTurns = Number(process.env.HUGE_TURNS ?? 500);
export const imgCount = Number(process.env.IMG_COUNT ?? 10);
export const pdfCount = Number(process.env.PDF_COUNT ?? 3);
export const repsLoad = Number(process.env.REPS_LOAD ?? 3);
export const resultsDir = resolve(repoRoot, "test/e2e/perf/results");

let fake: ChildProcess | undefined;

export function startFake(): void {
  const outputDir = resolve(repoRoot, "test/e2e/test-results");
  const binary = resolve(outputDir, "fakeanthropic");
  mkdirSync(outputDir, { recursive: true });
  if (!existsSync(binary)) {
    const built = spawnSync("go", ["build", "-o", binary, "./test/fakeanthropic/cmd"], { cwd: repoRoot, stdio: "inherit" });
    if (built.status !== 0) throw new Error("fakeanthropic build failed");
  }
  fake = spawn(binary, [
    "-port",
    String(fakePort),
    "-chunks",
    process.env.PERF_STREAM_CHUNKS ?? "1500",
    "-interval-ms",
    process.env.PERF_STREAM_INTERVAL_MS ?? "10",
  ], { cwd: repoRoot, stdio: "ignore" });
}

export function stopFake(): void {
  fake?.kill();
  fake = undefined;
}

export async function ensurePerfAgent(admin: ApiClient): Promise<string> {
  const provider = await admin.post("/api/providers", {
    id: "perf-fake",
    type: "anthropic",
    name: "perf-fake",
    enabled: true,
    api_key: "perf-not-a-secret",
    base_url: fakeURL,
  });
  if (provider.status !== 201 && provider.status !== 409) throw new Error(`provider: ${provider.status}`);
  const agents = expectStatus(await admin.get<{ agents: { id: string; name: string; }[]; }>("/api/agents"), 200, "agents");
  const existing = agents.agents.find((agent) => agent.name === "perf-agent");
  if (existing) return existing.id;
  return expectStatus(
    await admin.post<{ id: string; }>("/api/agents", { name: "perf-agent", model: "perf-fake/claude-sonnet-4-6", enabled: true }),
    201,
    "agent",
  ).id;
}

export async function newSession(admin: ApiClient, agentId: string): Promise<string> {
  return expectStatus(await admin.post<{ id: string; }>(`/api/agents/${agentId}/sessions`, { kind: "chat" }), 201, "session").id;
}

export async function send(admin: ApiClient, agentId: string, sessionId: string, text: string): Promise<void> {
  const result = await admin.stream(`/api/agents/${agentId}/sessions/${sessionId}/messages`, { parts: [{ type: "text", text }] });
  if (result.status !== 200) throw new Error(`turn failed: ${result.status}`);
}

export async function seed(admin: ApiClient, agentId: string, sessionId: string, turns: number): Promise<void> {
  for (let index = 1; index <= turns; index += 1) await send(admin, agentId, sessionId, `seed turn ${index}`);
}

function pngChunk(type: string, data: Uint8Array): Uint8Array {
  const bytes = new Uint8Array(12 + data.length);
  const view = new DataView(bytes.buffer);
  view.setUint32(0, data.length);
  bytes.set(new TextEncoder().encode(type), 4);
  bytes.set(data, 8);
  const crc = crc32(bytes.slice(4, 8 + data.length));
  view.setUint32(8 + data.length, crc);
  return bytes;
}

function crc32(data: Uint8Array): number {
  let crc = 0xffffffff;
  for (const value of data) {
    crc ^= value;
    for (let bit = 0; bit < 8; bit += 1) crc = (crc >>> 1) ^ ((crc & 1) ? 0xedb88320 : 0);
  }
  return (crc ^ 0xffffffff) >>> 0;
}

function noisePng(): Uint8Array {
  const width = 800;
  const height = 800;
  const raw = new Uint8Array(height * (width * 3 + 1));
  for (let offset = 0; offset < raw.length; offset += 65_536) {
    crypto.getRandomValues(raw.subarray(offset, Math.min(offset + 65_536, raw.length)));
  }
  for (let row = height - 1; row >= 0; row -= 1) raw[row * (width * 3 + 1)] = 0;
  const header = new Uint8Array(13);
  const view = new DataView(header.buffer);
  view.setUint32(0, width);
  view.setUint32(4, height);
  header.set([8, 2, 0, 0, 0], 8);
  const signature = Uint8Array.from([137, 80, 78, 71, 13, 10, 26, 10]);
  const compressed = new Uint8Array(deflateSync(raw));
  const output = new Uint8Array(signature.length + 25 + compressed.length + 24);
  output.set(signature);
  output.set(pngChunk("IHDR", header), 8);
  output.set(pngChunk("IDAT", compressed), 33);
  output.set(pngChunk("IEND", new Uint8Array()), 33 + compressed.length + 12);
  return output;
}

function minimalPdf(): Uint8Array {
  return new TextEncoder().encode(
    "%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n",
  );
}

interface FixtureState {
  agentId: string;
  sessionId: string;
  imagePaths: string[];
  pdfPaths: string[];
}
const fixturePath = resolve(repoRoot, "test/e2e/test-results/perf-files-fixture.json");

export async function seedFilesFixture(admin: ApiClient, agentId: string): Promise<FixtureState> {
  if (existsSync(fixturePath)) {
    const saved = JSON.parse(readFileSync(fixturePath, "utf8")) as FixtureState;
    const probe = await admin.get(`/api/agents/${saved.agentId}/sessions/${saved.sessionId}/messages`);
    if (probe.status === 200) return saved;
  }
  const sessionId = await newSession(admin, agentId);
  const imagePaths: string[] = [];
  const pdfPaths: string[] = [];
  for (let index = 1; index <= imgCount; index += 1) {
    const uploaded = expectStatus(
      await admin.upload<{ path: string; }>(
        `/api/agents/${agentId}/sessions/${sessionId}/workspace/upload`,
        `img-${index}.png`,
        noisePng(),
        "image/png",
      ),
      201,
      `upload image ${index}`,
    );
    imagePaths.push(uploaded.path);
    await send(admin, agentId, sessionId, `[file: ${uploaded.path}]\nplease look at image ${index}`);
  }
  for (let index = 1; index <= pdfCount; index += 1) {
    const uploaded = expectStatus(
      await admin.upload<{ path: string; }>(
        `/api/agents/${agentId}/sessions/${sessionId}/workspace/upload`,
        `doc-${index}.pdf`,
        minimalPdf(),
        "application/pdf",
      ),
      201,
      `upload PDF ${index}`,
    );
    pdfPaths.push(uploaded.path);
    await send(admin, agentId, sessionId, `[file: ${uploaded.path}]\nplease read document ${index}`);
  }
  const state = { agentId, sessionId, imagePaths, pdfPaths };
  mkdirSync(resolve(repoRoot, "test/e2e/test-results"), { recursive: true });
  writeFileSync(fixturePath, JSON.stringify(state, null, 2));
  return state;
}
