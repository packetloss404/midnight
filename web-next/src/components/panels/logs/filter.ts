// Structured log-filter parser for the LogViewer.
//
// Supports a Railway-style query language over normalized log lines:
//   - free text            → case-insensitive substring over message + fields
//   - `@key:value`         → structured match on a field (level, source, msg,
//                            or any entry field). Quote the value to include
//                            spaces: `@message:"build failed"`.
//   - boolean AND / OR / NOT (case-insensitive), `-term` shorthand for NOT,
//     and `( … )` grouping. Adjacent terms imply AND.
//
// Precedence (tightest first): NOT → AND → OR. Parsing is resilient: a
// malformed query degrades to a plain free-text match and reports the error
// so the UI can surface it without going blank.

/** The four levels the viewer renders and colors. */
export type LogLevel = "error" | "warn" | "info" | "debug";

export interface FilterLine {
  message: string;
  level: string;
  source: string;
  fields: Record<string, unknown>;
}

type Node =
  | { t: "and"; a: Node; b: Node }
  | { t: "or"; a: Node; b: Node }
  | { t: "not"; a: Node }
  | { t: "text"; v: string }
  | { t: "field"; k: string; v: string }
  | { t: "true" };

type Tok =
  | { kind: "lparen" }
  | { kind: "rparen" }
  | { kind: "and" }
  | { kind: "or" }
  | { kind: "not" }
  | { kind: "term"; value: string };

// ── Tokenizer ───────────────────────────────────────────────────────

function tokenize(input: string): Tok[] {
  const toks: Tok[] = [];
  let i = 0;
  const n = input.length;

  const readQuoted = (): string => {
    // assumes input[i] === '"'
    i += 1;
    let out = "";
    while (i < n && input[i] !== '"') {
      out += input[i];
      i += 1;
    }
    if (i < n) i += 1; // closing quote
    return out;
  };

  while (i < n) {
    const ch = input[i];
    if (ch === " " || ch === "\t" || ch === "\n") {
      i += 1;
      continue;
    }
    if (ch === "(") {
      toks.push({ kind: "lparen" });
      i += 1;
      continue;
    }
    if (ch === ")") {
      toks.push({ kind: "rparen" });
      i += 1;
      continue;
    }
    // A term: run of non-space chars, with quoted segments allowed inside
    // (so `@message:"a b"` stays one token).
    let word = "";
    while (i < n) {
      const c = input[i];
      if (c === " " || c === "\t" || c === "\n" || c === "(" || c === ")") break;
      if (c === '"') {
        word += readQuoted();
        continue;
      }
      word += c;
      i += 1;
    }
    if (word === "") continue;
    const upper = word.toUpperCase();
    if (upper === "AND") toks.push({ kind: "and" });
    else if (upper === "OR") toks.push({ kind: "or" });
    else if (upper === "NOT") toks.push({ kind: "not" });
    else toks.push({ kind: "term", value: word });
  }
  return toks;
}

// ── Recursive-descent parser ────────────────────────────────────────

class Parser {
  private pos = 0;
  constructor(private readonly toks: Tok[]) {}

  private peek(): Tok | undefined {
    return this.toks[this.pos];
  }
  private next(): Tok | undefined {
    return this.toks[this.pos++];
  }

  parse(): Node {
    const node = this.parseOr();
    if (this.pos < this.toks.length) {
      throw new Error("unexpected token in filter");
    }
    return node;
  }

  private parseOr(): Node {
    let left = this.parseAnd();
    for (;;) {
      const t = this.peek();
      if (t && t.kind === "or") {
        this.next();
        left = { t: "or", a: left, b: this.parseAnd() };
      } else break;
    }
    return left;
  }

  private parseAnd(): Node {
    let left = this.parseNot();
    for (;;) {
      const t = this.peek();
      if (!t) break;
      if (t.kind === "and") {
        this.next();
        left = { t: "and", a: left, b: this.parseNot() };
        continue;
      }
      // Implicit AND: a term / not / group directly follows.
      if (t.kind === "term" || t.kind === "not" || t.kind === "lparen") {
        left = { t: "and", a: left, b: this.parseNot() };
        continue;
      }
      break;
    }
    return left;
  }

