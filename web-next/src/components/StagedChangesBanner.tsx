// StagedChangesBanner — retained export name for the environment Changes bar.
// Pending configuration and asynchronous Activity are deliberately separate:
// a user reviews one durable revision, then applies and deploys it as one
// environment operation. Tab-local values are only a recovery buffer and are
// cleared as soon as the encrypted server draft is acknowledged.
//
// The project canvas mounts it once for the selected environment. It reads
// both the aggregate input buffer for that environment (across every
// service) and the authoritative environment changeset, so one tray covers
// edits made in any service drawer and continues to render after refresh.

import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  api,
  ApiError,
  type EnvironmentChangesetItem,
  type EnvironmentDeploymentAction,
  type EnvironmentOperationGroup,
} from "@/api/client";
import { queryKeys, useEnvironmentChangeset } from "@/api/hooks";
import {
  clearStagedForService,
  discardChange,
  isStagedVariableChange,
  stagedChangeKind,
  useStagedForEnv,
  type StagedChange,
} from "@/lib/stagedChanges";
import { SCOPE_LABEL } from "./panels/variables/helpers";
import { useCommitStagedChanges } from "./panels/variables/api";
import { presentError } from "@/lib/errorPresentation";

export type StagedChangesBannerProps = {
  projectId?: string;
  environmentId?: string;
  /** Optional map serviceId → display label, for nicer diff headers. */
  serviceLabels?: Record<string, string>;
  operations?: Array<{
    serviceId: string;
    serviceLabel: string;
    deployment: {
      deployment_id?: string;
      operation_id: string;
      operation_state: string;
      last_event?: string;
    };
  }>;
  onViewService?: (serviceId: string) => void;
  onViewDeployment?: (serviceId: string, deploymentId?: string) => void;
};

