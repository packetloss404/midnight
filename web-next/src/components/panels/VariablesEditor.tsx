// VariablesEditor — service environment variables with STAGED CHANGES.
//
// PROP CONTRACT (stable):
//   serviceId       required
//   projectId?      needed for the REST lifecycle scope + sibling-service refs
//   environmentId?  env scope
//
// The read path is authoritative REST (`GET /variables`, metadata only —
// plaintext is never returned). Edits do NOT apply on change: they accumulate
// in a tab-local input buffer (`lib/stagedChanges.ts`). The floating
// Changes bar (the retained `StagedChangesBanner` export) saves one
// encrypted server-side changeset for explicit review and atomic apply.

import { useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import type { VariableMeta } from "@/api/client";
import { useServices, useVariables } from "@/api/hooks";
import { Loading, ErrorState } from "@/components/states";
import {
  discardChange,
  stageChange,
  isStagedVariableChange,
  useStagedForService,
  type StagedVariableChange,
  type VarScope,
} from "@/lib/stagedChanges";
import {
  isWriteOnly,
  parseEnv,
  SCOPE_LABEL,
  serializeEnv,
  type RefContext,
} from "./variables/helpers";
import { ReferenceInput } from "./variables/ReferenceInput";

export type VariablesEditorProps = {
  serviceId: string;
  projectId?: string;
  environmentId?: string;
};

/** One effective row = server metadata and/or a pending staged change. */
type EffRow = {
  name: string;
  meta?: VariableMeta;
  staged?: StagedVariableChange;
};

const SCOPES: VarScope[] = ["plain", "secret", "sealed"];

export function VariablesEditor({ serviceId, projectId, environmentId }: VariablesEditorProps) {
  const { data, error, isPending, refetch } = useVariables(serviceId, environmentId);
  const staged = useStagedForService(serviceId, environmentId).filter(
    isStagedVariableChange,
  );
  const { data: services } = useServices(projectId, environmentId, { enabled: Boolean(projectId) });
  const [rawMode, setRawMode] = useState(false);

  const server = data ?? [];

  const refContext = useMemo<RefContext>(() => {
    const localNames = new Set<string>();
    const sharedNames = new Set<string>();
    for (const v of server) {
      localNames.add(v.name);
      if (!v.service_id) sharedNames.add(v.name);
    }
    for (const s of staged) localNames.add(s.name);
    return {
      serviceNames: (services ?? [])
        .filter((s) => s.id !== serviceId)
        .map((s) => s.slug),
      localNames: Array.from(localNames).sort(),
      sharedNames: Array.from(sharedNames).sort(),
    };
  }, [server, staged, services, serviceId]);

  const rows = useMemo<EffRow[]>(() => {
    const stagedByName = new Map(staged.map((s) => [s.name, s]));
    const out: EffRow[] = server.map((meta) => ({
      name: meta.name,
      meta,
      staged: stagedByName.get(meta.name),
    }));
    // Staged additions with no server counterpart.
    const serverNames = new Set(server.map((v) => v.name));
    for (const s of staged) {
      if (!serverNames.has(s.name)) out.push({ name: s.name, staged: s });
    }
    return out.sort((a, b) => a.name.localeCompare(b.name));
  }, [server, staged]);

  if (isPending) return <Loading label="Loading variables…" />;
  if (error)
    return <ErrorState title="Failed to load variables" error={error} onRetry={() => refetch()} />;

  const stagedCount = staged.length;

  return (
    <div className="grid gap-3">
      <p
        role="status"
        className="m-0 rounded-[var(--r-sm)] border border-info/40 bg-info-soft px-3 py-2 text-xs text-muted"
      >
        Variables are encrypted and write-only. Saved changes are resolved for the next deployment;
        they do not alter containers that are already running.
      </p>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <h3 className="text-sm font-semibold text-text">Variables</h3>
          {stagedCount > 0 ? (
            <span className="rounded-full border border-staged bg-staged-soft px-2 py-0.5 font-mono text-[10px] font-semibold uppercase tracking-wider text-staged">
              {stagedCount} staged
            </span>
          ) : null}
        </div>
        <div className="flex items-center gap-1.5">
          <ToggleButton active={!rawMode} onClick={() => setRawMode(false)}>
            Table
          </ToggleButton>
          <ToggleButton active={rawMode} onClick={() => setRawMode(true)}>
            Raw
          </ToggleButton>
        </div>
      </div>

      {rawMode ? (
        <RawEditor
          serviceId={serviceId}
          environmentId={environmentId}
          server={server}
          staged={staged}
          onDone={() => setRawMode(false)}
        />
      ) : (
        <TableEditor
          serviceId={serviceId}
          environmentId={environmentId}
          rows={rows}
          refContext={refContext}
        />
      )}
    </div>
  );
}

// ── table ──────────────────────────────────────────────────────────

function TableEditor({
  serviceId,
  environmentId,
  rows,
  refContext,
}: {
  serviceId: string;
  environmentId?: string;
  rows: EffRow[];
  refContext: RefContext;
}) {
  const [adding, setAdding] = useState(false);

  return (
    <div className="grid gap-2">
      <div className="overflow-x-auto rounded border border-line">
        <table className="w-full min-w-[640px] border-collapse text-left text-xs">
          <thead className="bg-surface-2 text-[10px] uppercase tracking-wider text-subtle">
            <tr>
              <th className="w-[26%] px-3 py-2 font-mono font-semibold">Name</th>
              <th className="px-3 py-2 font-mono font-semibold">Value</th>
              <th className="w-[7rem] px-3 py-2 font-mono font-semibold">Scope</th>
              <th className="w-[5rem] px-3 py-2 font-mono font-semibold">Version</th>
              <th className="w-[4rem] px-3 py-2 font-mono font-semibold">Size</th>
              <th className="w-[3rem] px-3 py-2" />
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 && !adding ? (
              <tr>
                <td colSpan={6} className="px-3 py-8 text-center text-muted">
                  No variables yet.
                </td>
              </tr>
            ) : null}
            {rows.map((row) => (
              <VariableRow
                key={row.name}
                serviceId={serviceId}
                environmentId={environmentId}
                row={row}
                refContext={refContext}
              />
            ))}
            {adding ? (
              <AddRow
                serviceId={serviceId}
                environmentId={environmentId}
                existingNames={new Set(rows.map((r) => r.name))}
                refContext={refContext}
                onClose={() => setAdding(false)}
              />
            ) : null}
          </tbody>
        </table>
      </div>
      {!adding ? (
        <div>
          <button
            type="button"
            onClick={() => setAdding(true)}
            className="inline-flex items-center gap-2 rounded-[var(--r-sm)] border border-accent bg-accent-soft px-3 py-1.5 text-xs font-semibold text-accent transition-colors hover:bg-surface-3"
          >
            + Add variable
          </button>
        </div>
      ) : null}
    </div>
  );
}

