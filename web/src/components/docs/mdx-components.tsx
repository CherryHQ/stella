import type { ComponentPropsWithoutRef, ReactElement, ReactNode } from "react";
import { isValidElement } from "react";
import { Link } from "@tanstack/react-router";
import { Mermaid } from "./Mermaid";

function slugify(text: string): string {
  return text
    .toString()
    .toLowerCase()
    .replace(/\s+/g, "-")
    .replace(/[^\w-]+/g, "")
    .replace(/--+/g, "-")
    .trim();
}

function Heading({
  level,
  children,
  ...props
}: { level: 1 | 2 | 3 | 4 | 5 | 6; children?: ReactNode } & ComponentPropsWithoutRef<"h1">) {
  const Tag = `h${level}` as const;
  const id = typeof children === "string" ? slugify(children) : props.id;
  const sizes: Record<number, string> = {
    1: "text-3xl font-semibold mt-8 mb-4",
    2: "text-2xl font-semibold mt-8 mb-3 border-b border-border pb-2",
    3: "text-xl font-semibold mt-6 mb-2",
    4: "text-lg font-medium mt-4 mb-2",
    5: "text-base font-medium mt-4 mb-1",
    6: "text-sm font-medium mt-4 mb-1",
  };
  return (
    <Tag id={id} className={`${sizes[level]} text-foreground scroll-mt-20`} {...props}>
      {children}
    </Tag>
  );
}

export function Cards({ children }: { children: ReactNode }) {
  return <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 my-6">{children}</div>;
}

export function Card({
  title,
  description,
  href,
}: {
  title: string;
  description?: string;
  href?: string;
  icon?: ReactNode;
}) {
  const content = (
    <div className="rounded-lg border border-border p-4 hover:bg-accent/50 transition-colors">
      <h3 className="text-sm font-semibold text-foreground mb-1">{title}</h3>
      {description && <p className="text-sm text-muted-foreground">{description}</p>}
    </div>
  );
  if (href) {
    // SAFETY: href is the MDX author's internal route, coerced to Link's route-union type.
    const to = href as never;
    return (
      <Link to={to} className="no-underline">
        {content}
      </Link>
    );
  }
  return content;
}

export const mdxComponents = {
  h1: (props: ComponentPropsWithoutRef<"h1">) => <Heading level={1} {...props} />,
  h2: (props: ComponentPropsWithoutRef<"h2">) => <Heading level={2} {...props} />,
  h3: (props: ComponentPropsWithoutRef<"h3">) => <Heading level={3} {...props} />,
  h4: (props: ComponentPropsWithoutRef<"h4">) => <Heading level={4} {...props} />,
  h5: (props: ComponentPropsWithoutRef<"h5">) => <Heading level={5} {...props} />,
  h6: (props: ComponentPropsWithoutRef<"h6">) => <Heading level={6} {...props} />,
  p: (props: ComponentPropsWithoutRef<"p">) => (
    <p className="text-muted-foreground leading-7 mb-4" {...props} />
  ),
  a: (props: ComponentPropsWithoutRef<"a">) => {
    const { href, children, ...rest } = props;
    const cls =
      "text-foreground underline underline-offset-4 decoration-border hover:decoration-foreground transition-colors";
    if (href?.startsWith("/")) {
      // SAFETY: a leading-"/" href is an internal route, coerced to Link's route-union type.
      const to = href as never;
      return (
        <Link to={to} className={cls}>
          {children}
        </Link>
      );
    }
    return (
      <a href={href} target="_blank" rel="noopener noreferrer" className={cls} {...rest}>
        {children}
      </a>
    );
  },
  ul: (props: ComponentPropsWithoutRef<"ul">) => (
    <ul className="list-disc pl-6 mb-4 space-y-1 text-muted-foreground" {...props} />
  ),
  ol: (props: ComponentPropsWithoutRef<"ol">) => (
    <ol className="list-decimal pl-6 mb-4 space-y-1 text-muted-foreground" {...props} />
  ),
  li: (props: ComponentPropsWithoutRef<"li">) => <li className="leading-7" {...props} />,
  blockquote: (props: ComponentPropsWithoutRef<"blockquote">) => (
    <blockquote
      className="border-l-2 border-border pl-4 my-4 text-muted-foreground italic"
      {...props}
    />
  ),
  code: ({ children, ...props }: ComponentPropsWithoutRef<"code">) => {
    if (typeof children === "string" && !children.includes("\n")) {
      return (
        <code
          className="bg-muted px-1.5 py-0.5 rounded text-sm font-mono text-foreground"
          {...props}
        >
          {children}
        </code>
      );
    }
    return (
      <code className="text-sm font-mono" {...props}>
        {children}
      </code>
    );
  },
  pre: (props: ComponentPropsWithoutRef<"pre">) => {
    const child = props.children;
    if (isValidElement(child)) {
      // SAFETY: pre's child is the <code> element by MDX convention, whose props carry className/children.
      const codeProps = (child as ReactElement<{ className?: string; children?: ReactNode }>).props;
      if (
        codeProps.className?.includes("language-mermaid") &&
        typeof codeProps.children === "string"
      ) {
        return <Mermaid chart={codeProps.children.replace(/\n$/, "")} />;
      }
    }
    return (
      <pre
        className="bg-muted rounded-lg p-4 my-4 overflow-x-auto text-sm leading-6 font-mono"
        {...props}
      />
    );
  },
  table: (props: ComponentPropsWithoutRef<"table">) => (
    <div className="my-4 overflow-x-auto">
      <table className="w-full text-sm border-collapse" {...props} />
    </div>
  ),
  thead: (props: ComponentPropsWithoutRef<"thead">) => (
    <thead className="border-b border-border" {...props} />
  ),
  th: (props: ComponentPropsWithoutRef<"th">) => (
    <th className="text-left font-semibold text-foreground px-3 py-2" {...props} />
  ),
  td: (props: ComponentPropsWithoutRef<"td">) => (
    <td className="text-muted-foreground px-3 py-2 border-b border-border" {...props} />
  ),
  hr: () => <hr className="my-8 border-border" />,
  img: (props: ComponentPropsWithoutRef<"img">) => (
    <img className="rounded-lg my-4 max-w-full" {...props} />
  ),
  strong: (props: ComponentPropsWithoutRef<"strong">) => (
    <strong className="font-semibold text-foreground" {...props} />
  ),
  Cards,
  Card,
};