export function StagedChangesBanner({
  projectId,
  environmentId,
  serviceLabels,
  operations = [],
  onViewService,
  onViewDeployment,
}: StagedChangesBannerProps) {
  const changes = useStagedForEnv(environmentId);
  const durable = useEnvironmentChangeset(environmentId);
  const queryClient = useQueryClient();
  const [reviewOpen, setReviewOpen] = useState(false);
  const [activityOpen, setActivityOpen] = useState(false);
  const [message, setMessage] = useState("");
  const [staleRevision, setStaleRevision] = useState(false);
  const [staleRefreshStatus, setStaleRefreshStatus] = useState<
    "idle" | "refreshing" | "ready" | "error"
  >("idle");
  const [saveStatus, setSaveStatus] = useState<"idle" | "waiting" | "saving" | "saved" | "error">("idle");
  const [submittedActivity, setSubmittedActivity] = useState<{
    id: string;
    environment_id: string;
    state: string;
    services: Array<{
      service_id: string;
      requires_deploy?: boolean;
      deployment_action?: EnvironmentDeploymentAction;
      intake_state: string;
      operation_id?: string;
      deployment_id?: string;
      operation_state?: string;
      deployment_state?: string;
      failure?: string;
    }>;
  } | null>(null);
  const lastAutosaveAttempt = useRef("");
  const reviewTrigger = useRef<HTMLButtonElement | null>(null);
  const reviewPanel = useRef<HTMLDivElement | null>(null);
  const reviewOrigin = useRef<HTMLElement | null>(null);
  const reviewWasOpen = useRef(false);
  const previousEnvironment = useRef(environmentId);
  const commit = useCommitStagedChanges(projectId);
  const scopedSubmittedActivity =
    submittedActivity?.environment_id === environmentId
      ? submittedActivity
      : null;
  const operationGroup = useQuery({
    queryKey: [
      "environment-operation-group",
      environmentId,
      scopedSubmittedActivity?.id,
    ],
    queryFn: ({ signal }) =>
      api.getEnvironmentOperationGroup(
        environmentId as string,
        scopedSubmittedActivity?.id as string,
        signal,
      ),
    enabled: Boolean(environmentId && scopedSubmittedActivity?.id),
    refetchInterval: (query) =>
      isTerminalActivityState(query.state.data?.state) ? false : 4_000,
  });
  const visibleActivity = operationGroup.data
    ? activityFromOperationGroup(operationGroup.data)
    : scopedSubmittedActivity;
  const changeset = durable.data?.changeset?.state === "draft"
    ? durable.data.changeset
    : null;
  const running = operations.filter(({ deployment }) => {
    const state = deployment.operation_state.toLowerCase();
    return state === "queued" || state === "running";
  });
  const visibleOperationIds = new Set(
    visibleActivity?.services
      .map((service) => service.operation_id)
      .filter((id): id is string => Boolean(id)) ?? [],
  );
  const activityOperations = running.filter(({ deployment }, index) =>
    !visibleOperationIds.has(deployment.operation_id) &&
    running.findIndex(
      (candidate) =>
        candidate.deployment.operation_id === deployment.operation_id,
    ) === index,
  );
  const refreshLatestRevision = async () => {
    setStaleRefreshStatus("refreshing");
    try {
      await queryClient.invalidateQueries({
        queryKey: queryKeys.environmentChangeset(environmentId),
        refetchType: "none",
      });
      const latest = await durable.refetch();
      if (latest.error) throw latest.error;
      setStaleRefreshStatus("ready");
    } catch {
      setStaleRefreshStatus("error");
    }
  };
  const handleRevisionConflict = (error: unknown) => {
    if (!(error instanceof ApiError) || error.status !== 412) return;
    setStaleRevision(true);
    void refreshLatestRevision();
  };
  const reviewLatestRevision = () => {
    apply.reset();
    applyAndDeploy.reset();
    discard.reset();
    setStaleRevision(false);
    setStaleRefreshStatus("idle");
    openReview();
  };
  const reconcile = async () => {
    await queryClient.invalidateQueries({
      queryKey: queryKeys.environmentChangeset(environmentId),
    });
    await Promise.all(
      (changeset?.items ?? []).map((item) =>
        queryClient.invalidateQueries({
          queryKey: queryKeys.variables(item.service_id, environmentId),
        }),
      ),
    );
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: queryKeys.environmentResources(environmentId) }),
      queryClient.invalidateQueries({ queryKey: ["service-environment-config"] }),
      queryClient.invalidateQueries({ queryKey: ["tenant-generated-domains"] }),
      queryClient.invalidateQueries({ queryKey: ["volume-recovery"] }),
    ]);
  };
  const apply = useMutation({
    mutationFn: () =>
      api.applyEnvironmentChangeset(
        environmentId as string,
        changeset?.revision as number,
      ),
    onSuccess: reconcile,
    onError: handleRevisionConflict,
  });
  const applyAndDeploy = useMutation({
    mutationFn: () =>
      api.applyAndDeployEnvironmentChangeset(
        environmentId as string,
        changeset?.revision as number,
      ),
    onSuccess: (result) => {
      const resourceImpact = new Map(
        (result.validation.affected_resources ?? []).map((impact) => [
          impact.service_id,
          impact,
        ]),
      );
      setSubmittedActivity(
        result.operation_group
          ? {
              id: result.operation_group.id,
              environment_id: result.operation_group.environment_id,
              state: result.operation_group.state,
              services: result.operation_group.items.map((item) => ({
                service_id: item.service_id,
                requires_deploy: item.requires_deploy,
                deployment_action: item.deployment_action,
                intake_state: item.intake_state,
                deployment_id: item.deployment_id,
                operation_id: item.operation_id,
                failure: item.failure_reason || item.failure_code,
              })),
            }
          : {
              id: result.operation_group_id,
              environment_id: environmentId as string,
              state: "accepted",
              services: result.affected_services.map((serviceId) => ({
                service_id: serviceId,
                requires_deploy: resourceImpact.get(serviceId)?.requires_deploy,
                deployment_action: resourceImpact.get(serviceId)?.deployment_action,
                intake_state: "accepted",
              })),
            },
      );
      setReviewOpen(false);
      void reconcile();
    },
    onError: handleRevisionConflict,
  });
  const discard = useMutation({
    mutationFn: () =>
      api.discardEnvironmentChangeset(
        environmentId as string,
        changeset?.revision as number,
      ),
    onSuccess: reconcile,
    onError: handleRevisionConflict,
  });

  const grouped = useMemo(() => groupByService(changes), [changes]);
  const durableGrouped = useMemo(
    () => {
      const out: Record<string, NonNullable<typeof changeset>["items"]> = {};
      for (const item of changeset?.items ?? []) {
        (out[item.service_id] ??= []).push(item);
      }
      return out;
    },
    [changeset?.items],
  );
  const validationIssues = useMemo(
    () => associateValidationErrors(
      durable.data?.validation?.errors ?? [],
      changeset?.items ?? [],
    ),
    [changeset?.items, durable.data?.validation?.errors],
  );

  const hasSensitiveInput = changes.some(isSensitiveChange);
  const canAutosave =
    changes.length > 0 &&
    !hasSensitiveInput &&
    !changeset &&
    !reviewOpen &&
    Boolean(environmentId);
  const autosaveFingerprint = useMemo(
    () => canAutosave ? JSON.stringify(changes) : "",
    [canAutosave, changes],
  );
  const busy =
    commit.isPending ||
    apply.isPending ||
    applyAndDeploy.isPending ||
    discard.isPending;
  const revisionActionsBlocked =
    staleRevision ||
    staleRefreshStatus === "refreshing" ||
    staleRefreshStatus === "error";
  const actionError =
    commit.error ??
    durable.error ??
    apply.error ??
    applyAndDeploy.error ??
    discard.error;
  const actionErrorPresentation = actionError
    ? presentError(actionError)
    : undefined;
  const applyDisabledReason = changeset
    ? staleRevision
      ? staleRefreshStatus === "refreshing"
        ? "Reloading the latest saved revision before any change action can run."
        : staleRefreshStatus === "error"
          ? "Reload the latest saved revision before retrying any change action."
          : "Review the latest saved revision before retrying any change action."
      : changes.length > 0
      ? "Discard or save the newer edits before applying this saved revision."
      : !durable.data?.validation?.valid
        ? durable.data?.validation?.errors[0] ||
          "Resolve the validation errors before applying."
        : busy
          ? "Another change action is still in progress."
          : undefined
    : undefined;

  useEffect(() => {
    if (previousEnvironment.current === environmentId) return;
    previousEnvironment.current = environmentId;
    setSubmittedActivity(null);
    setActivityOpen(false);
    setReviewOpen(false);
    setMessage("");
    setSaveStatus("idle");
    setStaleRevision(false);
    setStaleRefreshStatus("idle");
    lastAutosaveAttempt.current = "";
  }, [environmentId]);

  // Safe auto-save boundary: only drafts without secret/sealed plaintext are
  // sent after a short debounce. Sensitive input always requires an explicit
  // user action and is removed from the tab when the server acknowledges it.
  useEffect(() => {
    if (!autosaveFingerprint || busy || lastAutosaveAttempt.current === autosaveFingerprint) {
      return;
    }
    setSaveStatus("waiting");
    const timer = window.setTimeout(() => {
      lastAutosaveAttempt.current = autosaveFingerprint;
      setSaveStatus("saving");
      commit.mutate(
        { changes },
        {
          onSuccess: () => setSaveStatus("saved"),
          onError: () => setSaveStatus("error"),
        },
      );
    }, 700);
    return () => window.clearTimeout(timer);
  }, [autosaveFingerprint, busy, changes, commit]);

  useEffect(() => {
    if (reviewOpen) {
      reviewWasOpen.current = true;
      reviewPanel.current?.focus();
      return;
    }
    if (reviewWasOpen.current) {
      reviewWasOpen.current = false;
      (reviewOrigin.current ?? reviewTrigger.current)?.focus();
      reviewOrigin.current = null;
    }
  }, [reviewOpen]);

  const openReview = () => {
    reviewOrigin.current =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : reviewTrigger.current;
    setReviewOpen(true);
  };

  const closeReview = () => setReviewOpen(false);

  useEffect(() => {
    if (!changeset && changes.length === 0) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (!(event.metaKey || event.ctrlKey) || event.key !== "Enter") return;
      const target = event.target;
      if (
        target instanceof HTMLInputElement ||
        target instanceof HTMLTextAreaElement ||
        target instanceof HTMLSelectElement
      ) return;
      event.preventDefault();
      openReview();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [changes.length, changeset]);

  useEffect(() => {
    if (!reviewOpen) return;
    const onEscape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      closeReview();
    };
    document.addEventListener("keydown", onEscape, true);
    return () => document.removeEventListener("keydown", onEscape, true);
  }, [reviewOpen]);

  function runCommit() {
    if (busy) return;
    commit.mutate(
      { changes, message: message.trim() || undefined },
      {
        onSuccess: () => {
          setMessage("");
          setSaveStatus("saved");
        },
        onError: () => setSaveStatus("error"),
      },
    );
  }

  function discardLocalChanges() {
    for (const serviceId of new Set(changes.map((change) => change.serviceId))) {
      clearStagedForService(serviceId, environmentId);
    }
  }

  const activityCount = activityOperations.length + (visibleActivity ? 1 : 0);
  const activityTone = describeActivityTone(visibleActivity, activityOperations);
  if (changes.length === 0 && !changeset && activityCount === 0 && !staleRevision) return null;

  return (
    <div className="pointer-events-none fixed inset-x-0 bottom-4 z-40 flex justify-center px-3 sm:px-4">
      <div className="grid w-full max-w-3xl gap-2">
        {activityCount > 0 ? (
          <section className="pointer-events-auto ml-auto w-full max-w-md overflow-hidden rounded-[var(--r-lg)] border border-line bg-surface-2 shadow-[var(--shadow-lg)]" aria-label="Activity">
            <button
              type="button"
              onClick={() => setActivityOpen((value) => !value)}
              aria-expanded={activityOpen}
              className="flex w-full items-center gap-2 px-3 py-2 text-left"
            >
              <span
                className={`h-2 w-2 rounded-full ${activityTone.className} ${activityTone.running ? "animate-pulse" : ""}`}
                aria-hidden
              />
              <strong className="text-xs text-text">Activity</strong>
              <span className="text-xs text-muted">
                {activityCount} {activityCount === 1 ? "item" : "items"}
              </span>
              <span className="ml-auto text-[10px] text-subtle">{activityOpen ? "Hide" : "View"}</span>
            </button>
            {activityOpen ? (
              <div
                className="grid max-h-48 gap-1.5 overflow-y-auto border-t border-line p-2.5"
                aria-live="polite"
              >
                {operationGroup.error ? (
                  <div role="alert" className="flex flex-wrap items-center gap-2 rounded-[var(--r-sm)] border border-danger/40 bg-danger-soft px-2.5 py-2 text-xs text-danger">
                    <span>{presentError(operationGroup.error).message}</span>
                    <button
                      type="button"
                      className="ml-auto font-semibold underline-offset-2 hover:underline"
                      onClick={() => void operationGroup.refetch()}
                    >
                      Retry status
                    </button>
                  </div>
                ) : null}
                {visibleActivity ? (
                  <div className="rounded-[var(--r-sm)] bg-surface px-2.5 py-2 text-xs">
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-semibold text-text">Apply &amp; deploy</span>
                      <span className={`font-mono text-[10px] ${activityTone.textClassName}`}>{visibleActivity.state}</span>
                    </div>
                    <ul className="m-0 mt-1.5 grid list-none gap-1 p-0">
                      {visibleActivity.services.map((service) => (
                        <li key={service.service_id} className="flex flex-wrap items-center gap-2 text-[11px]">
                          {onViewService ? (
                            <button
                              type="button"
                              className="font-semibold text-muted underline-offset-2 hover:text-text hover:underline"
                              onClick={() => onViewService(service.service_id)}
                            >
                              {serviceLabels?.[service.service_id] ?? service.service_id}
                            </button>
                          ) : (
                            <span className="font-semibold text-muted">{serviceLabels?.[service.service_id] ?? service.service_id}</span>
                          )}
                          <span className="text-subtle">
                            {service.requires_deploy === true
                              ? service.deployment_state ||
                                service.operation_state ||
                                service.intake_state
                              : service.requires_deploy === false
                                ? "applied without deploy"
                                : "deployment impact unavailable"}
                          </span>
                          <span className="rounded bg-surface-3 px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wider text-muted">
                            {deploymentActionLabel(service.deployment_action)}
                          </span>
                          {service.failure ? <span className="ml-auto text-danger">{service.failure}</span> : null}
                          {service.deployment_id && onViewDeployment ? (
                            <button
                              type="button"
                              className="ml-auto font-semibold text-accent hover:underline"
                              onClick={() => onViewDeployment(service.service_id, service.deployment_id)}
                            >
                              View deployment
                            </button>
                          ) : null}
                        </li>
                      ))}
                    </ul>
                  </div>
                ) : null}
                {activityOperations.map(({ serviceId, serviceLabel, deployment }) => (
                  <div key={deployment.operation_id} className="flex items-center gap-2 rounded-[var(--r-sm)] bg-surface px-2.5 py-2 text-xs">
                    <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-warn" aria-hidden />
                    {onViewService ? (
                      <button type="button" className="font-semibold text-text hover:underline" onClick={() => onViewService(serviceId)}>
                        {serviceLabel}
                      </button>
                    ) : <span className="font-semibold text-text">{serviceLabel}</span>}
                    <span className="text-muted">{deployment.last_event || deployment.operation_state.toLowerCase()}</span>
                    {deployment.deployment_id && onViewDeployment ? (
                      <button type="button" className="ml-auto font-semibold text-accent hover:underline" onClick={() => onViewDeployment(serviceId, deployment.deployment_id)}>
                        View deployment
                      </button>
                    ) : <span className="ml-auto font-mono text-[10px] text-subtle">{deployment.operation_id}</span>}
                  </div>
                ))}
              </div>
            ) : null}
          </section>
        ) : null}

        {changes.length > 0 || changeset || staleRevision ? (
          <section
            className={`pointer-events-auto overflow-hidden rounded-[var(--r-lg)] border border-staged bg-surface-2 shadow-[var(--shadow-lg)] ${
              reviewOpen
                ? "max-sm:fixed max-sm:inset-0 max-sm:z-50 max-sm:flex max-sm:flex-col max-sm:rounded-none"
                : ""
            }`}
            aria-label="Changes"
          >
            <div className="flex flex-wrap items-center gap-2.5 px-3 py-2.5 sm:px-4">
              <span aria-hidden className="h-2 w-2 shrink-0 rounded-full bg-staged" />
              <strong className="text-sm text-text">Changes</strong>
              <span className="text-xs text-muted">
                {changeset
                  ? `${changeset.items.length} saved · revision ${changeset.revision}`
                  : staleRevision && changes.length === 0
                    ? "Latest revision needs review"
                    : `${changes.length} change${changes.length === 1 ? "" : "s"}`}
              </span>
              {changes.length > 0 && changeset ? (
                <span className="rounded bg-warn/10 px-1.5 py-0.5 text-[10px] font-semibold text-warn">
                  {changes.length} not saved
                </span>
              ) : null}
              {!changeset && changes.length > 0 ? (
                <span role="status" aria-live="polite" className={"text-[10px] " + (saveStatus === "error" ? "text-danger" : "text-subtle")}>
                  {hasSensitiveInput
                    ? "Secure values wait for confirmation"
                    : saveStatus === "saving" || commit.isPending
                      ? "Saving…"
                      : saveStatus === "error"
                        ? "Not saved"
                        : "Autosaves securely"}
                </span>
              ) : null}

              <div className="ml-auto flex items-center gap-1.5">
                <button
                  ref={reviewTrigger}
                  type="button"
                  onClick={reviewOpen ? closeReview : openReview}
                  aria-expanded={reviewOpen}
                  className="rounded-[var(--r-sm)] border border-line px-2.5 py-1.5 text-xs font-semibold text-muted hover:bg-surface-3"
                >
                  {reviewOpen ? "Close review" : "Review"}
                </button>
                {changeset ? (
                  <button
                    type="button"
                    disabled={revisionActionsBlocked || busy || !durable.data?.validation?.valid || changes.length > 0}
                    aria-describedby={applyDisabledReason ? "apply-deploy-disabled-reason" : undefined}
                    title={changes.length > 0
                      ? "Discard the newer unsaved edits before applying this saved revision"
                      : durable.data?.validation?.valid
                        ? "Apply revision and deploy affected services"
                        : "Resolve validation errors before applying"}
                    onClick={() => applyAndDeploy.mutate()}
                    className="rounded-[var(--r-sm)] border border-accent bg-accent px-3 py-1.5 text-xs font-semibold text-[var(--accent-ink)] transition-colors hover:brightness-110 disabled:opacity-50"
                  >
                    {applyAndDeploy.isPending ? "Applying & deploying…" : "Apply & deploy"}
                  </button>
                ) : hasSensitiveInput ? (
                  <button
                    type="button"
                    disabled={busy || Boolean(changeset)}
                    onClick={runCommit}
                    className="rounded-[var(--r-sm)] border border-accent bg-accent px-3 py-1.5 text-xs font-semibold text-[var(--accent-ink)] transition-colors hover:brightness-110 disabled:opacity-50"
                  >
                    {commit.isPending ? "Saving…" : "Save for review"}
                  </button>
                ) : reviewOpen ? (
                  <button
                    type="button"
                    disabled={busy}
                    onClick={runCommit}
                    className="rounded-[var(--r-sm)] border border-accent bg-accent px-3 py-1.5 text-xs font-semibold text-[var(--accent-ink)] transition-colors hover:brightness-110 disabled:opacity-50"
                  >
                    {commit.isPending ? "Saving…" : "Save for review"}
                  </button>
                ) : saveStatus === "error" ? (
                  <button
                    type="button"
                    disabled={busy}
                    onClick={runCommit}
                    className="rounded-[var(--r-sm)] border border-accent bg-accent-soft px-3 py-1.5 text-xs font-semibold text-accent disabled:opacity-50"
                  >
                    Retry save
                  </button>
                ) : null}
                {changeset ? (
                  <details className="relative">
                    <summary className="cursor-pointer list-none rounded-[var(--r-sm)] border border-line px-2 py-1.5 text-xs text-muted hover:bg-surface-3" aria-label="Advanced change actions">•••</summary>
                    <div className="absolute bottom-full right-0 z-10 mb-1 grid min-w-48 gap-1 rounded-[var(--r-sm)] border border-line bg-surface-2 p-1.5 shadow-[var(--shadow-lg)]">
                      <button type="button" disabled={revisionActionsBlocked || busy || !durable.data?.validation?.valid || changes.length > 0} onClick={() => apply.mutate()} className="rounded px-2 py-1.5 text-left text-xs text-muted hover:bg-surface-3 disabled:opacity-50">Apply without deploying</button>
                      <button type="button" disabled={revisionActionsBlocked || busy} onClick={() => discard.mutate()} className="rounded px-2 py-1.5 text-left text-xs text-danger hover:bg-danger-soft disabled:opacity-50">Discard saved revision</button>
                    </div>
                  </details>
                ) : null}
              </div>
            </div>

            {applyDisabledReason ? (
              <p
                id="apply-deploy-disabled-reason"
                role="status"
                className="m-0 border-t border-line px-3 py-2 text-[11px] text-warn sm:px-4"
              >
                {applyDisabledReason}
              </p>
            ) : null}
            {staleRevision ? (
              <div
                role="alert"
                className="flex flex-wrap items-center gap-2 border-t border-warn/40 bg-warn/10 px-3 py-2 text-xs text-warn sm:px-4"
              >
                <span>
                  {staleRefreshStatus === "refreshing"
                    ? "This saved revision changed on the server. Midnight is reloading the latest draft; all revision actions remain disabled and edits in this tab are preserved."
                    : staleRefreshStatus === "error"
                      ? "This saved revision changed on the server. The latest draft could not be reloaded; reload it before retrying. Edits in this tab are preserved."
                      : "The latest saved revision is loaded. Review it before retrying; edits in this tab were preserved."}
                </span>
                <button
                  type="button"
                  disabled={staleRefreshStatus === "refreshing"}
                  className="ml-auto rounded-[var(--r-sm)] border border-warn/50 px-2 py-1 font-semibold text-warn disabled:opacity-50"
                  onClick={
                    staleRefreshStatus === "ready" && changeset
                      ? reviewLatestRevision
                      : () => void refreshLatestRevision()
                  }
                >
                  {staleRefreshStatus === "refreshing"
                    ? "Reloading latest…"
                    : staleRefreshStatus === "ready" && changeset
                      ? "Review latest"
                      : "Reload latest"}
                </button>
              </div>
            ) : actionErrorPresentation ? (
              <p
                role="alert"
                className="m-0 border-t border-danger/40 bg-danger-soft px-3 py-2 text-xs text-danger sm:px-4"
              >
                {actionErrorPresentation.message}
                {actionErrorPresentation.recovery
                  ? ` ${actionErrorPresentation.recovery}`
                  : ""}
              </p>
            ) : null}

            {reviewOpen ? (
              <div
                ref={reviewPanel}
                role="dialog"
                aria-labelledby="change-review-title"
                tabIndex={-1}
                className="max-h-[55vh] overflow-y-auto border-t border-line px-3 py-3 outline-none sm:px-4 max-sm:min-h-0 max-sm:max-h-none max-sm:flex-1"
              >
                <div className="mb-3 flex items-start justify-between gap-3">
                  <div>
                    <h2 id="change-review-title" className="m-0 text-sm font-semibold text-text">Review changes</h2>
                    <p className="m-0 mt-0.5 text-[11px] text-subtle">Grouped by affected service. Sensitive values are never shown.</p>
                  </div>
                  <div className="flex items-center gap-2">
                    {changes.length > 0 ? (
                      <button type="button" onClick={discardLocalChanges} className="text-[10px] font-semibold uppercase tracking-wider text-danger">Discard {changeset ? "unsaved" : "all"}</button>
                    ) : null}
                    <kbd className="hidden rounded border border-line px-1.5 py-0.5 font-mono text-[9px] text-subtle sm:inline">⌘/Ctrl Enter</kbd>
                  </div>
                </div>

                {validationIssues.global.length > 0 ? (
                  <section aria-label="Global validation issues" className="mb-2 grid gap-1.5">
                    <ValidationIssues errors={validationIssues.global} />
                  </section>
                ) : null}
                {durable.data?.validation?.warnings.map((warning) => (
                  <p key={warning} className="mb-2 rounded-[var(--r-sm)] border border-warn/40 bg-warn/10 px-2.5 py-1.5 text-xs text-warn">{warning}</p>
                ))}

                {changes.length > 0 ? <div className="grid gap-3">
                  <p className={"m-0 text-[10px] font-semibold uppercase tracking-wider " + (changeset ? "text-warn" : "text-subtle")}>
                    {changeset
                      ? `Not in saved revision ${changeset.revision} · discard these edits before applying`
                      : hasSensitiveInput
                        ? "Ready to save securely"
                        : reviewOpen
                          ? "Ready to save"
                          : "Autosaving draft"}
                  </p>
              {Object.entries(grouped).map(([serviceId, list]) => (
                <ServiceChangeGroup
                  key={serviceId}
                  label={serviceLabels?.[serviceId] ?? serviceId}
                  impactKnown={false}
                >
                  <div className="flex items-center justify-between">
                    <button
                      type="button"
                      onClick={() =>
                        clearStagedForService(serviceId, environmentId)
                      }
                      className="text-[10px] uppercase tracking-wider text-subtle hover:text-danger"
                    >
                      Discard all
                    </button>
                  </div>
                  <ul className="grid gap-1">
                    {list.map((c) => (
                      <DiffRow
                        key={`${stagedChangeKind(c)}:${c.name}`}
                        change={c}
                        onDiscard={() =>
                          discardChange(
                            c.serviceId,
                            environmentId,
                            c.name,
                            stagedChangeKind(c),
                          )
                        }
                      />
                    ))}
                  </ul>
                </ServiceChangeGroup>
              ))}
            </div> : null}

            {changeset ? (
              <section className="grid gap-3" aria-labelledby="durable-changeset-title">
                <h3 id="durable-changeset-title" className="m-0 text-[10px] font-semibold uppercase tracking-wider text-subtle">Saved draft · revision {changeset.revision}</h3>
                {Object.entries(durableGrouped).map(([serviceId, list]) => (
                  <ServiceChangeGroup
                    key={serviceId}
                    label={serviceLabels?.[serviceId] ?? serviceId}
                    deploymentAction={durable.data?.validation?.affected_resources?.find((impact) => impact.service_id === serviceId)?.deployment_action}
                    impactKnown={Boolean(durable.data?.validation?.affected_resources?.find((impact) => impact.service_id === serviceId)?.deployment_action)}
                    errors={validationIssues.byService[serviceId]}
                  >
                    {(list ?? []).map((item) => (
                      <div key={item.id} className="grid gap-1">
                        <div className="flex items-center gap-2 rounded-[var(--r-sm)] bg-surface px-2.5 py-1.5 text-xs">
                          <OpBadge op={item.operation} />
                          <span className="font-mono text-text">{item.name}</span>
                          <span className="text-[9px] uppercase tracking-wider text-subtle">{item.kind}{item.scope ? ` · ${item.scope}` : ""}</span>
                          <span className="ml-auto text-staged">{item.value_change}</span>
                        </div>
                        <ValidationIssues
                          errors={validationIssues.byItem[item.id]}
                          label={`Validation issue for ${item.name}`}
                        />
                      </div>
                    ))}
                  </ServiceChangeGroup>
                ))}
                <p className="m-0 text-[10px] text-subtle">Apply &amp; deploy commits this exact revision once, then queues only the services marked for deployment. Use the advanced action when you intentionally need to apply without deploying.</p>
              </section>
            ) : null}

            {changes.length > 0 && !changeset ? <div className="mt-3 grid gap-1.5">
              <label
                htmlFor="staged-commit-msg"
                className="text-[10px] uppercase tracking-wider text-subtle"
              >
                Change note (optional before save)
              </label>
              <input
                id="staged-commit-msg"
                type="text"
                value={message}
                onChange={(e) => setMessage(e.target.value)}
                placeholder="Update database credentials"
                className="w-full rounded-[var(--r-sm)] border border-line bg-surface px-2.5 py-1.5 text-xs text-text outline-none placeholder:text-subtle focus:border-line-strong focus:shadow-[var(--focus-ring)]"
              />
              <p className="text-[10px] text-subtle">
                {hasSensitiveInput
                  ? "Secure and sealed values remain only in this tab until you choose Save for review."
                  : "Non-secret edits autosave after a short pause. If saving fails, the edits stay in this tab for retry."}
              </p>
            </div> : null}
              </div>
            ) : null}
          </section>
        ) : null}
      </div>
    </div>
  );
}

