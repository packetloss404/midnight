// ServiceSettings — durable, environment-scoped desired deployment config.

import {
	useEffect,
	useRef,
	useState,
	type FormEvent,
	type ReactNode,
} from "react";
import { useService, useEnvironments, useDeleteService } from "@/api/hooks";
import { ApiError } from "@/api/client";
import { CapabilityBoundary } from "@/components/CapabilityBoundary";
import { Loading, ErrorState } from "@/components/states";
import { presentError } from "@/lib/errorPresentation";
import { useProjectAccess } from "./automation-tokens/api";
import { DomainList } from "./DomainList";
import { RecoveryDomainList } from "./RecoveryDomainList";
import { TCPProxyList } from "./TCPProxyList";
import { CronOccurrenceHistory } from "./CronOccurrenceHistory";
import { Button, Field, TextInput } from "./form-kit";
import {
	createServiceConfigIntentKey,
	usePutServiceConfig,
	useServiceConfig,
	type PutServiceEnvironmentConfig,
	type ServiceConfigAuthority,
	type ServiceEnvironmentConfig,
} from "./settings/api";

export type ServiceSettingsProps = {
	serviceId: string;
	environmentId?: string;
	projectId?: string;
	mode?: "all" | "runtime" | "networking";
	authority: ServiceConfigAuthority;
};

export function ServiceSettings({
	serviceId,
	environmentId,
	projectId,
	mode = "all",
	authority,
}: ServiceSettingsProps) {
	const serviceQuery = useService(serviceId);
	const resolvedProjectId = projectId ?? serviceQuery.data?.project_id;
	const envsQuery = useEnvironments(resolvedProjectId, {
		enabled: mode === "all" && Boolean(resolvedProjectId),
	});

	const environments = envsQuery.data ?? [];
	const [selectedEnv, setSelectedEnv] = useState<string | undefined>(
		environmentId,
	);
	const exactServiceScope =
		serviceQuery.data?.id === serviceId &&
		Boolean(resolvedProjectId) &&
		(!projectId || serviceQuery.data?.project_id === projectId);
	const humanConfigCandidate =
		authority === "human" &&
		mode === "runtime" &&
		exactServiceScope &&
		Boolean(selectedEnv);
	const access = useProjectAccess(
		resolvedProjectId ?? "",
		selectedEnv,
		humanConfigCandidate,
	);

	// Default the env selection once the list arrives (prop wins if present).
	useEffect(() => {
		if (environmentId && environmentId !== selectedEnv) {
			setSelectedEnv(environmentId);
			return;
		}
		if (selectedEnv) return;
		if (environments.length > 0) setSelectedEnv(environments[0].id);
	}, [environmentId, environments, selectedEnv]);

	if (serviceQuery.isPending) return <Loading label="Loading service…" />;
	if (serviceQuery.error)
		return (
			<ErrorState
				title="Failed to load service"
				error={serviceQuery.error}
				onRetry={() => serviceQuery.refetch()}
			/>
		);

	if (
		authority === "human" &&
		mode !== "runtime" &&
		mode !== "networking"
	) {
		return (
			<CapabilityBoundary
				state="unavailable"
				title="Service configuration"
				detail="Your account can change runtime settings here, but not networking or the other sections, which need a separate permission. Ask an administrator for access."
			/>
		);
	}

	if (authority === "human" && !exactServiceScope) {
		return (
			<CapabilityBoundary
				state="unavailable"
				title="Service configuration"
				detail="Midnight could not verify that this service belongs to the selected project. No configuration request was sent."
			/>
		);
	}

	if (authority === "human" && mode === "runtime" && selectedEnv) {
		if (access.isPending) {
			return (
				<CapabilityBoundary
					state="degraded"
					title="Service configuration"
					detail="Checking the current project role and exact environment scope before loading desired configuration."
				/>
			);
		}
		const exactAccess =
			access.data?.principal_kind === "human" &&
			access.data.project_id === resolvedProjectId &&
			access.data.environment_id === selectedEnv;
		if (
			access.error ||
			!exactAccess ||
			!access.data?.permissions.includes("service.read")
		) {
			return (
				<CapabilityBoundary
					state="unavailable"
					title="Service configuration"
					detail={
						access.error
							? "Midnight could not verify service-read permission for this exact environment. No configuration request was sent."
							: !exactAccess
								? "Midnight could not verify the exact project and environment scope. No configuration request was sent."
								: "The current project role does not grant service-read permission. No configuration request was sent."
					}
				/>
			);
		}
	}

	return (
		<div className="grid gap-6">
			{mode === "all" ? (
				<IdentityBar
					serviceId={serviceQuery.data?.id ?? serviceId}
					slug={serviceQuery.data?.slug}
					environments={environments.map((e) => ({
						id: e.id,
						name: e.name,
					}))}
					selectedEnv={selectedEnv}
					onSelectEnv={setSelectedEnv}
					envsLoading={
						envsQuery.isPending && Boolean(resolvedProjectId)
					}
				/>
			) : null}

			{mode === "all" || mode === "runtime" ? (
				selectedEnv ? (
					<InstanceSettings
						projectId={resolvedProjectId!}
						serviceId={serviceId}
						environmentId={selectedEnv}
						authority={authority}
						canWrite={
							authority === "operator" ||
							access.data?.permissions.includes("service.write") ===
								true
						}
					/>
				) : (
					<NoEnvironment />
				)
			) : null}

			{mode === "all" || mode === "networking" ? (
				<NetworkingSettings
					projectId={resolvedProjectId}
					serviceId={serviceId}
					environmentId={selectedEnv}
					authority={authority}
				/>
			) : null}

			{mode === "all" || mode === "runtime" ? (
				<DangerZone
					serviceId={serviceQuery.data?.id ?? serviceId}
					slug={serviceQuery.data?.slug ?? serviceId}
					projectId={resolvedProjectId}
					resourceVersion={serviceQuery.data?.resource_version}
					canDelete={
						authority === "operator" ||
						access.data?.permissions.includes("service.delete") ===
							true
					}
					permissionChecked={
						authority === "operator" || !access.isPending
					}
				/>
			) : null}
		</div>
	);
}

