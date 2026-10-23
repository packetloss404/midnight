export const DOCUMENTATION_LINKS = [
  {
    section: "home",
    to: "/docs",
    label: "Docs home",
    title: "Midnight documentation",
    detail: "Guides and references for operating Midnight.",
  },
  {
    section: "agent-setup",
    to: "/docs/agent-setup",
    label: "Agent Setup",
    title: "Agent Setup",
    detail: "Installation and connection guidance for Midnight agents.",
  },
  {
    section: "api",
    to: "/docs/api",
    label: "API reference",
    title: "API reference",
    detail: "Reference material for Midnight's HTTP API.",
  },
  {
    section: "cli",
    to: "/docs/cli",
    label: "CLI reference",
    title: "CLI reference",
    detail: "Reference material for the Midnight command-line interface.",
  },
] as const;

export type DocumentationSection =
  (typeof DOCUMENTATION_LINKS)[number]["section"];

export function documentationMeta(section: DocumentationSection) {
  return (
    DOCUMENTATION_LINKS.find((item) => item.section === section) ??
    DOCUMENTATION_LINKS[0]
  );
}