function ServiceChangeGroup({
  label,
  deploymentAction,
  impactKnown,
  errors,
  children,
}: {
  label: string;
  deploymentAction?: EnvironmentDeploymentAction;
  impactKnown: boolean;
  errors?: string[];
  children: React.ReactNode;
}) {
  return (
    <section className="grid gap-1.5 rounded-[var(--r-sm)] border border-line bg-surface-2 p-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="m-0 font-mono text-[11px] font-semibold text-text">{label}</h3>
        <span className={"rounded px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wider " + (
          impactKnown && deploymentAction
            ? deploymentActionClassName(deploymentAction)
            : "bg-warn/10 text-warn"
        )}>
          {impactKnown && deploymentAction
            ? deploymentActionLabel(deploymentAction)
            : "Impact checking"}
        </span>
      </div>
      <ValidationIssues errors={errors} label={`Validation issues for ${label}`} />
      {children}
    </section>
  );
}

function ValidationIssues({
  errors,
  label,
}: {
  errors?: string[];
  label?: string;
}) {
  if (!errors?.length) return null;
  return (
    <div className="grid gap-1" aria-label={label}>
      {errors.map((error) => (
        <p
          key={error}
          role="alert"
          className="m-0 rounded-[var(--r-sm)] border border-danger/40 bg-danger-soft px-2.5 py-1.5 text-xs text-danger"
        >
          {error}
        </p>
      ))}
    </div>
  );
}

