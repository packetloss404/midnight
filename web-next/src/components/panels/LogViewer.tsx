// LogViewer — live + historical log stream for an operation.
//
// PROP CONTRACT (stable):
//   operationId?  operation whose logs to show (undefined = idle)
//   follow?       start in live-tail mode + auto-scroll to newest (default true)
//   height?       viewport height (px); defaults to a drawer-friendly size
//
// Operation and scoped runtime evidence are deliberately rendered as distinct
// sources. Supplying a runtime scope must never suppress operation history or
// events when an operation id is also present.

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useOperationEvents } from "@/api/hooks";
import { Empty, ErrorState, Loading } from "@/components/states";
import {
  eventToLine,
  useObservabilityLogHistory,
  useObservabilityLogStream,
  useOperationLogHistory,
  type LogLine,
  type ObservabilityLogScope,
} from "./logs/api";
import { compileLogQuery, type LogLevel } from "./logs/filter";

export type LogViewerProps = {
  operationId?: string;
  follow?: boolean;
  height?: number;
  scope?: ObservabilityLogScope;
};

// Keep memory + DOM bounded on long streams.
const MAX_MERGED = 5000;
const MAX_RENDERED = 1500;
// Distance (px) from the bottom within which we consider the view "pinned".
const PIN_THRESHOLD = 48;

const LEVEL_STYLE: Record<LogLevel, { dot: string; text: string; label: string }> = {
  error: { dot: "var(--danger)", text: "text-danger", label: "text-danger" },
  warn: { dot: "var(--warn)", text: "text-warn", label: "text-warn" },
  info: { dot: "var(--info)", text: "text-muted", label: "text-info" },
  debug: { dot: "var(--subtle)", text: "text-subtle", label: "text-subtle" },
};

export function LogViewer({
  operationId,
  follow = true,
  height = 320,
  scope,
}: LogViewerProps) {
  if (operationId && scope) {
    const sourceHeight = Math.max(240, Math.floor(height / 2));
    return (
      <div className="grid gap-4" data-log-sources="operation,runtime">
        <LogSourceSection
          title="Build & deploy logs"
          detail="Deployment history plus live events."
          source="operation"
        >
          <LogSourceViewer
            operationId={operationId}
            follow={follow}
            height={sourceHeight}
          />
        </LogSourceSection>
        <LogSourceSection
          title="Runtime logs"
          detail="Application and runtime logs scoped to this deployment. This source reports an error when the observability store or route is unavailable."
          source="runtime"
        >
          <LogSourceViewer scope={scope} follow={follow} height={sourceHeight} />
        </LogSourceSection>
      </div>
    );
  }
  return (
    <LogSourceViewer
      operationId={operationId}
      scope={scope}
      follow={follow}
      height={height}
    />
  );
}

function LogSourceSection({
  title,
  detail,
  source,
  children,
}: {
  title: string;
  detail: string;
  source: "operation" | "runtime";
  children: ReactNode;
}) {
  return (
    <section className="grid gap-2" data-log-source={source}>
      <header className="grid gap-0.5">
        <h3 className="m-0 text-[10px] font-semibold uppercase tracking-wider text-subtle">
          {title}
        </h3>
        <p className="m-0 text-xs text-muted">{detail}</p>
      </header>
      {children}
    </section>
  );
}

