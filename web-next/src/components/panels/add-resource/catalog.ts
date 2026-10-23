import type { ResourceKind } from "./api";

export type ResourceCategory = "Source" | "Data" | "Storage" | "Template";

export type AddResourceDefinition = {
  kind: ResourceKind;
  title: string;
  detail: string;
  category: ResourceCategory;
  keywords: readonly string[];
  icon: string;
};

export const ADD_RESOURCE_CATALOG = [
  resource("github", "GitHub repo", "Create a service linked to a granted repository.", "Source", ["git", "repository", "branch", "code"], "GH"),
  resource("image", "Container image", "Create a service from an immutable image digest.", "Source", ["docker", "oci", "registry", "digest"], "<>"),
  resource("empty", "Empty service", "Start with a service and connect its source later.", "Source", ["blank", "service", "manual"], "+"),
  resource("database", "Database", "Provision an available official database template.", "Data", ["postgres", "mysql", "redis", "mongodb", "sql"], "DB"),
  resource("volume", "Volume", "Attach persistent storage to an existing service.", "Storage", ["disk", "mount", "persistent", "data"], "[]"),
  resource("template", "Template", "Provision an available official application template.", "Template", ["starter", "catalog", "app", "stack"], "T"),
] as const satisfies readonly AddResourceDefinition[];

export const RESOURCE_CATEGORIES: readonly ResourceCategory[] = [
  "Source",
  "Data",
  "Storage",
  "Template",
];

export function resourceDefinition(kind: ResourceKind): AddResourceDefinition {
  return ADD_RESOURCE_CATALOG.find((resource) => resource.kind === kind)!;
}

export function searchResourceCatalog(query: string): AddResourceDefinition[] {
  const terms = query
    .trim()
    .toLocaleLowerCase()
    .split(/\s+/)
    .filter(Boolean);
  if (terms.length === 0) return [...ADD_RESOURCE_CATALOG];
  return ADD_RESOURCE_CATALOG.filter((candidate) => {
    const haystack = [
      candidate.title,
      candidate.detail,
      candidate.category,
      ...candidate.keywords,
    ]
      .join(" ")
      .toLocaleLowerCase();
    return terms.every((term) => haystack.includes(term));
  });
}

function resource(
  kind: ResourceKind,
  title: string,
  detail: string,
  category: ResourceCategory,
  keywords: readonly string[],
  icon: string,
): AddResourceDefinition {
  return { kind, title, detail, category, keywords, icon };
}
