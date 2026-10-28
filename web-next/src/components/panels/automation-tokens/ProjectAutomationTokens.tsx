import * as Dialog from "@radix-ui/react-dialog";
import { useEffect, useId, useMemo, useState, type FormEvent } from "react";
import { ApiError } from "@/api/client";
import { useAuth } from "@/auth/AuthProvider";
import { Empty, Loading } from "@/components/states";
import {
  Button,
  Card,
  Field,
  InlineError,
  TextInput,
} from "@/components/panels/form-kit";
import {
  type CreateProjectAutomationTokenResult,
  type ProjectAutomationToken,
  useCreateProjectAutomationToken,
  useProjectAccess,
  useProjectAutomationTokens,
  useRevokeProjectAutomationToken,
} from "./api";

const MANAGE_PROJECT_PERMISSION = "project.manage";
const RELATIVE_EXPIRY_OPTIONS = [
  { days: 1, label: "1 day" },
  { days: 7, label: "7 days" },
  { days: 30, label: "30 days" },
  { days: 90, label: "90 days" },
] as const;

export function ProjectAutomationTokens({
  projectId,
}: {
  projectId: string;
}) {
  const auth = useAuth();
  const isHuman = auth.state.status === "human";
  const access = useProjectAccess(projectId, undefined, isHuman);

  if (!isHuman) return null;

  if (access.isPending) {
    return (
      <Card
        title="Automation tokens"
        description="Project-scoped credentials for CI deploy workflows."
      >
        <Loading label="Checking project access…" />
      </Card>
    );
  }

  if (access.error) {
    const forbidden =
      access.error instanceof ApiError && access.error.status === 403;
    return (
      <Card
        title="Automation tokens"
        description="Project-scoped credentials for CI deploy workflows."
      >
        <CapabilityNotice
          title={
            forbidden
              ? "Project manager access required"
              : "Automation tokens unavailable"
          }
          detail={
            forbidden
              ? "Your account cannot manage credentials for this project."
              : "Midnight could not verify your project permissions. No token controls are available until that check succeeds."
          }
          tone={forbidden ? "forbidden" : "unavailable"}
          onRetry={forbidden ? undefined : () => void access.refetch()}
        />
      </Card>
    );
  }

  if (!access.data?.permissions.includes(MANAGE_PROJECT_PERMISSION)) {
    return (
      <Card
        title="Automation tokens"
        description="Project-scoped credentials for CI deploy workflows."
      >
        <CapabilityNotice
          title="Project manager access required"
          detail="Your account can view this project, but it cannot create or revoke project automation tokens."
          tone="forbidden"
        />
      </Card>
    );
  }

  return <TokenManager projectId={projectId} />;
}