function DiffRow({
  change,
  onDiscard,
}: {
  change: StagedChange;
  onDiscard: () => void;
}) {
  const { op, name } = change;
  if (!isStagedVariableChange(change)) {
    const detail = stagedResourceDetail(change);
    return (
      <li className="flex items-start gap-2 rounded-[var(--r-sm)] bg-surface px-2.5 py-1.5 text-xs">
        <OpBadge op={op} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <span className="font-mono text-text">{name}</span>
            <span className="text-[9px] uppercase tracking-wider text-subtle">
              {change.kind}
            </span>
          </div>
          <div className="mt-0.5 truncate font-mono text-[11px] text-staged">
            {detail}
          </div>
        </div>
        <DiscardButton name={name} onDiscard={onDiscard} />
      </li>
    );
  }
  const { value, base, scope } = change;
  const sensitive =
    scope === "secret" ||
    scope === "sealed" ||
    base?.scope === "secret" ||
    base?.scope === "sealed" ||
    base?.sealed === true;

  const oldText = base
    ? sensitive
      ? "value hidden"
      : `v${base.version} · ${base.lengthBucket}`
    : "—";
  const newText =
    op === "remove"
      ? "removed"
      : sensitive
        ? base
          ? "value replaced"
          : "value set"
        : value === ""
          ? "(empty)"
          : value;

  return (
    <li className="flex items-start gap-2 rounded-[var(--r-sm)] bg-surface px-2.5 py-1.5 text-xs">
      <OpBadge op={op} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          <span className="font-mono text-text">{name}</span>
          <span className="text-[9px] uppercase tracking-wider text-subtle">
            {SCOPE_LABEL[scope]}
          </span>
        </div>
        <div className="mt-0.5 flex items-center gap-1.5 font-mono text-[11px]">
          <span className="truncate text-subtle line-through">{oldText}</span>
          <span aria-hidden className="text-subtle">
            →
          </span>
          <span className={"truncate " + (op === "remove" ? "text-danger" : "text-staged")}>
            {newText}
          </span>
        </div>
      </div>
      <DiscardButton name={name} onDiscard={onDiscard} />
    </li>
  );
}