function VariableRow({
  serviceId,
  environmentId,
  row,
  refContext,
}: {
  serviceId: string;
  environmentId?: string;
  row: EffRow;
  refContext: RefContext;
}) {
  const { meta, staged } = row;
  const sealed = staged ? isWriteOnly(staged.scope) : meta ? isWriteOnly(meta.scope as VarScope) : false;
  const isShared = meta ? !meta.service_id : false;
  const isModified = Boolean(staged);
  const isRemoved = staged?.op === "remove";

  // Draft value: staged value if present, else empty (server never returns
  // plaintext). Sealed rows keep the write-only affordance until edited.
  const [draft, setDraft] = useState<string>(staged?.value ?? "");
  const scope: VarScope = staged?.scope ?? (meta?.scope as VarScope) ?? "plain";

  // The row is keyed by variable name and survives a successful refetch. Keep
  // its controlled input aligned with the tab-local input buffer so plaintext
  // is removed from the DOM as soon as the server acknowledges the saved draft.
  useEffect(() => {
    setDraft(staged?.value ?? "");
  }, [staged?.op, staged?.value]);

  function baseline(): StagedVariableChange["base"] {
    if (!meta) return undefined;
    return {
      id: meta.id,
      workspaceId: meta.workspace_id,
      projectId: meta.project_id,
      environmentId: meta.environment_id,
      serviceId: meta.service_id ?? "",
      scope: meta.scope as VarScope,
      version: meta.version,
      lengthBucket: meta.length_bucket,
      sealed: isWriteOnly(meta.scope as VarScope),
    };
  }

  function stageValue(value: string, nextScope: VarScope) {
    // Reverting an existing row to "no change" discards the stage.
    if (meta && value === "" && nextScope === (meta.scope as VarScope)) {
      discardChange(serviceId, environmentId, row.name);
      return;
    }
    stageChange({
      serviceId,
      environmentId: environmentId ?? "",
      name: row.name,
      op: meta ? "edit" : "add",
      scope: nextScope,
      value,
      base: baseline(),
    });
  }

  function stageRemove() {
    if (!meta) {
      // Discarding a never-committed add just drops it.
      discardChange(serviceId, environmentId, row.name);
      return;
    }
    stageChange({
      serviceId,
      environmentId: environmentId ?? "",
      name: row.name,
      op: "remove",
      scope: meta.scope as VarScope,
      value: "",
      base: baseline(),
    });
  }

  return (
    <tr
      className={
        "border-t border-line-soft align-top text-text " +
        (isModified ? "bg-staged-soft" : "")
      }
    >
      <td className="px-3 py-2">
        <div className="flex items-center gap-1.5">
          {isModified ? (
            <span
              aria-hidden
              className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-staged"
              title="Staged change"
            />
          ) : null}
          <span
            className={"font-mono " + (isRemoved ? "text-subtle line-through" : "")}
          >
            {row.name}
          </span>
          {isShared ? (
            <span className="rounded border border-info/40 bg-info-soft px-1 py-px text-[9px] uppercase tracking-wider text-info">
              shared
            </span>
          ) : (
            <span className="rounded border border-line px-1 py-px text-[9px] uppercase tracking-wider text-subtle">
              service
            </span>
          )}
        </div>
      </td>
      <td className="px-3 py-2">
        {isRemoved ? (
          <span className="font-mono text-subtle">— will be removed —</span>
        ) : (
          <ReferenceInput
            ariaLabel={`Value for ${row.name}`}
            value={draft}
            refContext={refContext}
            placeholder={
              meta
                ? sealed
                  ? "•••• (sealed — enter to replace)"
                  : "•••• (enter to change)"
                : "value or ${{ ref }}"
            }
            onChange={(v) => {
              setDraft(v);
              stageValue(v, scope);
            }}
          />
        )}
      </td>
      <td className="px-3 py-2">
        <select
          aria-label={`Scope for ${row.name}`}
          value={scope}
          disabled={isRemoved}
          onChange={(e) => {
            const next = e.target.value as VarScope;
            stageValue(draft, next);
          }}
          className="w-full rounded-[var(--r-sm)] border border-line bg-surface px-1.5 py-1 text-xs text-text outline-none focus:border-line-strong disabled:opacity-50"
        >
          {SCOPES.map((s) => (
            <option key={s} value={s}>
              {SCOPE_LABEL[s]}
            </option>
          ))}
        </select>
      </td>
      <td className="px-3 py-2 font-mono text-muted">{meta ? `v${meta.version}` : "—"}</td>
      <td className="px-3 py-2 font-mono text-muted" title={meta?.kek_fingerprint}>
        {meta?.length_bucket ?? "—"}
      </td>
      <td className="px-3 py-2 text-right">
        {isRemoved ? (
          <button
            type="button"
            onClick={() => discardChange(serviceId, environmentId, row.name)}
            className="text-[10px] font-semibold uppercase tracking-wider text-staged hover:underline"
          >
            Undo
          </button>
        ) : (
          <button
            type="button"
            onClick={stageRemove}
            aria-label={`Remove ${row.name}`}
            className="text-subtle transition-colors hover:text-danger"
            title="Remove"
          >
            ✕
          </button>
        )}
        {sealed && meta ? (
          <div className="mt-1 truncate font-mono text-[9px] text-subtle" title={meta.kek_fingerprint}>
            kek {meta.kek_fingerprint.slice(0, 8)}
          </div>
        ) : null}
      </td>
    </tr>
  );
}