function NoEnvironment() {
	return (
		<div className="rounded-lg border border-dashed border-line bg-bg-2 px-4 py-6 text-center text-sm text-muted">
			<strong className="block text-text">No environment to edit</strong>
			<span>
				Build, resource, and source settings are stored per environment.
				This project has no environment yet.
			</span>
		</div>
	);
}

function NetworkingSettings({
	projectId,
	serviceId,
	environmentId,
	authority,
}: {
	projectId?: string;
	serviceId: string;
	environmentId?: string;
	authority: ServiceConfigAuthority;
}) {
	if (authority === "human") {
		return (
			<Section
				title="Networking"
				description="Tenant-managed generated HTTP domains reconcile independently. Recovery-only TCP publication remains unavailable in this human session."
			>
				<DomainList
					projectId={projectId}
					serviceId={serviceId}
					environmentId={environmentId}
				/>
				<CapabilityBoundary
					state="unavailable"
					title="TCP publication"
					detail="Tenant-scoped TCP proxy management is not installed yet. No recovery-only networking request was sent."
				/>
			</Section>
		);
	}
	return (
		<RecoveryNetworkingSettings
			projectId={projectId}
			serviceId={serviceId}
			environmentId={environmentId}
			authority={authority}
		/>
	);
}

function RecoveryNetworkingSettings({
	projectId,
	serviceId,
	environmentId,
	authority,
}: {
	projectId?: string;
	serviceId: string;
	environmentId?: string;
	authority: ServiceConfigAuthority;
}) {
	const config = useServiceConfig({
		projectId: projectId ?? "",
		serviceId,
		environmentId: environmentId ?? "",
		authority,
		enabled: Boolean(projectId && environmentId),
	});
	return (
		<Section
			title="Networking"
			description="HTTP domains and L4 TCP publication reconcile independently; an unavailable domain controller does not hide TCP controls."
		>
			<RecoveryDomainList
				projectId={projectId}
				serviceId={serviceId}
				environmentId={environmentId}
			/>
			<TCPProxyList
				key={config.data?.version ?? "default-target-port"}
				projectId={projectId}
				serviceId={serviceId}
				environmentId={environmentId}
				defaultBackendPort={config.data?.target_port || undefined}
			/>
		</Section>
	);
}