function stagedResourceDetail(change: Exclude<StagedChange, { kind?: "variable" }>): string {
  switch (change.kind) {
    case "runtime":
      return `${change.op === "add" ? "create" : "update"} · ${change.runtime.runtime_kind} · ${change.runtime.replicas} replica${change.runtime.replicas === 1 ? "" : "s"}`;
    case "networking":
      return change.op === "remove" ? "remove generated public domain" : "add generated public domain";
    case "storage":
      if (change.op === "remove") return "remove volume";
      return `${change.op === "add" ? "create" : "resize"} · ${change.storage?.size_bytes ?? 0} bytes · ${change.storage?.mount_path ?? ""}`;
  }
}

function DiscardButton({ name, onDiscard }: { name: string; onDiscard: () => void }) {
  return (
    <button
      type="button"
      onClick={onDiscard}
      aria-label={`Discard change to ${name}`}
      className="shrink-0 text-subtle hover:text-danger"
      title="Discard"
    >
      ✕
    </button>
  );
}

function OpBadge({ op }: { op: StagedChange["op"] }) {
  const map = {
    add: { label: "NEW", cls: "border-accent text-accent" },
    edit: { label: "MOD", cls: "border-staged text-staged" },
    remove: { label: "DEL", cls: "border-danger text-danger" },
  } as const;
  const { label, cls } = map[op];
  return (
    <span
      className={
        "mt-0.5 shrink-0 rounded border px-1 py-px font-mono text-[9px] font-semibold " + cls
      }
    >
      {label}
    </span>
  );
}

