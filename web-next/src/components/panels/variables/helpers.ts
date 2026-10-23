// Pure helpers for the Variables editor: `.env` <-> rows, and the `${{ ... }}`
// reference-autocomplete engine. No React, no I/O — kept separate so they can
// be unit-tested and reused by both the table and RAW editors.

import type { VarScope } from "@/lib/stagedChanges";

// ── .env round-trip ────────────────────────────────────────────────

export type EnvRow = { name: string; value: string };

/**
 * Parse a `.env`-style blob into ordered rows. Supports `KEY=value`,
 * `export KEY=value`, `#` comments, blank lines, and single/double quoted
 * values (quotes stripped). Later duplicates win, matching dotenv semantics.
 */
export function parseEnv(text: string): EnvRow[] {
  const rows = new Map<string, string>();
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const body = line.startsWith("export ") ? line.slice(7).trim() : line;
    const eq = body.indexOf("=");
    if (eq <= 0) continue;
    const name = body.slice(0, eq).trim();
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) continue;
    let value = body.slice(eq + 1).trim();
    if (
      (value.startsWith('"') && value.endsWith('"') && value.length >= 2) ||
      (value.startsWith("'") && value.endsWith("'") && value.length >= 2)
    ) {
      value = value.slice(1, -1);
    }
    rows.set(name, value);
  }
  return Array.from(rows, ([name, value]) => ({ name, value }));
}

/** Serialise rows to `.env`. Values with whitespace/`#`/`=` are double-quoted. */
export function serializeEnv(rows: readonly EnvRow[]): string {
  return rows
    .map(({ name, value }) => {
      const needsQuote = /[\s#"'=]/.test(value) || value === "";
      const out = needsQuote ? `"${value.replace(/"/g, '\\"')}"` : value;
      return `${name}=${out}`;
    })
    .join("\n");
}

// ── reference autocomplete ─────────────────────────────────────────
// Mirrors `internal/varrefs/parser.go`:
//   ${{Service.VAR}}  → cross-service    ${{shared.KEY}} → project shared
//   ${{VAR}}          → same-service      ${{this.VAR}}   → builtin

export type RefSuggestion = {
  /** Text inserted at the caret (replaces the active token body). */
  insert: string;
  /** Human label. */
  label: string;
  kind: "shared" | "builtin" | "service" | "local";
};

const OPEN = "${{";

/** The open `${{`…caret token the caret currently sits inside, if any. */
export type ActiveRefToken = { start: number; body: string };

/**
 * Return the reference token the caret is editing, or null. A token is active
 * when the nearest preceding `${{` before the caret has no closing `}}` yet.
 */
export function activeRefToken(value: string, caret: number): ActiveRefToken | null {
  const head = value.slice(0, caret);
  const open = head.lastIndexOf(OPEN);
  if (open === -1) return null;
  const between = head.slice(open + OPEN.length);
  if (between.includes("}}")) return null;
  return { start: open + OPEN.length, body: between };
}

export type RefContext = {
  /** Other service slugs in the environment (for `Service.VAR`). */
  serviceNames: string[];
  /** Variable names on the current service (for same-service `VAR`). */
  localNames: string[];
  /** Known project shared keys (for `shared.KEY`), if available. */
  sharedNames?: string[];
};

/** Rank suggestions for the active token body (already inside `${{`). */
export function suggestRefs(token: string, ctx: RefContext): RefSuggestion[] {
  const body = token.trimStart();
  const out: RefSuggestion[] = [];

  const dot = body.indexOf(".");
  if (dot === -1) {
    // First segment: sentinels, services, and same-service locals.
    const q = body.toLowerCase();
    const match = (s: string) => s.toLowerCase().startsWith(q);
    if (match("shared"))
      out.push({ insert: "shared.", label: "shared.", kind: "shared" });
    if (match("this"))
      out.push({ insert: "this.", label: "this.", kind: "builtin" });
    for (const svc of ctx.serviceNames)
      if (match(svc)) out.push({ insert: `${svc}.`, label: `${svc}.`, kind: "service" });
    for (const name of ctx.localNames)
      if (match(name)) out.push({ insert: name, label: name, kind: "local" });
    return out.slice(0, 8);
  }

  // Second segment: `<prefix>.<partial>`.
  const prefix = body.slice(0, dot);
  const partial = body.slice(dot + 1).toLowerCase();
  const pool =
    prefix.toLowerCase() === "shared"
      ? ctx.sharedNames ?? []
      : prefix.toLowerCase() === "this"
        ? ctx.localNames
        : ctx.localNames; // cross-service var names are not known client-side
  for (const name of pool)
    if (name.toLowerCase().startsWith(partial))
      out.push({ insert: `${prefix}.${name}`, label: `${prefix}.${name}`, kind: "service" });
  return out.slice(0, 8);
}

/**
 * Replace the active token body with `insert` and append the closing `}}`,
 * returning the new value + caret position. If the caret already sits before a
 * `}}`, that existing closer is reused.
 */
export function applyRefSuggestion(
  value: string,
  caret: number,
  insert: string,
): { value: string; caret: number } {
  const active = activeRefToken(value, caret);
  if (!active) return { value, caret };
  const before = value.slice(0, active.start);
  const after = value.slice(caret);
  const hasCloser = after.startsWith("}}");
  const closer = insert.endsWith(".") ? "" : hasCloser ? "" : "}}";
  const next = `${before}${insert}${closer}${after}`;
  const newCaret = before.length + insert.length + closer.length;
  return { value: next, caret: newCaret };
}

// ── scope display ──────────────────────────────────────────────────

export const SCOPE_LABEL: Record<VarScope, string> = {
  plain: "Plain",
  secret: "Secret",
  sealed: "Sealed",
};

/** Whether a scope hides its value (write-only). */
export function isWriteOnly(scope: VarScope): boolean {
  return scope === "sealed";
}