function AddRow({
  serviceId,
  environmentId,
  existingNames,
  refContext,
  onClose,
}: {
  serviceId: string;
  environmentId?: string;
  existingNames: Set<string>;
  refContext: RefContext;
  onClose: () => void;
}) {
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [scope, setScope] = useState<VarScope>("plain");

  const trimmed = name.trim();
  const nameValid = /^[A-Za-z_][A-Za-z0-9_]*$/.test(trimmed);
  const dupe = existingNames.has(trimmed);
  const canSave = nameValid && !dupe;

  function save() {
    if (!canSave) return;
    stageChange({
      serviceId,
      environmentId: environmentId ?? "",
      name: trimmed,
      op: "add",
      scope,
      value,
    });
    onClose();
  }

  return (
    <tr className="border-t border-line-soft bg-staged-soft align-top">
      <td className="px-3 py-2">
        <ReferenceInput
          ariaLabel="New variable name"
          value={name}
          refContext={refContext}
          placeholder="VARIABLE_NAME"
          onChange={setName}
          onEnter={save}
        />
        {trimmed && !nameValid ? (
          <p className="mt-1 text-[10px] text-danger">Use letters, digits, underscores.</p>
        ) : dupe ? (
          <p className="mt-1 text-[10px] text-danger">Already exists.</p>
        ) : null}
      </td>
      <td className="px-3 py-2">
        <ReferenceInput
          ariaLabel="New variable value"
          value={value}
          refContext={refContext}
          placeholder="value or ${{ ref }}"
          onChange={setValue}
          onEnter={save}
        />
      </td>
      <td className="px-3 py-2">
        <select
          aria-label="New variable scope"
          value={scope}
          onChange={(e) => setScope(e.target.value as VarScope)}
          className="w-full rounded-[var(--r-sm)] border border-line bg-surface px-1.5 py-1 text-xs text-text outline-none focus:border-line-strong"
        >
          {SCOPES.map((s) => (
            <option key={s} value={s}>
              {SCOPE_LABEL[s]}
            </option>
          ))}
        </select>
      </td>
      <td className="px-3 py-2 font-mono text-subtle">new</td>
      <td className="px-3 py-2" />
      <td className="px-3 py-2 text-right">
        <div className="flex justify-end gap-1.5">
          <button
            type="button"
            onClick={save}
            disabled={!canSave}
            className="rounded-[var(--r-sm)] border border-accent bg-accent-soft px-2 py-1 text-[10px] font-semibold text-accent disabled:opacity-40"
          >
            Stage
          </button>
          <button
            type="button"
            onClick={onClose}
            className="rounded-[var(--r-sm)] border border-line px-2 py-1 text-[10px] text-muted hover:bg-surface-3"
          >
            Cancel
          </button>
        </div>
      </td>
    </tr>
  );
}

