import { defineConfig } from "@hey-api/openapi-ts";

export default defineConfig({
  input: "../api/spec/openapi.yaml",
  output: {
    path: "src/lib/api-client",
    postProcess: ["prettier"],
  },
  plugins: [
    {
      name: "@hey-api/client-fetch",
      baseUrl: false,
    },
    { name: "@tanstack/react-query" },
  ],
});