// ── Env-scoped editable settings ──────────────────────────────────

function InstanceSettings({
	projectId,
	serviceId,
	environmentId,
	authority,
	canWrite,
}: {
	projectId: string;
	serviceId: string;
	environmentId: string;
	authority: ServiceConfigAuthority;
	canWrite: boolean;
}) {
	const configQuery = useServiceConfig({
		projectId,
		serviceId,
		environmentId,
		authority,
	});
	const saveConfig = usePutServiceConfig();
	const [draft, setDraft] = useState<PutServiceEnvironmentConfig | null>(
		null,
	);
	const [savedVersion, setSavedVersion] = useState<number | null>(null);
	const [staged, setStaged] = useState(false);
	const retryIntent = useRef<{ fingerprint: string; key: string }>();

	useEffect(() => {
		if (!configQuery.data) return;
		setDraft(configToPut(configQuery.data));
	}, [
		configQuery.data?.service_id,
		configQuery.data?.environment_id,
		configQuery.data?.version,
	]);

	useEffect(() => {
		setSavedVersion(null);
		setStaged(false);
		saveConfig.reset();
		retryIntent.current = undefined;
	}, [projectId, serviceId, environmentId, authority]);

	if (configQuery.isPending) return <Loading label="Loading settings…" />;
	if (configQuery.error)
		return (
			<ErrorState
				title="Failed to load service settings"
				error={configQuery.error}
				onRetry={() => configQuery.refetch()}
			/>
		);

	if (!draft || !configQuery.data)
		return <Loading label="Preparing settings…" />;

	const update = <K extends keyof PutServiceEnvironmentConfig>(
		field: K,
		value: PutServiceEnvironmentConfig[K],
	) =>
		setDraft((current) =>
			current ? { ...current, [field]: value } : current,
		);

	const updateRuntimeKind = (
		runtimeKind: PutServiceEnvironmentConfig["runtime_kind"],
	) => {
		setDraft((current) => {
			if (!current) return current;
			if (runtimeKind === "cron") {
				return {
					...current,
					runtime_kind: "cron",
					replicas: 1,
					restart_policy: "no",
					restart_retries: 0,
					health_kind: "none",
					predeploy_command: "",
				};
			}
			return {
				...current,
				runtime_kind: runtimeKind,
				cron_schedule: "",
			};
		});
	};

	const save = (event: FormEvent) => {
		event.preventDefault();
		if (!canWrite) return;
		const fingerprint = JSON.stringify({
			projectId,
			serviceId,
			environmentId,
			config: draft,
		});
		if (retryIntent.current?.fingerprint !== fingerprint) {
			retryIntent.current = {
				fingerprint,
				key: createServiceConfigIntentKey(),
			};
		}
		saveConfig.mutate(
			{
				projectId,
				serviceId,
				environmentId,
				authority,
				config: draft,
				idempotencyKey: retryIntent.current.key,
			},
			{
				onSuccess: (saved) => {
					if (saved.staged) {
						setStaged(true);
						setSavedVersion(null);
						retryIntent.current = undefined;
						return;
					}
					setDraft(configToPut(saved));
					setSavedVersion(saved.version);
					retryIntent.current = undefined;
				},
			},
		);
	};
	const saveError = saveConfig.error
		? presentError(saveConfig.error)
		: undefined;
	const reloadLatest = async () => {
		const latest = await configQuery.refetch();
		if (latest.data) {
			setDraft(configToPut(latest.data));
			setSavedVersion(null);
			saveConfig.reset();
			retryIntent.current = undefined;
		}
	};

	return (
		<form className="grid gap-6" onSubmit={save}>
			{!canWrite ? (
				<p
					role="status"
					className="m-0 rounded-[var(--r-sm)] border border-line bg-bg-2 px-3 py-2 text-xs text-muted"
				>
					This role has service-read access. Desired configuration is
					read-only until service-write permission is granted.
				</p>
			) : null}
			<fieldset disabled={!canWrite} className="contents">
			<p
				role="status"
				className="m-0 rounded-[var(--r-sm)] border border-accent/40 bg-accent-soft px-3 py-2 text-xs text-muted"
			>
				<strong className="text-accent">
					{configQuery.data.explicit
						? `Saved settings v${configQuery.data.version}.`
						: "Using server defaults."}
				</strong>{" "}
				Saving adds these settings to your draft for review.
				Running containers are not changed or redeployed automatically.
			</p>

			<Section
				title="Build & start"
				description="Source-relative build inputs captured in the next immutable deployment snapshot."
			>
				<div className="grid gap-3 sm:grid-cols-2">
					<Field label="Builder">
						<select
							value={draft.builder}
							onChange={(event) =>
								update(
									"builder",
									event.currentTarget
										.value as PutServiceEnvironmentConfig["builder"],
								)
							}
							className={selectClass}
						>
							<option value="auto">Auto detect</option>
							<option value="railpack">Railpack</option>
							<option value="dockerfile">Dockerfile</option>
						</select>
					</Field>
					<Field
						label="Root directory"
						hint="Relative to the source checkout; empty means repository root."
					>
						<TextInput
							value={draft.root_directory}
							placeholder="apps/web"
							onChange={(event) =>
								update(
									"root_directory",
									event.currentTarget.value,
								)
							}
						/>
					</Field>
				</div>
				<Field
					label="Dockerfile path"
					hint="Used by Dockerfile or auto builder selection."
				>
					<TextInput
						value={draft.dockerfile_path}
						onChange={(event) =>
							update("dockerfile_path", event.currentTarget.value)
						}
					/>
				</Field>
				<Field
					label="Build command"
					hint="Leave empty for builder defaults."
				>
					<TextInput
						value={draft.build_command}
						placeholder="npm run build"
						onChange={(event) =>
							update("build_command", event.currentTarget.value)
						}
					/>
				</Field>
				<Field
					label="Start command"
					hint="Leave empty for image or builder defaults."
				>
					<TextInput
						value={draft.start_command}
						placeholder="npm start"
						onChange={(event) =>
							update("start_command", event.currentTarget.value)
						}
					/>
				</Field>
				<Field
					label="Pre-deploy command"
					hint="Saved as desired intent. A deploy fails closed if its runtime has no safe one-off job executor."
				>
					<TextInput
						value={draft.predeploy_command}
						placeholder="npm run migrate"
						onChange={(event) =>
							update(
								"predeploy_command",
								event.currentTarget.value,
							)
						}
					/>
				</Field>
			</Section>

			<Section
				title="Runtime"
				description="Container placement and lifecycle intent for a later deployment."
			>
				<Field
					label="Runtime kind"
					hint="Cron jobs run on a schedule and then stop; services and workers keep running."
				>
					<select
						value={draft.runtime_kind}
						onChange={(event) =>
							updateRuntimeKind(
								event.currentTarget
									.value as PutServiceEnvironmentConfig["runtime_kind"],
							)
						}
						className={selectClass}
					>
						<option value="service">Service</option>
						<option value="background">Background worker</option>
						<option value="cron">Cron job</option>
					</select>
				</Field>
				<div className="grid gap-3 sm:grid-cols-2">
					<NumberField
						label="Target port"
						value={draft.target_port}
						min={1}
						max={65535}
						onChange={(value) => update("target_port", value)}
					/>
					<NumberField
						label="Replicas"
						hint="Desired count for stateless services on this host. Services with attached volumes must use one replica."
						value={draft.replicas}
						min={1}
						max={50}
						disabled={draft.runtime_kind === "cron"}
						onChange={(value) => update("replicas", value)}
					/>
				</div>
				<div className="grid gap-3 sm:grid-cols-2">
					<NumberField
						label="Drain grace (seconds)"
						value={draft.drain_grace_seconds}
						min={1}
						max={3600}
						onChange={(value) =>
							update("drain_grace_seconds", value)
						}
					/>
					<Field
						label="Overlap during rollout"
						hint="Keep the prior instance available while the candidate proves ready."
					>
						<label className="flex min-h-9 items-center gap-2 rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 text-sm text-text">
							<input
								type="checkbox"
								checked={draft.overlap_enabled}
								onChange={(event) =>
									update(
										"overlap_enabled",
										event.currentTarget.checked,
									)
								}
							/>
							Enabled
						</label>
					</Field>
				</div>
			</Section>

			{draft.runtime_kind === "cron" ? (
				<Section
					title="Cron schedule"
					description="Five-field UTC schedule, at most one run every five minutes. Each run starts, finishes, and stops."
				>
					<Field label="Schedule (UTC)">
						<TextInput
							value={draft.cron_schedule}
							placeholder="*/5 * * * *"
							onChange={(event) =>
								update(
									"cron_schedule",
									event.currentTarget.value,
								)
							}
						/>
					</Field>
					<div className="grid gap-3 sm:grid-cols-3">
						<NumberField
							label="Run timeout (seconds)"
							value={draft.cron_timeout_seconds}
							min={1}
							max={86400}
							onChange={(value) =>
								update("cron_timeout_seconds", value)
							}
						/>
						<NumberField
							label="Maximum attempts"
							value={draft.cron_max_attempts}
							min={1}
							max={10}
							onChange={(value) =>
								update("cron_max_attempts", value)
							}
						/>
						<NumberField
							label="Retry delay (seconds)"
							value={draft.cron_retry_delay_seconds}
							min={1}
							max={86400}
							onChange={(value) =>
								update("cron_retry_delay_seconds", value)
							}
						/>
					</div>
				</Section>
			) : null}

			<CronOccurrenceHistory
				serviceId={serviceId}
				environmentId={environmentId}
				enabled={
					authority === "operator" && draft.runtime_kind === "cron"
				}
			/>
			{authority === "human" && draft.runtime_kind === "cron" ? (
				<CapabilityBoundary
					state="unavailable"
					title="Cron execution history"
					detail="Your account cannot view past cron runs; that needs a separate permission. You can still save the schedule here. Ask an administrator for access."
				/>
			) : null}

			<Section
				title="Health & restart"
				description="Readiness proof and Docker restart policy."
			>
				<div className="grid gap-3 sm:grid-cols-2">
					<Field label="Health check">
						<select
							value={draft.health_kind}
							disabled={draft.runtime_kind === "cron"}
							onChange={(event) =>
								update(
									"health_kind",
									event.currentTarget
										.value as PutServiceEnvironmentConfig["health_kind"],
								)
							}
							className={selectClass}
						>
							<option value="tcp">TCP connect</option>
							<option value="http">HTTP path</option>
							<option value="none">
								None / background worker
							</option>
						</select>
					</Field>
					<NumberField
						label="Health port"
						hint="Zero uses the target port."
						value={draft.health_port}
						min={0}
						max={65535}
						onChange={(value) => update("health_port", value)}
					/>
				</div>
				{draft.health_kind === "http" ? (
					<Field label="Health path">
						<TextInput
							value={draft.health_path}
							placeholder="/health"
							onChange={(event) =>
								update("health_path", event.currentTarget.value)
							}
						/>
					</Field>
				) : null}
				<div className="grid gap-3 sm:grid-cols-2">
					<NumberField
						label="Health timeout (seconds)"
						value={draft.health_timeout_seconds}
						min={1}
						max={86400}
						onChange={(value) =>
							update("health_timeout_seconds", value)
						}
					/>
					<NumberField
						label="Health interval (seconds)"
						value={draft.health_interval_seconds}
						min={1}
						max={86399}
						onChange={(value) =>
							update("health_interval_seconds", value)
						}
					/>
				</div>
				<div className="grid gap-3 sm:grid-cols-2">
					<Field label="Restart policy">
						<select
							value={draft.restart_policy}
							disabled={draft.runtime_kind === "cron"}
							onChange={(event) =>
								update(
									"restart_policy",
									event.currentTarget
										.value as PutServiceEnvironmentConfig["restart_policy"],
								)
							}
							className={selectClass}
						>
							<option value="unless-stopped">
								Unless stopped
							</option>
							<option value="always">Always</option>
							<option value="on-failure">On failure</option>
							<option value="no">Never</option>
						</select>
					</Field>
					<NumberField
						label="Restart retries"
						hint="Only valid with the on-failure policy."
						value={draft.restart_retries}
						min={0}
						max={1000}
						disabled={draft.restart_policy !== "on-failure"}
						onChange={(value) => update("restart_retries", value)}
					/>
				</div>
			</Section>

			<div className="flex flex-wrap items-center gap-3 rounded-lg border border-line bg-surface px-4 py-3">
				<button
					type="submit"
					disabled={!canWrite || saveConfig.isPending}
					className="inline-flex items-center gap-2 rounded-[var(--r-sm)] border border-accent bg-accent-soft px-3 py-1.5 text-xs font-semibold text-accent transition-colors hover:bg-surface-3 disabled:opacity-60"
				>
					{saveConfig.isPending ? "Saving…" : "Save desired config"}
				</button>
				{savedVersion !== null ? (
					<span role="status" className="text-xs text-accent">
						Saved as version {savedVersion}. Deploy when ready.
					</span>
				) : null}
				{staged ? (
					<p className="m-0 text-xs text-success">Ready in Changes. Review it before applying and deploying.</p>
				) : null}
				{saveError ? (
					<div className="flex flex-wrap items-center gap-2">
						<span role="alert" className="text-xs text-danger">
							{saveError.message}
						</span>
					{saveError.status === 409 || saveError.status === 412 ? (
							<button
								type="button"
								onClick={() => void reloadLatest()}
								className="rounded-[var(--r-sm)] border border-line-strong bg-surface-2 px-2.5 py-1 text-xs font-semibold text-text"
							>
								Reload latest & discard draft
							</button>
						) : null}
					</div>
				) : null}
			</div>
			</fieldset>
		</form>
	);
}

