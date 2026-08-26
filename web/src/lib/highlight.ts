import type { Highlighter } from "shiki";

let highlighterPromise: Promise<Highlighter> | null = null;

function getHighlighter(): Promise<Highlighter> {
  if (!highlighterPromise) {
    // Dynamic import keeps shiki (and its grammars) out of the main chunk;
    // it only loads once someone actually opens a file.
    highlighterPromise = import("shiki").then(({ createHighlighter }) =>
      createHighlighter({
        themes: ["github-light", "github-dark"],
        langs: [],
      }),
    );
  }
  return highlighterPromise;
}

/** Maps the server's language names (and bare extensions) onto shiki grammars. */
export function shikiLang(language: string): string {
  const map: Record<string, string> = {
    js: "javascript",
    ts: "typescript",
    jsx: "jsx",
    tsx: "tsx",
    py: "python",
    rb: "ruby",
    rs: "rust",
    yml: "yaml",
    md: "markdown",
    sh: "bash",
    zsh: "bash",
    dockerfile: "dockerfile",
    makefile: "makefile",
  };
  return map[language.toLowerCase()] ?? language.toLowerCase();
}

/**
 * Highlights source to HTML, or returns null when there is no grammar for the
 * language. Callers fall back to plain text on null: an unknown extension is
 * normal, not an error.
 */
export async function highlightToHtml(content: string, language: string): Promise<string | null> {
  const lang = shikiLang(language);
  if (!lang) return null;
  try {
    const hl = await getHighlighter();
    // SAFETY: lang is a shiki grammar id; the getLoadedLanguages union accepts it.
    if (!hl.getLoadedLanguages().includes(lang as never)) {
      // SAFETY: lang is a shiki grammar id resolved from shikiLang above.
      await hl.loadLanguage(lang as never);
    }
    const isDark = document.documentElement.classList.contains("dark");
    return hl.codeToHtml(content, { lang, theme: isDark ? "github-dark" : "github-light" });
  } catch {
    return null;
  }
}
