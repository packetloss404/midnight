import { useRef, useState, type FormEvent } from "react";
import type { Environment } from "@/api/client";
import { useProjectAccess } from "@/components/panels/automation-tokens/api";
import {
	Button,
	Field,
	InlineError,
	TextInput,
} from "@/components/panels/form-kit";
import { ErrorState, Loading } from "@/components/states";
import { presentError } from "@/lib/errorPresentation";
import {
	createEnvironmentMetadataIntentKey,
	environmentMetadataTier,
	ENVIRONMENT_METADATA_TIERS,
	updateEnvironmentMetadata,
	type EnvironmentMetadataTier,
} from "./api";

const ENVIRONMENT_WRITE_PERMISSION = "environment.write";

type EnvironmentMetadataIntent = {
	fingerprint: string;
	idempotencyKey: string;
};

type EnvironmentMetadataDraft = {
	name: string;
	slug: string;
	tier?: EnvironmentMetadataTier;
};

export function EnvironmentMetadataEditor({
	projectId,
	environment,
	onCancel,
	onReloadLatest,
	onSaved,
}: {
	projectId: string;
	environment: Environment;
	onCancel: () => void;
	onReloadLatest: () => Promise<Environment | undefined>;
	onSaved: (environment: Environment) => Promise<void>;
}) {
	const access = useProjectAccess(projectId, environment.id, true);
	const [baseline, setBaseline] = useState(environment);
	const [draft, setDraft] = useState<EnvironmentMetadataDraft>({
		name: environment.name,
		slug: environment.slug,
		tier: environmentMetadataTier(environment.tier),
	});
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const [savedVersion, setSavedVersion] = useState<number>();
	const retryIntent = useRef<EnvironmentMetadataIntent>();
	const exactEnvironmentAccess =
		access.data?.principal_kind === "human" &&
		access.data.project_id === projectId &&
		access.data.environment_id === environment.id;
	const canEdit =
		!access.isPending &&
		!access.error &&
		exactEnvironmentAccess &&
		access.data?.permissions.includes(ENVIRONMENT_WRITE_PERMISSION) ===
			true;
	const normalizedName = draft.name.trim();
	const normalizedSlug = draft.slug.trim();
	const nameId = `${environment.id}-metadata-name`;
	const nameLabelId = `${nameId}-label`;
	const slugId = `${environment.id}-metadata-slug`;
	const slugLabelId = `${slugId}-label`;
	const slugHintId = `${slugId}-hint`;
	const tierId = `${environment.id}-metadata-tier`;
	const tierLabelId = `${tierId}-label`;
	const tierHintId = `${tierId}-hint`;
	const unchanged =
		normalizedName === baseline.name &&
		normalizedSlug === baseline.slug &&
		draft.tier === baseline.tier;

	async function submit(event: FormEvent) {
		event.preventDefault();
		setError(null);
		setSavedVersion(undefined);
		if (!canEdit) {
			setError(
				"Midnight could not verify environment-write permission for this exact environment.",
			);
			return;
		}
		if (!draft.tier) {
			setError("Midnight returned an unsupported environment tier.");
			return;
		}
		if (unchanged) {
			setError("Change the name, slug, or tier before saving.");
			return;
		}
		const fingerprint = JSON.stringify({
			projectId,
			environmentId: baseline.id,
			expectedVersion: baseline.resource_version,
			name: normalizedName,
			slug: normalizedSlug,
			tier: draft.tier,
		});
		if (retryIntent.current?.fingerprint !== fingerprint) {
			retryIntent.current = {
				fingerprint,
				idempotencyKey: createEnvironmentMetadataIntentKey(),
			};
		}

		setBusy(true);
		try {
			const saved = await updateEnvironmentMetadata({
				projectId,
				environmentId: baseline.id,
				name: normalizedName,
				slug: normalizedSlug,
				tier: draft.tier,
				expectedVersion: baseline.resource_version,
				expectedIsProtected: baseline.is_protected,
				idempotencyKey: retryIntent.current.idempotencyKey,
			});
			setBaseline(saved);
			setDraft({
				name: saved.name,
				slug: saved.slug,
				tier: environmentMetadataTier(saved.tier),
			});
			setSavedVersion(saved.resource_version);
			retryIntent.current = undefined;
			await onSaved(saved);
		} catch (err) {
			setError(err);
		} finally {
			setBusy(false);
		}
	}

	async function reloadLatest() {
		setBusy(true);
		setError(null);
		try {
			const latest = await onReloadLatest();
			if (!latest) {
				throw new Error(
					"The environment is no longer visible in this project.",
				);
			}
			setBaseline(latest);
			setDraft({
				name: latest.name,
				slug: latest.slug,
				tier: environmentMetadataTier(latest.tier),
			});
			setSavedVersion(undefined);
			retryIntent.current = undefined;
		} catch (err) {
			setError(err);
		} finally {
			setBusy(false);
		}
	}

	if (access.isPending) {
		return <Loading label="Checking exact environment access…" />;
	}
	if (access.error || !exactEnvironmentAccess) {
		return (
			<div className="grid gap-2">
				<ErrorState
					title="Environment editing unavailable"
					error={
						access.error ??
						new Error(
							"Midnight could not verify the exact project and environment scope.",
						)
					}
					onRetry={() => void access.refetch()}
				/>
				<div>
					<Button variant="secondary" onClick={onCancel}>
						Cancel
					</Button>
				</div>
			</div>
		);
	}
	if (!canEdit) {
		return (
			<div className="grid gap-2">
				<p
					role="note"
					className="m-0 rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 text-xs text-muted"
				>
					This exact environment is read-only for your current project
					role. No metadata request was sent.
				</p>
				<div>
					<Button variant="secondary" onClick={onCancel}>
						Cancel
					</Button>
				</div>
			</div>
		);
	}

	const presentedError = error ? presentError(error) : undefined;
	return (
		<form
			className="grid gap-3 border-t border-line pt-3"
			onSubmit={(event) => void submit(event)}
		>
			<div className="grid gap-3 sm:grid-cols-2">
				<Field label="Name" htmlFor={nameId} labelId={nameLabelId}>
					<TextInput
						id={nameId}
						aria-labelledby={nameLabelId}
						value={draft.name}
						maxLength={200}
						autoFocus
						onChange={(event) => {
							const value = event.currentTarget.value;
							setDraft((current) => ({
								...current,
								name: value,
							}));
						}}
					/>
				</Field>
				<Field
					label="Slug"
					htmlFor={slugId}
					labelId={slugLabelId}
					hint="Lowercase letters, numbers, and hyphens; changing it does not redeploy services."
					hintId={slugHintId}
				>
					<TextInput
						id={slugId}
						aria-labelledby={slugLabelId}
						aria-describedby={slugHintId}
						value={draft.slug}
						maxLength={63}
						onChange={(event) => {
							const value = event.currentTarget.value;
							setDraft((current) => ({
								...current,
								slug: value,
							}));
						}}
					/>
				</Field>
			</div>
			<Field
				label="Tier"
				htmlFor={tierId}
				labelId={tierLabelId}
				hint="Organizational metadata only; changing tier does not promote or copy an environment."
				hintId={tierHintId}
			>
				<select
					id={tierId}
					aria-labelledby={tierLabelId}
					aria-describedby={tierHintId}
					value={draft.tier ?? ""}
					onChange={(event) => {
						const value = event.currentTarget.value;
						setDraft((current) => ({
							...current,
							tier: environmentMetadataTier(value),
						}));
					}}
					className="w-full rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 text-sm text-text focus:border-accent focus:outline-none"
				>
					{!draft.tier ? (
						<option value="">Unsupported tier</option>
					) : null}
					{ENVIRONMENT_METADATA_TIERS.map((tier) => (
						<option key={tier} value={tier}>
							{tier}
						</option>
					))}
				</select>
			</Field>
			<p
				role="note"
				className="m-0 rounded-[var(--r-sm)] border border-line bg-surface px-3 py-2 text-xs text-muted"
			>
				Protection remains{" "}
				{baseline.is_protected ? "enabled" : "disabled"} and cannot be
				changed here. Saving updates metadata only; it does not deploy,
				restart, promote, duplicate, or copy configuration or data.
			</p>
			<div className="flex flex-wrap items-center gap-2">
				<Button
					type="submit"
					disabled={busy || unchanged || !draft.tier}
				>
					{busy ? "Saving…" : "Save environment metadata"}
				</Button>
				<Button variant="secondary" onClick={onCancel} disabled={busy}>
					Cancel
				</Button>
				{savedVersion !== undefined ? (
					<span role="status" className="text-xs text-accent">
						Saved metadata version {savedVersion}. No deployment
						started.
					</span>
				) : null}
			</div>
			{error ? (
				<div className="grid gap-2">
					<InlineError error={error} />
					{presentedError?.status === 409 ? (
						<div>
							<Button
								variant="secondary"
								onClick={() => void reloadLatest()}
								disabled={busy}
							>
								Reload latest &amp; discard draft
							</Button>
						</div>
					) : null}
				</div>
			) : null}
		</form>
	);
}