// ── raw (.env) ─────────────────────────────────────────────────────

function RawEditor({
  serviceId,
  environmentId,
  server,
  staged,
  onDone,
}: {
  serviceId: string;
  environmentId?: string;
  server: VariableMeta[];
  staged: StagedVariableChange[];
  onDone: () => void;
}) {
  // Seed with what we have plaintext for (staged adds/edits). Existing server
  // vars whose values we cannot read are listed as commented hints so the
  // operator knows the full key set without us fabricating masked values.
  const initial = useMemo(() => {
    const stagedRows = staged
      .filter((s) => s.op !== "remove")
      .map((s) => ({ name: s.name, value: s.value }));
    const stagedNames = new Set(stagedRows.map((r) => r.name));
    const hints = server
      .filter((v) => !stagedNames.has(v.name))
      .map((v) => `# ${v.name}=<${v.scope} · ${v.length_bucket} · unchanged>`)
      .join("\n");
    const body = serializeEnv(stagedRows);
    return [hints, body].filter(Boolean).join("\n");
  }, [server, staged]);

  const [text, setText] = useState(initial);
  const metaByName = useMemo(() => new Map(server.map((v) => [v.name, v])), [server]);

  function apply() {
    const parsed = parseEnv(text);
    for (const { name, value } of parsed) {
      const meta = metaByName.get(name);
      stageChange({
        serviceId,
        environmentId: environmentId ?? "",
        name,
        op: meta ? "edit" : "add",
        // Preserve an existing var's scope; new vars default to plain.
        scope: (meta?.scope as VarScope) ?? "plain",
        value,
        base: meta
          ? {
              scope: meta.scope as VarScope,
              id: meta.id,
              workspaceId: meta.workspace_id,
              projectId: meta.project_id,
              environmentId: meta.environment_id,
              serviceId: meta.service_id ?? "",
              version: meta.version,
              lengthBucket: meta.length_bucket,
              sealed: isWriteOnly(meta.scope as VarScope),
            }
          : undefined,
      });
    }
    onDone();
  }

  return (
    <div className="grid gap-2">
      <label className="text-[10px] uppercase tracking-wider text-subtle" htmlFor="raw-env">
        Paste or edit as .env — <code className="font-mono">KEY=value</code>, one per line.
        Supports <code className="font-mono">{"${{ Service.VAR }}"}</code> references.
      </label>
      <textarea
        id="raw-env"
        value={text}
        onChange={(e) => setText(e.target.value)}
        spellCheck={false}
        rows={12}
        className="w-full resize-y rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 font-mono text-xs text-text outline-none focus:border-line-strong focus:shadow-[var(--focus-ring)]"
        placeholder={"DATABASE_URL=postgres://…\nAPI_KEY=${{ shared.API_KEY }}"}
      />
      <div className="flex gap-1.5">
        <button
          type="button"
          onClick={apply}
          className="rounded-[var(--r-sm)] border border-accent bg-accent-soft px-3 py-1.5 text-xs font-semibold text-accent hover:bg-surface-3"
        >
          Stage changes
        </button>
        <button
          type="button"
          onClick={onDone}
          className="rounded-[var(--r-sm)] border border-line px-3 py-1.5 text-xs text-muted hover:bg-surface-3"
        >
          Cancel
        </button>
      </div>
    </div>
  );
}

// ── bits ───────────────────────────────────────────────────────────

function ToggleButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={
        "rounded-[var(--r-sm)] border px-2.5 py-1 text-xs font-semibold transition-colors " +
        (active
          ? "border-line-strong bg-surface-3 text-text"
          : "border-line text-muted hover:bg-surface-2")
      }
    >
      {children}
    </button>
  );
}
