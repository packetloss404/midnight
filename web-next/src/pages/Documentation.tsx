import { Link } from "@tanstack/react-router";
import { useEffect, useRef, type ReactNode } from "react";
import {
  DOCUMENTATION_LINKS,
  type DocumentationSection,
} from "@/lib/documentation";

export function Documentation({
  section,
  children,
}: {
  section: DocumentationSection;
  children: ReactNode;
}) {
  const activeLinkRef = useRef<HTMLAnchorElement>(null);

  useEffect(() => {
    activeLinkRef.current?.scrollIntoView?.({
      behavior: "auto",
      block: "nearest",
      inline: "center",
    });
  }, [section]);

  return (
    <section className="flex min-w-0 w-full flex-col">
      <div className="mx-auto w-full max-w-5xl px-[var(--main-gutter)] pt-[var(--main-gutter)]">
        <nav
          aria-label="Documentation"
          className="flex max-w-full gap-1 overflow-x-auto border-b border-line"
        >
          {DOCUMENTATION_LINKS.map((item) => (
            <Link
              key={item.section}
              to={item.to}
              ref={item.section === section ? activeLinkRef : undefined}
              aria-current={item.section === section ? "page" : undefined}
              activeOptions={{ exact: true }}
              className="shrink-0 border-b-2 border-transparent px-3 py-2 text-xs font-semibold text-muted hover:text-text [&.active]:border-accent [&.active]:text-accent"
            >
              {item.label}
            </Link>
          ))}
        </nav>
      </div>

      <div className="min-w-0">{children}</div>
    </section>
  );
}
