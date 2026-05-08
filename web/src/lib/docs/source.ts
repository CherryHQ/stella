import { docs } from "collections/server";
import { loader } from "fumadocs-core/source";
import { lucideIconsPlugin } from "fumadocs-core/source/lucide-icons";
import { i18n } from "@/lib/docs/i18n";

export const source = loader({
  source: docs.toFumadocsSource(),
  baseUrl: "/docs",
  i18n: { ...i18n, hideLocale: "default-locale" },
  plugins: [lucideIconsPlugin()],
});