function configToPut(
	config: ServiceEnvironmentConfig,
): PutServiceEnvironmentConfig {
	return {
		expected_version: config.version,
		root_directory: config.root_directory,
		builder: config.builder,
		dockerfile_path: config.dockerfile_path,
		build_command: config.build_command,
		start_command: config.start_command,
		predeploy_command: config.predeploy_command,
		target_port: config.target_port,
		health_kind: config.health_kind,
		health_path: config.health_path,
		health_port: config.health_port,
		health_timeout_seconds: config.health_timeout_seconds,
		health_interval_seconds: config.health_interval_seconds,
		restart_policy: config.restart_policy,
		restart_retries: config.restart_retries,
		replicas: config.replicas,
		overlap_enabled: config.overlap_enabled,
		drain_grace_seconds: config.drain_grace_seconds,
		runtime_kind: config.runtime_kind,
		cron_schedule: config.cron_schedule,
		cron_timeout_seconds: config.cron_timeout_seconds,
		cron_max_attempts: config.cron_max_attempts,
		cron_retry_delay_seconds: config.cron_retry_delay_seconds,
	};
}

const selectClass =
	"w-full rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 text-sm text-text focus:border-accent focus:outline-none";

function NumberField({
	label,
	hint,
	value,
	min,
	max,
	disabled,
	onChange,
}: {
	label: string;
	hint?: string;
	value: number;
	min: number;
	max: number;
	disabled?: boolean;
	onChange: (value: number) => void;
}) {
	return (
		<Field label={label} hint={hint}>
			<TextInput
				type="number"
				value={value}
				min={min}
				max={max}
				disabled={disabled}
				onChange={(event) =>
					onChange(Number(event.currentTarget.value))
				}
			/>
		</Field>
	);
}

