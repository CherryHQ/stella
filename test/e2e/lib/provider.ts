import { ApiClient, expectStatus } from "./api.ts";
import { openAIEnv } from "./env.ts";

export interface ConfiguredModel {
  providerId: string;
  modelRef: string;
}

const providerName = "e2e-openai";

// Registers the OPENAI_* credentials as an OpenAI-compatible provider and makes
// its model the deployment default, so every agent turn in the run uses it.
// Idempotent: a second call finds the provider by name.
export async function ensureProvider(admin: ApiClient): Promise<ConfiguredModel> {
  const env = openAIEnv();
  const list = expectStatus(await admin.get<{ providers: { id: string; name: string; }[]; }>("/api/providers"), 200, "list providers");
  let providerId = list.providers.find((p) => p.name === providerName)?.id;
  if (!providerId) {
    const created = expectStatus(
      await admin.post<{ id: string; }>("/api/providers", {
        id: providerName,
        type: "openai",
        name: providerName,
        enabled: true,
        api_key: env.apiKey,
        base_url: env.baseURL,
        model_policy: "allow_all",
        models: { [env.model]: { id: env.model, enabled: true, input: ["text"] } },
      }),
      201,
      "create provider",
    );
    providerId = created.id;
  }
  const modelRef = `${providerId}/${env.model}`;
  const current = expectStatus(await admin.get<Record<string, string>>("/api/default-models"), 200, "get default models");
  if (current.model !== modelRef) {
    expectStatus(
      await admin.put("/api/default-models", {
        model: modelRef,
        model_thinking: "",
        model_strong: "",
        model_strong_thinking: "",
        model_fast: "",
        model_fast_thinking: "",
        model_vision: "",
        model_embedding: "",
      }),
      200,
      "set default models",
    );
  }
  return { providerId, modelRef };
}