function TokenManager({ projectId }: { projectId: string }) {
  const tokens = useProjectAutomationTokens(projectId, true);
  const createToken = useCreateProjectAutomationToken(projectId);
  const revokeToken = useRevokeProjectAutomationToken(projectId);
  const [createOpen, setCreateOpen] = useState(false);
  const [reveal, setReveal] =
    useState<CreateProjectAutomationTokenResult | null>(null);
  const [revokeTarget, setRevokeTarget] =
    useState<ProjectAutomationToken | null>(null);

  const activeTokens = useMemo(
    () =>
      [...(tokens.data?.items ?? [])].sort((left, right) =>
        right.created_at.localeCompare(left.created_at),
      ),
    [tokens.data?.items],
  );
  const controlsAvailable = !tokens.isPending && !tokens.error;

  return (
    <Card
      title="Automation tokens"
      description="Project-scoped credentials for CI deploy workflows. Tokens use the restricted ci-deploy-v1 profile."
      action={
        controlsAvailable ? (
          <Button onClick={() => setCreateOpen(true)}>+ New token</Button>
        ) : undefined
      }
    >
      {tokens.isPending ? (
        <Loading label="Loading automation tokens…" />
      ) : tokens.error ? (
        <CapabilityNotice
          title={
            tokens.error instanceof ApiError && tokens.error.status === 403
              ? "Project manager access required"
              : "Automation tokens unavailable"
          }
          detail={
            tokens.error instanceof ApiError && tokens.error.status === 403
              ? "Your permission changed while this page was open. Token controls are disabled."
              : "Midnight could not load token metadata. Existing credentials are unaffected."
          }
          tone={
            tokens.error instanceof ApiError && tokens.error.status === 403
              ? "forbidden"
              : "unavailable"
          }
          onRetry={() => void tokens.refetch()}
        />
      ) : activeTokens.length === 0 ? (
        <Empty
          title="No automation tokens"
          detail="Create a short-lived credential for a CI deploy workflow."
          icon={<span aria-hidden>⌁</span>}
          action={
            <Button onClick={() => setCreateOpen(true)}>
              Create automation token
            </Button>
          }
        />
      ) : (
        <ul className="m-0 grid list-none gap-2 p-0">
          {activeTokens.map((token) => {
            const status = tokenStatus(token);
            return (
              <li
                key={token.id}
                className="grid gap-3 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center"
              >
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <strong className="truncate text-sm text-text">
                      {token.name}
                    </strong>
                    <StatusPill status={status} />
                  </div>
                  <p className="m-0 mt-1 break-all font-mono text-[11px] text-subtle">
                    {token.token_prefix}
                  </p>
                  <dl className="m-0 mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-muted">
                    <div className="flex gap-1">
                      <dt>Created</dt>
                      <dd className="m-0 text-text">
                        <time dateTime={token.created_at}>
                          {formatDate(token.created_at)}
                        </time>
                      </dd>
                    </div>
                    <div className="flex gap-1">
                      <dt>Expires</dt>
                      <dd className="m-0 text-text">
                        <time dateTime={token.expires_at}>
                          {formatDate(token.expires_at)}
                        </time>
                      </dd>
                    </div>
                    <div className="flex gap-1">
                      <dt>Last used</dt>
                      <dd className="m-0 text-text">
                        {token.last_used_at ? (
                          <time dateTime={token.last_used_at}>
                            {formatDate(token.last_used_at)}
                          </time>
                        ) : (
                          "Never"
                        )}
                      </dd>
                    </div>
                  </dl>
                </div>
                {status === "active" ? (
                  <Button
                    variant="danger"
                    className="w-full sm:w-auto"
                    onClick={() => setRevokeTarget(token)}
                  >
                    Revoke
                  </Button>
                ) : null}
              </li>
            );
          })}
        </ul>
      )}

      <CreateTokenDialog
        open={createOpen}
        onOpenChange={(open) => {
          setCreateOpen(open);
          if (!open) {
            setReveal(null);
            createToken.reset();
          }
        }}
        reveal={reveal}
        busy={createToken.isPending}
        error={createToken.error}
        onCreate={async (name, expiresAt) => {
          const result = await createToken.mutateAsync({ name, expiresAt });
          setReveal(result);
          createToken.reset();
        }}
      />

      <RevokeTokenDialog
        token={revokeTarget}
        busy={revokeToken.isPending}
        error={revokeToken.error}
        onOpenChange={(open) => {
          if (!open) {
            setRevokeTarget(null);
            revokeToken.reset();
          }
        }}
        onConfirm={async () => {
          if (!revokeTarget) return;
          await revokeToken.mutateAsync({ tokenId: revokeTarget.id, expectedVersion: revokeTarget.resource_version });
          setRevokeTarget(null);
          revokeToken.reset();
        }}
      />
    </Card>
  );
}