// ── Layout primitives ─────────────────────────────────────────────

function Section({
	title,
	description,
	children,
}: {
	title: string;
	description?: string;
	children: ReactNode;
}) {
	return (
		<section className="grid gap-3 rounded-lg border border-line bg-surface px-4 py-4">
			<header className="grid gap-0.5">
				<h3 className="m-0 text-sm font-semibold text-text">{title}</h3>
				{description ? (
					<p className="m-0 text-xs text-muted">{description}</p>
				) : null}
			</header>
			<div className="grid gap-3">{children}</div>
		</section>
	);
}

function IdentityBar({
	serviceId,
	slug,
	environments,
	selectedEnv,
	onSelectEnv,
	envsLoading,
}: {
	serviceId: string;
	slug?: string;
	environments: { id: string; name: string }[];
	selectedEnv?: string;
	onSelectEnv: (id: string) => void;
	envsLoading: boolean;
}) {
	return (
		<div className="flex flex-wrap items-end justify-between gap-3 rounded-lg border border-line bg-surface-2 px-4 py-3">
			<div className="grid gap-0.5">
				<span className="font-mono text-[10px] uppercase tracking-wider text-subtle">
					Service
				</span>
				<span className="font-mono text-sm text-text">
					{slug ?? serviceId}
				</span>
				<span className="font-mono text-[10px] text-subtle">
					{serviceId}
				</span>
			</div>
			<label className="grid gap-1 text-right">
				<span className="font-mono text-[10px] uppercase tracking-wider text-subtle">
					Environment
				</span>
				{envsLoading ? (
					<span className="text-xs text-muted">loading…</span>
				) : environments.length === 0 ? (
					<span className="text-xs text-muted">none</span>
				) : (
					<select
						value={selectedEnv ?? ""}
						onChange={(e) => onSelectEnv(e.target.value)}
						className="rounded-[var(--r-sm)] border border-line bg-surface px-2 py-1 text-xs text-text"
					>
						{environments.map((e) => (
							<option key={e.id} value={e.id}>
								{e.name}
							</option>
						))}
					</select>
				)}
			</label>
		</div>
	);
}

