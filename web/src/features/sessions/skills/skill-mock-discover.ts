export interface DiscoverSkill {
  slug: string;
  name: string;
  summary: string;
  version: string;
  installs: number;
}

// TODO(discover): replace this mock catalog with the real discovery endpoint when it lands.
export const MOCK_DISCOVER_SKILLS: DiscoverSkill[] = [
  {
    slug: "web-research",
    name: "web-research",
    summary: "Search the web, read pages, and synthesize answers with citations.",
    version: "1.4.2",
    installs: 12840,
  },
  {
    slug: "pdf-toolkit",
    name: "pdf-toolkit",
    summary: "Extract text and tables from PDFs, merge, split, and fill forms.",
    version: "2.1.0",
    installs: 9320,
  },
  {
    slug: "code-reviewer",
    name: "code-reviewer",
    summary: "Review diffs for bugs, security issues, and style violations.",
    version: "0.9.3",
    installs: 7651,
  },
  {
    slug: "sql-assistant",
    name: "sql-assistant",
    summary: "Write, explain, and optimize SQL queries against your schema.",
    version: "1.0.1",
    installs: 5408,
  },
  {
    slug: "daily-digest",
    name: "daily-digest",
    summary: "Summarize feeds, inboxes, and channels into a morning brief.",
    version: "1.2.0",
    installs: 4977,
  },
  {
    slug: "translate-pro",
    name: "translate-pro",
    summary: "High-quality translation with glossary and tone control.",
    version: "3.0.4",
    installs: 4120,
  },
  {
    slug: "github-ops",
    name: "github-ops",
    summary: "Triage issues, draft PR descriptions, and watch CI runs on GitHub.",
    version: "0.8.0",
    installs: 3866,
  },
];