  private parseNot(): Node {
    const t = this.peek();
    if (t && t.kind === "not") {
      this.next();
      return { t: "not", a: this.parseNot() };
    }
    return this.parsePrimary();
  }

  private parsePrimary(): Node {
    const t = this.next();
    if (!t) throw new Error("unexpected end of filter");
    if (t.kind === "lparen") {
      const inner = this.parseOr();
      const close = this.next();
      if (!close || close.kind !== "rparen") {
        throw new Error("missing closing parenthesis");
      }
      return inner;
    }
    if (t.kind === "term") return termNode(t.value);
    throw new Error("expected a search term");
  }
}

function termNode(raw: string): Node {
  // `-term` → NOT term
  if (raw.startsWith("-") && raw.length > 1) {
    return { t: "not", a: termNode(raw.slice(1)) };
  }
  // `@key:value` → field match
  if (raw.startsWith("@")) {
    const rest = raw.slice(1);
    const colon = rest.indexOf(":");
    if (colon !== -1) {
      const k = rest.slice(0, colon);
      const v = rest.slice(colon + 1);
      return { t: "field", k, v };
    }
    // `@key` with no value → free text on the raw token
  }
  return { t: "text", v: raw };
}

// ── Evaluation ──────────────────────────────────────────────────────

function fieldToString(value: unknown): string {
  if (value == null) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  try {
    return JSON.stringify(value);
  } catch {
    return "";
  }
}

function matchText(needle: string, line: FilterLine): boolean {
  if (needle === "") return true;
  const q = needle.toLowerCase();
  if (line.message.toLowerCase().includes(q)) return true;
  if (line.source.toLowerCase().includes(q)) return true;
  if (line.level.toLowerCase().includes(q)) return true;
  for (const value of Object.values(line.fields)) {
    if (fieldToString(value).toLowerCase().includes(q)) return true;
  }
  return false;
}

function matchField(key: string, value: string, line: FilterLine): boolean {
  const k = key.toLowerCase();
  const v = value.toLowerCase();
  switch (k) {
    case "level":
      // Exact level match (info/warn/error/debug), case-insensitive.
      return v === "" ? line.level !== "" : line.level.toLowerCase() === v;
    case "source":
    case "service":
    case "component":
    case "surface":
      return line.source.toLowerCase().includes(v);
    case "message":
    case "msg":
      return line.message.toLowerCase().includes(v);
    default: {
      const raw = line.fields[key] ?? line.fields[k];
      const str = fieldToString(raw).toLowerCase();
      if (v === "") return str !== "";
      return str.includes(v);
    }
  }
}

function evalNode(node: Node, line: FilterLine): boolean {
  switch (node.t) {
    case "true":
      return true;
    case "text":
      return matchText(node.v, line);
    case "field":
      return matchField(node.k, node.v, line);
    case "not":
      return !evalNode(node.a, line);
    case "and":
      return evalNode(node.a, line) && evalNode(node.b, line);
    case "or":
      return evalNode(node.a, line) || evalNode(node.b, line);
  }
}

export interface CompiledFilter {
  predicate: (line: FilterLine) => boolean;
  error: string | null;
  active: boolean;
}

/**
 * Compile a filter string into a predicate. Never throws: on a parse error it
 * falls back to a plain free-text match over the whole input and reports the
 * error message for display.
 */
export function compileLogQuery(input: string): CompiledFilter {
  const trimmed = input.trim();
  if (trimmed === "") {
    return { predicate: () => true, error: null, active: false };
  }
  try {
    const toks = tokenize(trimmed);
    if (toks.length === 0) {
      return { predicate: () => true, error: null, active: false };
    }
    const ast = new Parser(toks).parse();
    return { predicate: (line) => evalNode(ast, line), error: null, active: true };
  } catch (err) {
    const message = err instanceof Error ? err.message : "invalid filter";
    const needle = trimmed;
    return {
      predicate: (line) => matchText(needle, line),
      error: message,
      active: true,
    };
  }
}