// ── Danger zone ─────────────────────────────────
// Deleting a service is refused while it is still running. The server owns
// that decision; this panel states the rule up front, names exactly what will
// be removed, and reports the server's typed refusal verbatim instead of
// flattening it into "something went wrong".

/** What the delete removes, in the order a user would think of it. */
const DELETED_WITH_SERVICE = [
	"Its environment variables",
	"Its build and runtime settings, in every environment",
	"Its published domain and HTTP route",
	"Its TCP proxy listeners",
	"Its volumes (recoverable for 7 days, then erased)",
	"Its repository link and auto-deploy",
	"Any unapplied changes that name it",
];

/** What survives, so nobody is surprised either way. */
const KEPT_AFTER_DELETE = [
	"Past deployments and their logs, as read-only history",
	"The audit record of this deletion",
];

function serviceDeleteErrorMessage(error: unknown): string {
	const code = error instanceof ApiError ? error.code : undefined;
	const status = error instanceof ApiError ? error.status : undefined;
	switch (code) {
		case "SERVICE_DELETE_RUNNING":
			return "This service is still running. Stop its deployment from the Deployments tab, then delete it.";
		case "SERVICE_DELETE_BLOCKED":
			return "Something still holds this service: a volume that is still attached, an open pull-request preview, or an upload that has not finished. Clear it, then try again.";
		case "AUTHORIZATION_DENIED":
			return "You do not have permission to delete services in this project. Ask a project owner.";
		case "PRECONDITION_FAILED":
		case "PRECONDITION_REQUIRED":
			return "This service changed while the page was open. Reload and try again.";
		case "CAPABILITY_UNAVAILABLE":
			return "This server cannot delete services yet.";
		case "RESOURCE_NOT_FOUND":
			return "This service no longer exists.";
		default:
			break;
	}
	if (status === 401) return "Your session expired. Sign in again.";
	return presentError(error).message;
}