function groupByService(changes: StagedChange[]): Record<string, StagedChange[]> {
  const out: Record<string, StagedChange[]> = {};
  for (const c of changes) {
    (out[c.serviceId] ??= []).push(c);
  }
  for (const list of Object.values(out)) {
    list.sort((a, b) => a.name.localeCompare(b.name));
  }
  return out;
}

export function associateValidationErrors(
  errors: string[],
  items: EnvironmentChangesetItem[],
): {
  global: string[];
  byService: Record<string, string[]>;
  byItem: Record<string, string[]>;
} {
  const result = {
    global: [] as string[],
    byService: {} as Record<string, string[]>,
    byItem: {} as Record<string, string[]>,
  };
  for (const error of errors) {
    const normalized = error.toLocaleLowerCase();
    let candidates = items.filter((item) => {
      const name = item.name.trim().toLocaleLowerCase();
      return name.length >= 2 && normalized.includes(name);
    });
    if (candidates.length === 0) {
      const kind = validationErrorKind(normalized);
      if (kind) candidates = items.filter((item) => item.kind === kind);
    }
    if (candidates.length === 1) {
      (result.byItem[candidates[0].id] ??= []).push(error);
      continue;
    }
    const serviceIds = Array.from(
      new Set(candidates.map((item) => item.service_id)),
    );
    if (serviceIds.length === 1) {
      (result.byService[serviceIds[0]] ??= []).push(error);
      continue;
    }
    result.global.push(error);
  }
  return result;
}