function CreateTokenDialog({
  open,
  reveal,
  busy,
  error,
  onOpenChange,
  onCreate,
}: {
  open: boolean;
  reveal: CreateProjectAutomationTokenResult | null;
  busy: boolean;
  error: Error | null;
  onOpenChange: (open: boolean) => void;
  onCreate: (name: string, expiresAt: string) => Promise<void>;
}) {
  const formId = useId();
  const [name, setName] = useState("");
  const [expiryMode, setExpiryMode] = useState<"relative" | "absolute">(
    "relative",
  );
  const [relativeDays, setRelativeDays] = useState("30");
  const [absoluteExpiry, setAbsoluteExpiry] = useState(() =>
    defaultAbsoluteExpiry(),
  );
  const [validationError, setValidationError] = useState<string>();
  const [copyState, setCopyState] = useState<"idle" | "copied" | "error">(
    "idle",
  );

  useEffect(() => {
    if (open) return;
    setName("");
    setExpiryMode("relative");
    setRelativeDays("30");
    setAbsoluteExpiry(defaultAbsoluteExpiry());
    setValidationError(undefined);
    setCopyState("idle");
  }, [open]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setValidationError(undefined);
    const trimmedName = name.trim();
    if (!trimmedName) {
      setValidationError("A token name is required.");
      return;
    }
    const expiry =
      expiryMode === "relative"
        ? new Date(
            Date.now() + Number(relativeDays) * 24 * 60 * 60 * 1000,
          )
        : new Date(absoluteExpiry);
    if (!Number.isFinite(expiry.getTime()) || expiry.getTime() <= Date.now()) {
      setValidationError("Choose an expiry time in the future.");
      return;
    }
    try {
      await onCreate(trimmedName, expiry.toISOString());
    } catch {
      // The mutation owns and renders the bounded API error.
    }
  }

  async function copySecret() {
    if (!reveal) return;
    try {
      await navigator.clipboard.writeText(reveal.access_token);
      setCopyState("copied");
    } catch {
      setCopyState("error");
    }
  }

  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/70 backdrop-blur-[2px]" />
        <Dialog.Content className="fixed left-1/2 top-1/2 z-50 grid max-h-[calc(100dvh-2rem)] w-[calc(100vw-2rem)] max-w-lg -translate-x-1/2 -translate-y-1/2 gap-4 overflow-y-auto rounded-lg border border-line-strong bg-surface p-4 shadow-2xl sm:p-5">
          <header className="grid gap-1 pr-8">
            <Dialog.Title className="m-0 text-base font-semibold text-text">
              {reveal ? "Copy automation token" : "New automation token"}
            </Dialog.Title>
            <Dialog.Description className="m-0 text-xs leading-5 text-muted">
              {reveal
                ? "This secret is available only in this dialog."
                : "Create a ci-deploy-v1 credential for this project. It can deploy, follow deployment progress, and read logs for this project; it cannot change project settings."}
            </Dialog.Description>
          </header>
          <Dialog.Close
            className="absolute right-3 top-3 grid h-9 w-9 place-items-center rounded-[var(--r-sm)] border border-line bg-surface-2 text-lg text-muted hover:bg-surface-3 hover:text-text"
            aria-label="Close"
          >
            ×
          </Dialog.Close>

          {reveal ? (
            <div className="grid gap-4">
              <div
                role="alert"
                className="rounded-[var(--r-sm)] border border-warn/50 bg-warn-soft px-3 py-3 text-xs text-text"
              >
                <strong className="block text-warn">
                  Copy it now. Midnight will never show it again.
                </strong>
                Store it in your CI provider&apos;s secret vault. Do not commit
                it to source control.
              </div>
              <div className="grid gap-2">
                <label
                  htmlFor={`${formId}-secret`}
                  className="text-xs font-semibold text-text"
                >
                  Access token
                </label>
                <textarea
                  id={`${formId}-secret`}
                  readOnly
                  rows={4}
                  spellCheck={false}
                  value={reveal.access_token}
                  className="w-full resize-none break-all rounded-[var(--r-sm)] border border-line bg-bg-2 px-3 py-2 font-mono text-xs text-text focus:border-accent focus:outline-none"
                  onFocus={(event) => event.currentTarget.select()}
                />
                <div className="flex flex-wrap items-center gap-2">
                  <Button onClick={() => void copySecret()}>
                    {copyState === "copied" ? "Copied" : "Copy token"}
                  </Button>
                  <span
                    aria-live="polite"
                    className={
                      copyState === "error"
                        ? "text-xs text-danger"
                        : "text-xs text-muted"
                    }
                  >
                    {copyState === "copied"
                      ? "Copied to clipboard."
                      : copyState === "error"
                        ? "Clipboard access failed. Select and copy the token manually."
                        : ""}
                  </span>
                </div>
              </div>
              <Button
                variant="secondary"
                className="w-full"
                onClick={() => onOpenChange(false)}
              >
                I have saved this token
              </Button>
            </div>
          ) : (
            <form className="grid gap-4" onSubmit={(event) => void submit(event)}>
              <Field label="Token name" htmlFor={`${formId}-name`}>
                <TextInput
                  id={`${formId}-name`}
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  placeholder="Production deploy workflow"
                  autoComplete="off"
                  maxLength={128}
                  autoFocus
                  required
                />
              </Field>

              <fieldset className="grid gap-2 border-0 p-0">
                <legend className="text-xs font-semibold text-text">
                  Expiry
                </legend>
                <div className="grid grid-cols-2 gap-2">
                  <label className="flex min-h-10 items-center gap-2 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2 text-xs text-text">
                    <input
                      type="radio"
                      name={`${formId}-expiry-mode`}
                      checked={expiryMode === "relative"}
                      onChange={() => setExpiryMode("relative")}
                    />
                    Relative
                  </label>
                  <label className="flex min-h-10 items-center gap-2 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2 text-xs text-text">
                    <input
                      type="radio"
                      name={`${formId}-expiry-mode`}
                      checked={expiryMode === "absolute"}
                      onChange={() => setExpiryMode("absolute")}
                    />
                    Date and time
                  </label>
                </div>
              </fieldset>

              {expiryMode === "relative" ? (
                <Field
                  label="Lifetime"
                  htmlFor={`${formId}-relative-expiry`}
                  hint="Short-lived tokens reduce the impact of an accidental disclosure."
                >
                  <select
                    id={`${formId}-relative-expiry`}
                    value={relativeDays}
                    onChange={(event) => setRelativeDays(event.target.value)}
                    className="w-full rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 text-sm text-text focus:border-accent focus:outline-none"
                  >
                    {RELATIVE_EXPIRY_OPTIONS.map((option) => (
                      <option key={option.days} value={option.days}>
                        {option.label}
                      </option>
                    ))}
                  </select>
                </Field>
              ) : (
                <Field
                  label="Expires at"
                  htmlFor={`${formId}-absolute-expiry`}
                  hint="Uses your browser's local time zone."
                >
                  <TextInput
                    id={`${formId}-absolute-expiry`}
                    type="datetime-local"
                    value={absoluteExpiry}
                    min={minimumAbsoluteExpiry()}
                    onChange={(event) => setAbsoluteExpiry(event.target.value)}
                    required
                  />
                </Field>
              )}

              {validationError ? <InlineError error={validationError} /> : null}
              {error ? <InlineError error={error} /> : null}

              <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
                <Button
                  variant="secondary"
                  className="w-full sm:w-auto"
                  disabled={busy}
                  onClick={() => onOpenChange(false)}
                >
                  Cancel
                </Button>
                <Button
                  type="submit"
                  className="w-full sm:w-auto"
                  disabled={busy}
                >
                  {busy ? "Creating…" : "Create token"}
                </Button>
              </div>
            </form>
          )}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function RevokeTokenDialog({
  token,
  busy,
  error,
  onOpenChange,
  onConfirm,
}: {
  token: ProjectAutomationToken | null;
  busy: boolean;
  error: Error | null;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => Promise<void>;
}) {
  return (
    <Dialog.Root open={Boolean(token)} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/70 backdrop-blur-[2px]" />
        <Dialog.Content className="fixed left-1/2 top-1/2 z-50 grid w-[calc(100vw-2rem)] max-w-md -translate-x-1/2 -translate-y-1/2 gap-4 rounded-lg border border-line-strong bg-surface p-4 shadow-2xl sm:p-5">
          <Dialog.Title className="m-0 text-base font-semibold text-text">
            Revoke automation token?
          </Dialog.Title>
          <Dialog.Description className="m-0 text-xs leading-5 text-muted">
            <strong className="text-text">{token?.name}</strong> will stop
            authenticating immediately. Running deployments are not cancelled.
            This action cannot be undone.
          </Dialog.Description>
          {error ? <InlineError error={error} /> : null}
          <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
            <Button
              variant="secondary"
              className="w-full sm:w-auto"
              disabled={busy}
              onClick={() => onOpenChange(false)}
            >
              Keep token
            </Button>
            <Button
              variant="danger"
              className="w-full sm:w-auto"
              disabled={busy}
              onClick={() => void onConfirm().catch(() => undefined)}
            >
              {busy ? "Revoking…" : "Revoke token"}
            </Button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function CapabilityNotice({
  title,
  detail,
  tone,
  onRetry,
}: {
  title: string;
  detail: string;
  tone: "forbidden" | "unavailable";
  onRetry?: () => void;
}) {
  return (
    <div
      role={tone === "unavailable" ? "alert" : "status"}
      className={
        tone === "forbidden"
          ? "grid gap-2 rounded-[var(--r-sm)] border border-line bg-surface-2 px-4 py-4 text-sm"
          : "grid gap-2 rounded-[var(--r-sm)] border border-warn/40 bg-warn-soft px-4 py-4 text-sm"
      }
    >
      <strong className={tone === "forbidden" ? "text-text" : "text-warn"}>
        {title}
      </strong>
      <span className="text-xs leading-5 text-muted">{detail}</span>
      {onRetry ? (
        <div>
          <Button variant="secondary" onClick={onRetry}>
            Retry
          </Button>
        </div>
      ) : null}
    </div>
  );
}

function StatusPill({
  status,
}: {
  status: "active" | "expired" | "revoked";
}) {
  const tone =
    status === "active"
      ? "border-accent/50 bg-accent-soft text-accent"
      : status === "expired"
        ? "border-warn/50 bg-warn-soft text-warn"
        : "border-line-strong bg-surface text-subtle";
  return (
    <span
      className={`rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wider ${tone}`}
    >
      {status}
    </span>
  );
}

function tokenStatus(
  token: ProjectAutomationToken,
): "active" | "expired" | "revoked" {
  if (token.revoked_at) return "revoked";
  if (new Date(token.expires_at).getTime() <= Date.now()) return "expired";
  return "active";
}

function formatDate(value: string): string {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return "Unknown";
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}

function defaultAbsoluteExpiry(): string {
  const date = new Date(Date.now() + 24 * 60 * 60 * 1000);
  date.setMinutes(Math.ceil(date.getMinutes() / 15) * 15, 0, 0);
  return localDateTimeValue(date);
}

function minimumAbsoluteExpiry(): string {
  return localDateTimeValue(new Date(Date.now() + 60 * 1000));
}

function localDateTimeValue(date: Date): string {
  const offset = date.getTimezoneOffset() * 60 * 1000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}
