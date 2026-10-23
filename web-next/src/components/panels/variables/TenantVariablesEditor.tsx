import { useId, useState, type FormEvent } from "react";
import { useService } from "@/api/hooks";
import { CapabilityBoundary } from "@/components/CapabilityBoundary";
import { ErrorState, Loading } from "@/components/states";
import { Button } from "@/components/panels/form-kit";
import { stageChange } from "@/lib/stagedChanges";
import { useProjectAccess } from "@/components/panels/automation-tokens/api";
import {
  isValidTenantVariableCreateName,
  TENANT_VARIABLE_SCOPES,
  TENANT_VARIABLE_NAME_MAX_BYTES,
  useTenantVariables,
  type TenantVariableMeta,
  type TenantVariableScope,
} from "./tenant-api";

const VARIABLE_READ_PERMISSION = "variables.read";
const VARIABLE_WRITE_PERMISSION = "variables.write";

type ReplacementDraft = {
  baseline: TenantVariableMeta;
  scope: TenantVariableScope;
  value: string;
  revealed: boolean;
};

type AddDraft = {
  name: string;
  scope: TenantVariableScope;
  value: string;
  revealed: boolean;
};

const EMPTY_ADD_DRAFT: AddDraft = {
  name: "",
  scope: "secret",
  value: "",
  revealed: false,
};

export function TenantVariablesEditor({
  projectId,
  serviceId,
  environmentId,
}: {
  projectId: string;
  serviceId: string;
  environmentId: string;
}) {
  const targetKey = `${projectId}\u0000${serviceId}\u0000${environmentId}`;
  return (
    <ScopedTenantVariablesEditor
      key={targetKey}
      projectId={projectId}
      serviceId={serviceId}
      environmentId={environmentId}
    />
  );
}