function validationErrorKind(
  error: string,
): EnvironmentChangesetItem["kind"] | undefined {
  if (error.includes("runtime configuration") || error.includes("service configuration")) {
    return "runtime";
  }
  if (error.includes("domain")) return "networking";
  if (error.includes("volume")) return "storage";
  return undefined;
}

function isSensitiveChange(change: StagedChange): boolean {
  if (!isStagedVariableChange(change)) return false;
  return (
    change.scope === "secret" ||
    change.scope === "sealed" ||
    change.base?.scope === "secret" ||
    change.base?.scope === "sealed" ||
    change.base?.sealed === true
  );
}

function activityFromOperationGroup(group: EnvironmentOperationGroup) {
  return {
    id: group.id,
    environment_id: group.environment_id,
    state: group.state,
    services: group.items.map((item) => ({
      service_id: item.service_id,
      requires_deploy: item.requires_deploy,
      deployment_action: item.deployment_action,
      intake_state: item.intake_state,
      deployment_id: item.deployment_id,
      operation_id: item.operation_id,
      operation_state: item.operation_state,
      deployment_state: item.deployment_state,
      failure: item.failure_reason || item.failure_code,
    })),
  };
}

function deploymentActionLabel(action?: EnvironmentDeploymentAction): string {
  if (action === "rebuild") return "Build and deploy";
  if (action === "redeploy") return "Redeploy";
  if (action === "none") return "Apply only";
  return "Action pending";
}