function LogSourceViewer({
  operationId,
  follow = true,
  height = 320,
  scope,
}: LogViewerProps) {
  const [tail, setTail] = useState(follow);
  const [showTimestamps, setShowTimestamps] = useState(true);
  const [query, setQuery] = useState("");
  const [pinned, setPinned] = useState(true);
  const [copied, setCopied] = useState(false);

  const scrollRef = useRef<HTMLDivElement | null>(null);
  const bottomRef = useRef<HTMLDivElement | null>(null);

  const operationHistory = useOperationLogHistory(operationId);
  const observabilityHistory = useObservabilityLogHistory(scope);
  const history = scope ? observabilityHistory : operationHistory;
  // Live tail owns its SSE connection; gate it by the tail toggle so turning
  // tail off actually closes the socket.
  const operationStream = useOperationEvents(
    tail ? operationId : undefined,
  );
  const observabilityStream = useObservabilityLogStream(scope, tail);
  const connected = scope ? observabilityStream.connected : operationStream.connected;
  const sseError = scope ? observabilityStream.error : operationStream.error;

  // ── Merge history + live events into one time-ordered list ──────────
  const merged = useMemo<LogLine[]>(() => {
    const historyLines = history.data?.lines ?? [];
    const eventLines = scope
      ? observabilityStream.lines
      : operationStream.events.map(eventToLine);
    const unique = new Map<string, LogLine>();
    for (const line of [...historyLines, ...eventLines]) unique.set(line.id, line);
    const all = [...unique.values()];
    all.sort((a, b) => {
      if (a.tsMs !== b.tsMs) return a.tsMs - b.tsMs;
      if (a.seq != null && b.seq != null) return a.seq - b.seq;
      return 0;
    });
    // Cap to the most-recent N so a huge history never blows up memory.
    return all.length > MAX_MERGED ? all.slice(all.length - MAX_MERGED) : all;
  }, [history.data, observabilityStream.lines, operationStream.events, scope]);

  // ── Compile the filter + apply it ───────────────────────────────────
  const compiled = useMemo(() => compileLogQuery(query), [query]);
  const filtered = useMemo(() => {
    const rows = compiled.active ? merged.filter(compiled.predicate) : merged;
    return rows.length > MAX_RENDERED ? rows.slice(rows.length - MAX_RENDERED) : rows;
  }, [merged, compiled]);

  const overflowed = merged.length >= MAX_MERGED;

  // ── Pause-on-scroll: track whether the view is pinned to the bottom ──
  const handleScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const distance = el.scrollHeight - el.scrollTop - el.clientHeight;
    setPinned(distance <= PIN_THRESHOLD);
  }, []);

  const jumpToBottom = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
    setPinned(true);
  }, []);

  // Auto-scroll only while tailing AND the user hasn't scrolled up.
  useEffect(() => {
    if (tail && pinned) {
      bottomRef.current?.scrollIntoView({ block: "end" });
    }
  }, [filtered, tail, pinned]);

  // ── Copy / download ─────────────────────────────────────────────────
  const serialize = useCallback(
    () =>
      filtered
        .map((l) => {
          const ts = l.ts ? `${l.ts} ` : "";
          return `${ts}[${l.level}] ${l.source}: ${l.message}`;
        })
        .join("\n"),
    [filtered],
  );

  const handleCopy = useCallback(() => {
    const text = serialize();
    void navigator.clipboard?.writeText(text).then(
      () => {
        setCopied(true);
        window.setTimeout(() => setCopied(false), 1500);
      },
      () => setCopied(false),
    );
  }, [serialize]);

  const handleDownload = useCallback(() => {
    const blob = new Blob([serialize()], { type: "text/plain;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `midnight-logs-${operationId ?? "deployment"}.log`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  }, [serialize, operationId]);

  // ── Idle / loading / error gates ────────────────────────────────────
  if (!operationId && !scope) {
    return (
      <Empty
        title="No deployment selected"
        detail="Select a deployment to stream its build and deploy logs."
        action={<span className="text-xs text-subtle">Pick a deployment above</span>}
      />
    );
  }

  const historyPending = history.isPending;
  const hasAnything = merged.length > 0;

  if (historyPending && !hasAnything && !tail) {
    return <Loading label="Loading logs…" />;
  }

  if (history.error && !hasAnything) {
    return (
      <ErrorState
        title="Failed to load logs"
        error={history.error}
        onRetry={() => history.refetch()}
      />
    );
  }

  // ── Connection status ───────────────────────────────────────────────
  const status: { label: string; tone: string; live: boolean } = !tail
    ? { label: "paused", tone: "var(--subtle)", live: false }
    : sseError
      ? { label: "reconnecting", tone: "var(--warn)", live: false }
      : connected
        ? { label: "streaming", tone: "var(--accent)", live: true }
        : { label: "connecting", tone: "var(--subtle)", live: false };

  const activeUnavailable = (history.data?.unavailable ?? []).filter((s) => !s.available);

  return (
    <div
      className="flex flex-col overflow-hidden rounded border border-line bg-bg-2"
      style={{ height }}
    >
      {/* ── Toolbar ───────────────────────────────────────────────── */}
      <div className="flex flex-wrap items-center gap-2 border-b border-line-soft bg-surface px-2.5 py-2">
        <span className="inline-flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wider text-subtle">
          <span
            className={`h-1.5 w-1.5 rounded-full ${status.live ? "animate-pulse" : ""}`}
            style={{ background: status.tone }}
            aria-hidden="true"
          />
          {status.label}
        </span>

        <div className="mx-1 h-4 w-px bg-line" aria-hidden="true" />

        <ToggleButton active={tail} onClick={() => setTail((v) => !v)} title="Toggle live tail">
          Live tail
        </ToggleButton>
        <ToggleButton
          active={showTimestamps}
          onClick={() => setShowTimestamps((v) => !v)}
          title="Toggle timestamps"
        >
          Timestamps
        </ToggleButton>

        <div className="ml-auto flex items-center gap-1.5">
          <span className="font-mono text-[10px] text-subtle" aria-live="polite">
            {filtered.length}
            {compiled.active && filtered.length !== merged.length ? `/${merged.length}` : ""} lines
          </span>
          <IconButton onClick={handleCopy} title="Copy visible logs">
            {copied ? "Copied" : "Copy"}
          </IconButton>
          <IconButton onClick={handleDownload} title="Download visible logs">
            Download
          </IconButton>
        </div>
      </div>

      {/* ── Filter bar ────────────────────────────────────────────── */}
      <div className="border-b border-line-soft bg-surface px-2.5 pb-2">
        <div className="flex items-center gap-2">
          <input
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            spellCheck={false}
            autoCapitalize="off"
            autoCorrect="off"
            placeholder="Filter — free text, @level:error, @source:build, AND / OR / NOT, -exclude"
            aria-label="Filter logs"
            className="w-full rounded-sm border border-line bg-bg px-2.5 py-1.5 font-mono text-[11px] text-text placeholder:text-subtle focus:border-accent focus:outline-none"
          />
          {query ? (
            <button
              type="button"
              onClick={() => setQuery("")}
              className="shrink-0 rounded-sm border border-line px-2 py-1.5 text-[10px] text-subtle hover:bg-surface-2 hover:text-text"
            >
              Clear
            </button>
          ) : null}
        </div>
        {compiled.error ? (
          <p className="m-0 mt-1 font-mono text-[10px] text-warn">
            filter fell back to plain text: {compiled.error}
          </p>
        ) : null}
      </div>

      {/* ── Log body ──────────────────────────────────────────────── */}
      <div className="relative min-h-0 flex-1">
        <div
          ref={scrollRef}
          onScroll={handleScroll}
          className="h-full overflow-auto bg-bg px-2.5 py-2 font-mono text-[11px] leading-relaxed"
          role="log"
          aria-live={tail && pinned ? "polite" : "off"}
          aria-label="Log output"
        >
          {overflowed ? (
            <p className="m-0 mb-1 text-[10px] italic text-subtle">
              …showing the most recent {MAX_MERGED.toLocaleString()} lines
            </p>
          ) : null}

          {activeUnavailable.length > 0 && merged.length === 0 ? (
            <p className="m-0 text-subtle">
              No backed log store for {activeUnavailable.map((s) => s.surface).join(", ")}.
              {history.data?.message ? ` ${history.data.message}` : ""}
            </p>
          ) : null}

          {filtered.length === 0 ? (
            <p className="m-0 text-subtle">
              {merged.length === 0
                ? tail
                  ? "Waiting for log output…"
                  : "No log entries for this deployment yet."
                : "No lines match the current filter."}
            </p>
          ) : (
            filtered.map((line) => (
              <LogRow key={line.id} line={line} showTimestamp={showTimestamps} />
            ))
          )}
          <div ref={bottomRef} />
        </div>

        {/* Pause-on-scroll resume affordance */}
        {tail && !pinned ? (
          <button
            type="button"
            onClick={jumpToBottom}
            className="absolute bottom-2 left-1/2 -translate-x-1/2 rounded-full border border-accent bg-accent-soft px-3 py-1 text-[10px] font-semibold text-accent shadow hover:bg-surface-3"
          >
            ↓ Tail paused — jump to latest
          </button>
        ) : null}
      </div>
    </div>
  );
}

// ── Row ───────────────────────────────────────────────────────────────

function LogRow({ line, showTimestamp }: { line: LogLine; showTimestamp: boolean }) {
  const style = LEVEL_STYLE[line.level];
  return (
    <div className="flex gap-2 whitespace-pre-wrap break-words py-px">
      {showTimestamp ? (
        <span className="shrink-0 select-none text-subtle" aria-hidden="true">
          {formatTs(line.ts)}
        </span>
      ) : null}
      <span
        className={`shrink-0 select-none uppercase ${style.label}`}
        style={{ width: "3.25rem" }}
        title={`level: ${line.level}`}
      >
        {line.level}
      </span>
      <span className="shrink-0 select-none text-subtle" title={`source: ${line.source}`}>
        {line.source}
      </span>
      <span className={style.text}>{line.message}</span>
    </div>
  );
}

function formatTs(ts: string): string {
  if (!ts) return "--:--:--";
  const ms = Date.parse(ts);
  if (Number.isNaN(ms)) return ts;
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

// ── Toolbar controls ──────────────────────────────────────────────────

function ToggleButton({
  active,
  onClick,
  title,
  children,
}: {
  active: boolean;
  onClick: () => void;
  title: string;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      aria-pressed={active}
      className={
        "rounded-sm border px-2 py-1 text-[10px] font-semibold transition-colors " +
        (active
          ? "border-accent bg-accent-soft text-accent"
          : "border-line bg-surface-2 text-subtle hover:text-text")
      }
    >
      {children}
    </button>
  );
}

function IconButton({
  onClick,
  title,
  children,
}: {
  onClick: () => void;
  title: string;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      className="rounded-sm border border-line bg-surface-2 px-2 py-1 text-[10px] font-semibold text-subtle transition-colors hover:text-text"
    >
      {children}
    </button>
  );
}