function ScopedTenantVariablesEditor({
  projectId,
  serviceId,
  environmentId,
}: {
  projectId: string;
  serviceId: string;
  environmentId: string;
}) {
  const service = useService(serviceId);
  const exactService =
    service.data?.id === serviceId &&
    service.data.project_id === projectId;
  const validTarget = Boolean(projectId && serviceId && environmentId);
  const access = useProjectAccess(
    projectId,
    environmentId,
    validTarget && exactService,
  );
  const exactAccess =
    access.data?.principal_kind === "human" &&
    access.data.project_id === projectId &&
    access.data.environment_id === environmentId;
  const canRead =
    exactService &&
    exactAccess &&
    !access.isPending &&
    !access.error &&
    access.data?.permissions.includes(VARIABLE_READ_PERMISSION) === true;
  const canWrite =
    canRead &&
    access.data?.permissions.includes(VARIABLE_WRITE_PERMISSION) === true;
  const target = { projectId, serviceId, environmentId };
  const variables = useTenantVariables(target, canRead);

  const [adding, setAdding] = useState(false);
  const [addDraft, setAddDraft] = useState<AddDraft>(EMPTY_ADD_DRAFT);
  const [replacement, setReplacement] = useState<ReplacementDraft>();
  const [removeCandidate, setRemoveCandidate] =
    useState<TenantVariableMeta>();
  const [notice, setNotice] = useState<string>();
  function resetMutationState() {
    setNotice(undefined);
  }

  function submitAdd(event: FormEvent) {
    event.preventDefault();
    if (!canWrite) return;
    const name = addDraft.name;
    if (
      !isValidTenantVariableCreateName(name) ||
      addDraft.value.length === 0
    ) {
      return;
    }
    stageChange({
      serviceId,
      environmentId,
      name,
      op: "add",
      scope: addDraft.scope,
      value: addDraft.value,
    });
    setAddDraft(EMPTY_ADD_DRAFT);
    setAdding(false);
    setNotice(
      `${name} is ready in Changes. Save the encrypted draft for review, then apply and deploy it.`,
    );
  }

  function submitReplacement(event: FormEvent) {
    event.preventDefault();
    if (!canWrite || !replacement || replacement.value.length === 0) {
      return;
    }
    stageChange({
      serviceId,
      environmentId,
      name: replacement.baseline.name,
      op: "edit",
      scope: replacement.scope,
      value: replacement.value,
      base: stagedBaseline(replacement.baseline),
    });
    const name = replacement.baseline.name;
    setReplacement(undefined);
    setNotice(
      `${name} replacement is ready in Changes. Save the encrypted draft for review, then apply and deploy it.`,
    );
  }

  function confirmRemove() {
    if (!canWrite || !removeCandidate) return;
    stageChange({
      serviceId,
      environmentId,
      name: removeCandidate.name,
      op: "remove",
      scope: removeCandidate.scope,
      value: "",
      base: stagedBaseline(removeCandidate),
    });
    const removedName = removeCandidate.name;
    setRemoveCandidate(undefined);
    setNotice(
      `${removedName} removal is ready in Changes. Save the draft for review, then apply and deploy it.`,
    );
  }

  if (!validTarget) {
    return (
      <CapabilityBoundary
        state="unavailable"
        title="Service variables"
        detail="A concrete project, service, and environment are required. No variable request was sent."
      />
    );
  }
  if (service.isPending) {
    return <Loading label="Checking exact service scope…" />;
  }
  if (service.error) {
    return (
      <ErrorState
        title="Failed to verify service scope"
        error={service.error}
        onRetry={() => void service.refetch()}
      />
    );
  }
  if (!exactService) {
    return (
      <CapabilityBoundary
        state="unavailable"
        title="Service variables"
        detail="Midnight could not verify that this service belongs to the selected project. No variable request was sent."
      />
    );
  }
  if (access.isPending) {
    return <Loading label="Checking exact variable access…" />;
  }
  if (access.error || !exactAccess) {
    return (
      <ErrorState
        title="Variable access unavailable"
        error={
          access.error ??
          new Error(
            "Midnight could not verify the exact project and environment scope.",
          )
        }
        onRetry={() => void access.refetch()}
      />
    );
  }
  if (!canRead) {
    return (
      <CapabilityBoundary
        state="unavailable"
        title="Service variables"
        detail="The current role does not grant variables-read permission for this exact environment. No variable request was sent."
      />
    );
  }
  if (variables.isPending) {
    return <Loading label="Loading service variable metadata…" />;
  }
  if (variables.error) {
    return (
      <ErrorState
        title="Failed to load service variables"
        error={variables.error}
        onRetry={() => void variables.refetch()}
      />
    );
  }

  const existingNames = new Set(
    (variables.data ?? []).map((variable) => variable.name),
  );
  const normalizedAddName = addDraft.name;
  const addNameValid =
    normalizedAddName.length === 0 ||
    isValidTenantVariableCreateName(normalizedAddName);
  const addNameDuplicate = existingNames.has(normalizedAddName);

  return (
    <section
      className="grid gap-4"
      aria-label="Service environment variables"
    >
      <div
        role="status"
        className="rounded-[var(--r-sm)] border border-info/40 bg-info-soft px-3 py-2 text-xs text-muted"
      >
        Values are encrypted and never returned by this surface. Edits remain
        in this tab until Changes saves one encrypted environment draft.
        Apply &amp; deploy commits the exact reviewed revision and queues affected
        services. Applying without a deployment is available as an advanced action.
      </div>

      {!canWrite ? (
        <p
          role="note"
          className="m-0 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2 text-xs text-muted"
        >
          This role can inspect variable metadata but cannot add, replace, or
          remove values without variables-write permission.
        </p>
      ) : null}

      {notice ? (
        <p
          role="status"
          aria-live="polite"
          className="m-0 rounded-[var(--r-sm)] border border-success/40 bg-success-soft px-3 py-2 text-xs text-muted"
        >
          {notice}
        </p>
      ) : null}

      <div className="grid gap-2">
        {(variables.data ?? []).length === 0 ? (
          <div className="rounded-lg border border-dashed border-line bg-bg-2 px-4 py-6 text-center text-sm text-muted">
            <strong className="block text-text">
              No service-owned variables
            </strong>
            <span>
              Shared variables are managed separately and are not exposed as
              editable service rows here.
            </span>
          </div>
        ) : (
          <ul className="grid gap-2">
            {(variables.data ?? []).map((variable) => (
              <li
                key={variable.id}
                className="grid gap-3 rounded-lg border border-line bg-surface-2 px-3 py-3"
              >
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-0">
                    <strong className="block truncate font-mono text-sm text-text">
                      {variable.name}
                    </strong>
                    <span className="text-[11px] text-muted">
                      {scopeLabel(variable.scope)} · metadata v
                      {variable.version} · {variable.length_bucket}
                    </span>
                  </div>
                  {canWrite ? (
                    <div className="flex flex-wrap gap-1.5">
                      <Button
                        variant="secondary"
                        onClick={() => {
                          resetMutationState();
                          setRemoveCandidate(undefined);
                          setReplacement({
                            baseline: variable,
                            scope: variable.scope,
                            value: "",
                            revealed: false,
                          });
                        }}
                      >
                        Replace {variable.name}
                      </Button>
                      <Button
                        variant="danger"
                        onClick={() => {
                          resetMutationState();
                          setReplacement(undefined);
                          setRemoveCandidate(variable);
                        }}
                      >
                        Remove {variable.name}
                      </Button>
                    </div>
                  ) : null}
                </div>

                {replacement?.baseline.id === variable.id ? (
                  <ReplacementForm
                    draft={replacement}
                    onChange={(next) => {
                      setNotice(undefined);
                      setReplacement(next);
                    }}
                    onSubmit={submitReplacement}
                    onCancel={() => {
                      resetMutationState();
                      setReplacement(undefined);
                    }}
                  />
                ) : null}

                {removeCandidate?.id === variable.id ? (
                  <div
                    role="alert"
                    aria-label={`Remove ${variable.name}`}
                    className="grid gap-2 rounded-[var(--r-sm)] border border-danger/40 bg-danger-soft px-3 py-2"
                  >
                    <p className="m-0 text-xs text-muted">
                      Remove <strong>{variable.name}</strong> from desired state
                      for future deployments? Existing containers are not
                      changed.
                    </p>
                    <div className="flex flex-wrap gap-1.5">
                      <Button
                        variant="danger"
                        onClick={confirmRemove}
                      >
                        Confirm remove {variable.name}
                      </Button>
                      <Button
                        variant="secondary"
                        onClick={() => {
                          resetMutationState();
                          setRemoveCandidate(undefined);
                        }}
                      >
                        Cancel removal
                      </Button>
                    </div>
                  </div>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </div>

      {canWrite && !adding ? (
        <div>
          <Button
            onClick={() => {
              resetMutationState();
              setReplacement(undefined);
              setRemoveCandidate(undefined);
              setAdding(true);
            }}
          >
            + Add variable
          </Button>
        </div>
      ) : null}

      {canWrite && adding ? (
        <AddVariableForm
          draft={addDraft}
          nameValid={addNameValid}
          duplicate={addNameDuplicate}
          onChange={(next) => {
            setNotice(undefined);
            setAddDraft(next);
          }}
          onSubmit={submitAdd}
          onCancel={() => {
            resetMutationState();
            setAddDraft(EMPTY_ADD_DRAFT);
            setAdding(false);
          }}
        />
      ) : null}
    </section>
  );
}

function AddVariableForm({
  draft,
  nameValid,
  duplicate,
  onChange,
  onSubmit,
  onCancel,
}: {
  draft: AddDraft;
  nameValid: boolean;
  duplicate: boolean;
  onChange: (draft: AddDraft) => void;
  onSubmit: (event: FormEvent) => void;
  onCancel: () => void;
}) {
  const formId = useId();
  const nameHintId = `${formId}-name-hint`;
  return (
    <form
      aria-label="Add service variable"
      className="grid gap-3 rounded-lg border border-accent/40 bg-surface-2 px-3 py-3"
      onSubmit={onSubmit}
    >
      <div className="grid gap-1.5">
        <label
          htmlFor={`${formId}-name`}
          className="text-xs font-semibold text-text"
        >
          Variable name
        </label>
        <input
          id={`${formId}-name`}
          value={draft.name}
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          aria-invalid={!nameValid || duplicate}
          aria-describedby={nameHintId}
          onChange={(event) =>
            onChange({ ...draft, name: event.currentTarget.value })
          }
          className="w-full rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 font-mono text-sm text-text outline-none focus:border-accent"
        />
        <span
          id={nameHintId}
          className={
            "text-[11px] " +
            (!nameValid || duplicate ? "text-danger" : "text-muted")
          }
        >
          {!nameValid
            ? `Use letters, numbers, and underscores; begin with a letter or underscore and use no more than ${TENANT_VARIABLE_NAME_MAX_BYTES} bytes.`
            : duplicate
              ? "A service-owned variable with this name already exists."
              : `Use a portable environment-variable name, up to ${TENANT_VARIABLE_NAME_MAX_BYTES} bytes.`}
        </span>
      </div>
      <ScopeField
        id={`${formId}-scope`}
        value={draft.scope}
        onChange={(scope) =>
          onChange({
            ...draft,
            scope,
            revealed: scope === "plain" ? true : draft.revealed,
          })
        }
      />
      <ValueField
        id={`${formId}-value`}
        label="Variable value"
        value={draft.value}
        scope={draft.scope}
        revealed={draft.revealed}
        onReveal={(revealed) => onChange({ ...draft, revealed })}
        onChange={(value) => onChange({ ...draft, value })}
      />
      <div className="flex flex-wrap gap-1.5">
        <Button
          type="submit"
          disabled={
            draft.name.length === 0 ||
            !nameValid ||
            duplicate ||
            draft.value.length === 0
          }
        >
          Save variable
        </Button>
        <Button variant="secondary" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

function ReplacementForm({
  draft,
  onChange,
  onSubmit,
  onCancel,
}: {
  draft: ReplacementDraft;
  onChange: (draft: ReplacementDraft) => void;
  onSubmit: (event: FormEvent) => void;
  onCancel: () => void;
}) {
  const formId = useId();
  return (
    <form
      aria-label={`Replace ${draft.baseline.name}`}
      className="grid gap-3 border-t border-line pt-3"
      onSubmit={onSubmit}
    >
      <p className="m-0 text-xs text-muted">
        The stored value cannot be loaded. Enter a complete replacement for
        metadata version {draft.baseline.version}.
      </p>
      <ScopeField
        id={`${formId}-scope`}
        value={draft.scope}
        sealedOnly={draft.baseline.scope === "sealed"}
        onChange={(scope) =>
          onChange({
            ...draft,
            scope,
            revealed: scope === "plain" ? true : draft.revealed,
          })
        }
      />
      <ValueField
        id={`${formId}-value`}
        label={`Replacement value for ${draft.baseline.name}`}
        value={draft.value}
        scope={draft.scope}
        revealed={draft.revealed}
        onReveal={(revealed) => onChange({ ...draft, revealed })}
        onChange={(value) => onChange({ ...draft, value })}
      />
      <div className="flex flex-wrap gap-1.5">
        <Button type="submit" disabled={draft.value.length === 0}>
          Save replacement for {draft.baseline.name}
        </Button>
        <Button variant="secondary" onClick={onCancel}>
          Cancel replacement
        </Button>
      </div>
    </form>
  );
}

function ScopeField({
  id,
  value,
  sealedOnly = false,
  disabled = false,
  onChange,
}: {
  id: string;
  value: TenantVariableScope;
  sealedOnly?: boolean;
  disabled?: boolean;
  onChange: (scope: TenantVariableScope) => void;
}) {
  const hintId = `${id}-hint`;
  return (
    <div className="grid gap-1.5">
      <label htmlFor={id} className="text-xs font-semibold text-text">
        Value handling
      </label>
      <select
        id={id}
        value={value}
        disabled={disabled}
        aria-describedby={hintId}
        onChange={(event) =>
          onChange(event.currentTarget.value as TenantVariableScope)
        }
        className="w-full rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 text-sm text-text outline-none focus:border-accent"
      >
        {TENANT_VARIABLE_SCOPES.filter(
          (scope) => !sealedOnly || scope === "sealed",
        ).map((scope) => (
          <option key={scope} value={scope}>
            {scopeLabel(scope)}
          </option>
        ))}
      </select>
      <span id={hintId} className="text-[11px] text-muted">
        {sealedOnly
          ? "Sealed value handling cannot be downgraded. The replacement remains sealed and masked by default."
          : "All values are write-only in this surface. Secret and sealed drafts are masked by default."}
      </span>
    </div>
  );
}

function ValueField({
  id,
  label,
  value,
  scope,
  revealed,
  disabled = false,
  onReveal,
  onChange,
}: {
  id: string;
  label: string;
  value: string;
  scope: TenantVariableScope;
  revealed: boolean;
  disabled?: boolean;
  onReveal: (revealed: boolean) => void;
  onChange: (value: string) => void;
}) {
  const hintId = `${id}-hint`;
  const sensitive = scope === "secret" || scope === "sealed";
  return (
    <div className="grid gap-1.5">
      <div className="flex items-center justify-between gap-2">
        <label htmlFor={id} className="text-xs font-semibold text-text">
          {label}
        </label>
        {sensitive ? (
          <button
            type="button"
            aria-pressed={revealed}
            disabled={disabled}
            onClick={() => onReveal(!revealed)}
            className="text-[11px] font-semibold text-muted hover:text-text"
          >
            {revealed ? "Hide value" : "Reveal value"}
          </button>
        ) : null}
      </div>
      <input
        id={id}
        type={sensitive && !revealed ? "password" : "text"}
        value={value}
        disabled={disabled}
        autoComplete="new-password"
        spellCheck={false}
        aria-describedby={hintId}
        onChange={(event) => onChange(event.currentTarget.value)}
        className="w-full rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 font-mono text-sm text-text outline-none focus:border-accent"
      />
      <span id={hintId} className="text-[11px] text-muted">
        A non-empty complete replacement is required. Midnight never returns
        the previous value.
      </span>
    </div>
  );
}

function scopeLabel(scope: TenantVariableScope): string {
  switch (scope) {
    case "plain":
      return "Plain";
    case "secret":
      return "Secret";
    case "sealed":
      return "Sealed";
  }
}

function stagedBaseline(variable: TenantVariableMeta) {
  return {
    id: variable.id,
    workspaceId: variable.workspace_id,
    projectId: variable.project_id,
    environmentId: variable.environment_id,
    serviceId: variable.service_id,
    scope: variable.scope,
    version: variable.version,
    lengthBucket: variable.length_bucket,
    sealed: variable.scope === "sealed",
  };
}
