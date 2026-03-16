import type { BaseLayoutProps } from "fumadocs-ui/layouts/shared";

export const gitConfig = {
  user: "vaayne",
  repo: "anna",
  branch: "main",
};

export function baseOptions(): BaseLayoutProps {
  return {
    nav: {
      title: (
        <>
          <img
            src="/favicon.png"
            alt="anna"
            width={24}
            height={24}
            className="rounded-sm"
          />
          anna
        </>
      ),
    },
    links: [
      { text: "About", url: "/about" },
      { text: "Docs", url: "/docs" },
    ],
    githubUrl: `https://github.com/${gitConfig.user}/${gitConfig.repo}`,
  };
}