function deploymentActionClassName(action: EnvironmentDeploymentAction): string {
  if (action === "rebuild") return "bg-warn/10 text-warn";
  if (action === "redeploy") return "bg-accent-soft text-accent";
  return "bg-surface-3 text-muted";
}

function isTerminalActivityState(state?: string): boolean {
  return Boolean(
    state && ["succeeded", "failed", "partial"].includes(state.toLowerCase()),
  );
}

function describeActivityTone(
  activity: ReturnType<typeof activityFromOperationGroup> | {
    state: string;
    services: Array<{
      failure?: string;
      operation_state?: string;
      deployment_state?: string;
    }>;
  } | null,
  running: StagedChangesBannerProps["operations"],
): { className: string; textClassName: string; running: boolean } {
  const states = [
    activity?.state,
    ...(activity?.services.flatMap((service) => [
      service.operation_state,
      service.deployment_state,
    ]) ?? []),
  ]
    .filter((state): state is string => Boolean(state))
    .map((state) => state.toLowerCase());
  const failed =
    activity?.services.some((service) => Boolean(service.failure)) ||
    states.some((state) => ["failed", "partial", "canceled"].includes(state));
  if (failed) {
    return {
      className: "bg-danger",
      textClassName: "text-danger",
      running: false,
    };
  }
  const pending =
    Boolean(running?.length) ||
    states.some((state) =>
      ["accepted", "queued", "running", "building", "deploying", "pending"].includes(state),
    );
  if (pending) {
    return {
      className: "bg-warn",
      textClassName: "text-warn",
      running: true,
    };
  }
  return {
    className: "bg-success",
    textClassName: "text-success",
    running: false,
  };
}