function DangerZone({
	serviceId,
	slug,
	projectId,
	resourceVersion,
	canDelete,
	permissionChecked,
}: {
	serviceId: string;
	slug: string;
	projectId?: string;
	resourceVersion?: number;
	/** True when the current principal holds `service.delete` in this project. */
	canDelete: boolean;
	/** False while the permission answer is still in flight. */
	permissionChecked: boolean;
}) {
	const [confirmation, setConfirmation] = useState("");
	const remove = useDeleteService(projectId);

	const permitted = canDelete;
	const hasVersion = typeof resourceVersion === "number" && resourceVersion > 0;
	const matches = confirmation.trim() === slug;
	const canSubmit = permitted && hasVersion && matches && !remove.isPending;

	if (remove.isSuccess) {
		const removed = remove.data.removed;
		return (
			<section className="grid gap-2 rounded-lg border border-line bg-surface px-4 py-4">
				<h3 className="m-0 text-sm font-semibold text-text">
					Service deleted
				</h3>
				<p className="m-0 text-xs text-muted">
					<span className="font-mono">{remove.data.slug}</span> is gone,
					along with {removed.variables} variable
					{removed.variables === 1 ? "" : "s"}, {removed.volumes} volume
					{removed.volumes === 1 ? "" : "s"}, {removed.published_domains}{" "}
					domain
					{removed.published_domains === 1 ? "" : "s"}, and{" "}
					{removed.environment_configs} environment configuration
					{removed.environment_configs === 1 ? "" : "s"}.{" "}
					{remove.data.retained_deployments} past deployment
					{remove.data.retained_deployments === 1 ? "" : "s"} remain
					readable as history.
				</p>
			</section>
		);
	}

	return (
		<section className="grid gap-3 rounded-lg border border-danger/40 bg-danger-soft px-4 py-4">
			<header className="grid gap-0.5">
				<h3 className="m-0 text-sm font-semibold text-danger">
					Danger zone
				</h3>
				<p className="m-0 text-xs text-muted">
					Irreversible and destructive actions.
				</p>
			</header>

			<div className="grid gap-3 rounded-[var(--r-sm)] border border-line bg-surface px-3 py-3">
				<div className="grid gap-1">
					<span className="text-sm font-medium text-text">
						Delete this service
					</span>
					<p className="m-0 text-xs text-muted">
						The service must be stopped first. Deleting it removes:
					</p>
					<ul className="m-0 grid list-disc gap-0.5 pl-4 text-xs text-muted">
						{DELETED_WITH_SERVICE.map((item) => (
							<li key={item}>{item}</li>
						))}
					</ul>
					<p className="m-0 mt-1 text-xs text-muted">Kept:</p>
					<ul className="m-0 grid list-disc gap-0.5 pl-4 text-xs text-muted">
						{KEPT_AFTER_DELETE.map((item) => (
							<li key={item}>{item}</li>
						))}
					</ul>
				</div>

				{!permitted && permissionChecked ? (
					<p className="m-0 text-xs text-muted">
						Your role in this project cannot delete services. Ask a
						project owner.
					</p>
				) : null}
				{!hasVersion ? (
					<p className="m-0 text-xs text-muted">
						This server does not report a service version, so Midnight
						cannot safely confirm what it would be deleting.
					</p>
				) : null}

				<Field
					label={`Type ${slug} to confirm`}
					htmlFor={`delete-confirm-${serviceId}`}
				>
					<TextInput
						id={`delete-confirm-${serviceId}`}
						value={confirmation}
						autoComplete="off"
						spellCheck={false}
						placeholder={slug}
						disabled={!permitted || !hasVersion || remove.isPending}
						onChange={(event) =>
							setConfirmation(event.currentTarget.value)
						}
					/>
				</Field>

				{remove.error ? (
					<div
						role="alert"
						className="rounded-[var(--r-sm)] border border-danger/40 bg-danger-soft px-3 py-2 text-xs text-danger"
					>
						{serviceDeleteErrorMessage(remove.error)}
					</div>
				) : null}

				<div>
					<Button
						variant="danger"
						disabled={!canSubmit}
						onClick={() => {
							if (!canSubmit || !hasVersion) return;
							remove.mutate({
								serviceId,
								expectedVersion: resourceVersion as number,
							});
						}}
					>
						{remove.isPending
							? "Deleting…"
							: `Delete ${slug} permanently`}
					</Button>
				</div>
			</div>
		</section>
	);
}
