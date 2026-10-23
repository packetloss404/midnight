import { useState, type FormEvent } from "react";
import { presentError } from "@/lib/errorPresentation";
import {
	useCreateMonitor,
	useCreateMonitorChannel,
	useDeleteMonitor,
	useMonitorChannels,
	useMonitorDeliveries,
	useMonitorIncidents,
	useMonitors,
	useRetryMonitorDelivery,
	useSubscribeMonitor,
	type Monitor,
	type MonitorDeliveryStatus,
	type MonitorIncident,
} from "./monitors/api";

export function MonitorPanel({
	projectId,
	environmentId,
	serviceId,
}: {
	projectId: string;
	environmentId?: string;
	serviceId?: string;
}) {
	const monitors = useMonitors(projectId);
	const channels = useMonitorChannels(projectId);
	const create = useCreateMonitor(projectId);
	const remove = useDeleteMonitor(projectId);
	const createChannel = useCreateMonitorChannel(projectId);
	const subscribe = useSubscribeMonitor(projectId);
	const incidents = useMonitorIncidents(projectId, environmentId, serviceId);
	const deliveries = useMonitorDeliveries(
		projectId,
		environmentId,
		serviceId,
	);
	const retryDelivery = useRetryMonitorDelivery(
		projectId,
		environmentId,
		serviceId,
	);
	const [name, setName] = useState("");
	const [signal, setSignal] = useState<Monitor["signal"]>("cpu_percent");
	const [threshold, setThreshold] = useState("80");
	const [endpointVariable, setEndpointVariable] = useState("");
	const [signingVariable, setSigningVariable] = useState("");
	const [channelName, setChannelName] = useState("");
	const [selectedChannels, setSelectedChannels] = useState<
		Record<string, string>
	>({});

	function submitMonitor(event: FormEvent) {
		event.preventDefault();
		if (!environmentId || !serviceId) return;
		create.mutate(
			{
				project_id: projectId,
				environment_id: environmentId,
				service_id: serviceId,
				name: name.trim(),
				signal,
				operator: "gt",
				threshold: Number(threshold),
				window_seconds: 300,
				evaluation_interval_seconds: 60,
				consecutive_failures: 2,
				consecutive_recoveries: 2,
			},
			{ onSuccess: () => setName("") },
		);
	}

	function submitChannel(event: FormEvent) {
		event.preventDefault();
		createChannel.mutate(
			{
				project_id: projectId,
				display_name: channelName.trim(),
				endpoint_variable_id: endpointVariable.trim(),
				signing_variable_id: signingVariable.trim(),
			},
			{
				onSuccess: () => {
					setChannelName("");
					setEndpointVariable("");
					setSigningVariable("");
				},
			},
		);
	}

	const error =
		monitors.error ??
		channels.error ??
		incidents.error ??
		deliveries.error ??
		create.error ??
		remove.error ??
		createChannel.error ??
		subscribe.error ??
		retryDelivery.error;

	return (
		<div className="grid gap-4">
			<p className="m-0 text-xs leading-5 text-muted">
				Monitors check recorded Docker metrics in the background. Alerts,
				recoveries, incidents, retries, and undelivered notifications all
				survive a restart.
			</p>
			{error ? (
				<p
					role="alert"
					className="m-0 rounded border border-danger/40 bg-danger-soft px-3 py-2 text-xs text-danger"
				>
					{presentError(error).message}
				</p>
			) : null}

			<div className="grid gap-2">
				{monitors.isPending ? (
					<p className="m-0 text-xs text-subtle">Loading monitors…</p>
				) : null}
				{(monitors.data ?? []).length === 0 && !monitors.isPending ? (
					<p className="m-0 text-xs text-subtle">
						No metric monitors configured.
					</p>
				) : null}
				{(monitors.data ?? []).map((monitor) => (
					<article
						key={monitor.id}
						className="grid gap-2 rounded border border-line bg-surface-2 px-3 py-3 sm:grid-cols-[1fr_auto]"
					>
						<div>
							<div className="flex flex-wrap items-center gap-2">
								<strong className="text-sm text-text">
									{monitor.name}
								</strong>
								<span
									className={
										"rounded px-1.5 py-0.5 font-mono text-[10px] uppercase " +
										(monitor.state === "firing"
											? "bg-danger-soft text-danger"
											: monitor.state === "healthy"
												? "bg-success-soft text-success"
												: "bg-bg-2 text-subtle")
									}
								>
									{monitor.state}
								</span>
							</div>
							<p className="m-0 mt-1 font-mono text-[11px] text-muted">
								{monitor.signal}{" "}
								{monitor.operator === "gte" ? "≥" : ">"}{" "}
								{monitor.threshold}
								{monitor.last_value === undefined
									? ""
									: ` · last ${monitor.last_value.toFixed(2)}`}
							</p>
						</div>
						<div className="flex flex-wrap items-center gap-2">
							{(channels.data ?? []).length > 0 ? (
								<>
									<select
										aria-label={`Channel for ${monitor.name}`}
										value={
											selectedChannels[monitor.id] ?? ""
										}
										onChange={(event) =>
											setSelectedChannels((current) => ({
												...current,
												[monitor.id]:
													event.target.value,
											}))
										}
										className="rounded border border-line bg-surface px-2 py-1 text-xs text-text"
									>
										<option value="">
											Notify channel…
										</option>
										{(channels.data ?? []).map(
											(channel) => (
												<option
													key={channel.id}
													value={channel.id}
												>
													{channel.display_name}
												</option>
											),
										)}
									</select>
									<button
										type="button"
										disabled={
											!selectedChannels[monitor.id] ||
											subscribe.isPending
										}
										onClick={() =>
											subscribe.mutate({
												monitorId: monitor.id,
												channelId:
													selectedChannels[
														monitor.id
													],
											})
										}
										className="rounded border border-line px-2 py-1 text-xs text-muted hover:text-text disabled:opacity-50"
									>
										Attach
									</button>
								</>
							) : null}
							<button
								type="button"
								disabled={remove.isPending}
								onClick={() => remove.mutate(monitor.id)}
								className="rounded border border-danger/40 px-2 py-1 text-xs text-danger disabled:opacity-50"
							>
								Delete
							</button>
						</div>
					</article>
				))}
			</div>

			<IncidentDeliveryHistory
				incidents={incidents.data ?? []}
				deliveries={deliveries.data ?? []}
				pending={incidents.isPending || deliveries.isPending}
				retrying={retryDelivery.isPending}
				onRetry={(deliveryId) => retryDelivery.mutate(deliveryId)}
			/>

			<form
				onSubmit={submitMonitor}
				className="grid gap-2 rounded border border-line px-3 py-3 sm:grid-cols-4"
			>
				<input
					aria-label="Monitor name"
					required
					value={name}
					onChange={(event) => setName(event.target.value)}
					placeholder="CPU high"
					className="rounded border border-line bg-surface px-2 py-1.5 text-xs text-text"
				/>
				<select
					aria-label="Monitor signal"
					value={signal}
					onChange={(event) =>
						setSignal(event.target.value as Monitor["signal"])
					}
					className="rounded border border-line bg-surface px-2 py-1.5 text-xs text-text"
				>
					<option value="cpu_percent">CPU %</option>
					<option value="memory_percent">Memory %</option>
					<option value="restart_count">Restart count</option>
				</select>
				<input
					aria-label="Monitor threshold"
					required
					min="0"
					type="number"
					value={threshold}
					onChange={(event) => setThreshold(event.target.value)}
					className="rounded border border-line bg-surface px-2 py-1.5 text-xs text-text"
				/>
				<button
					type="submit"
					disabled={!environmentId || !serviceId || create.isPending}
					className="rounded bg-accent px-3 py-1.5 text-xs font-medium text-accent-contrast disabled:opacity-50"
				>
					{create.isPending ? "Creating…" : "Create monitor"}
				</button>
				{!environmentId || !serviceId ? (
					<p className="m-0 text-[11px] text-warning sm:col-span-4">
						Select a concrete environment and service to create a
						monitor.
					</p>
				) : null}
			</form>

			<details className="rounded border border-line px-3 py-2">
				<summary className="cursor-pointer text-xs font-medium text-text">
					Signed webhook channels
				</summary>
				<p className="text-[11px] leading-5 text-muted">
					Supply sealed variable IDs, not plaintext. One variable must
					contain the HTTPS endpoint and the other the HMAC signing
					key; the worker resolves both only for delivery.
				</p>
				<form
					onSubmit={submitChannel}
					className="grid gap-2 sm:grid-cols-4"
				>
					<input
						aria-label="Channel name"
						required
						value={channelName}
						onChange={(event) => setChannelName(event.target.value)}
						placeholder="On-call webhook"
						className="rounded border border-line bg-surface px-2 py-1.5 text-xs text-text"
					/>
					<input
						aria-label="Endpoint variable ID"
						required
						value={endpointVariable}
						onChange={(event) =>
							setEndpointVariable(event.target.value)
						}
						placeholder="var_endpoint"
						className="rounded border border-line bg-surface px-2 py-1.5 font-mono text-xs text-text"
					/>
					<input
						aria-label="Signing variable ID"
						required
						value={signingVariable}
						onChange={(event) =>
							setSigningVariable(event.target.value)
						}
						placeholder="var_signing"
						className="rounded border border-line bg-surface px-2 py-1.5 font-mono text-xs text-text"
					/>
					<button
						type="submit"
						disabled={createChannel.isPending}
						className="rounded border border-line-strong px-3 py-1.5 text-xs text-text disabled:opacity-50"
					>
						Add channel
					</button>
				</form>
			</details>
		</div>
	);
}

function IncidentDeliveryHistory({
	incidents,
	deliveries,
	pending,
	retrying,
	onRetry,
}: {
	incidents: MonitorIncident[];
	deliveries: MonitorDeliveryStatus[];
	pending: boolean;
	retrying: boolean;
	onRetry: (deliveryId: string) => void;
}) {
	return (
		<details open className="rounded border border-line px-3 py-2">
			<summary className="cursor-pointer text-xs font-medium text-text">
				Incidents & notification delivery
			</summary>
			<p className="text-[11px] leading-5 text-muted">
				Metadata only. Notification payloads, webhook destinations,
				variable references, signing material, and raw transport errors
				are never returned to this view.
			</p>
			{pending ? (
				<p className="m-0 text-xs text-subtle">
					Loading incident history…
				</p>
			) : null}
			{!pending && incidents.length === 0 ? (
				<p className="m-0 text-xs text-subtle">
					No incidents have fired in this scope.
				</p>
			) : null}
			{incidents.length > 0 ? (
				<div className="grid gap-2">
					<h4 className="m-0 text-[10px] font-semibold uppercase tracking-wider text-subtle">
						Recent incidents
					</h4>
					{incidents.slice(0, 8).map((incident) => (
						<div
							key={incident.id}
							className="flex flex-wrap items-center justify-between gap-2 rounded bg-surface-2 px-2.5 py-2"
						>
							<div>
								<span className="text-xs font-medium text-text">
									{incident.monitor_name}
								</span>
								<p className="m-0 font-mono text-[10px] text-subtle">
									{incident.id} · opened{" "}
									{formatMonitorTime(incident.opened_at)}
									{incident.resolved_at
										? ` · resolved ${formatMonitorTime(incident.resolved_at)}`
										: ""}
								</p>
							</div>
							<div className="flex items-center gap-2">
								{incident.dead_letter_count > 0 ? (
									<span className="rounded bg-danger-soft px-1.5 py-0.5 text-[10px] text-danger">
										{incident.dead_letter_count} dead
									</span>
								) : null}
								<span
									className={monitorStatusClass(
										incident.state,
									)}
								>
									{incident.state}
								</span>
							</div>
						</div>
					))}
				</div>
			) : null}

			{deliveries.length > 0 ? (
				<div className="mt-3 grid gap-2">
					<h4 className="m-0 text-[10px] font-semibold uppercase tracking-wider text-subtle">
						Notification delivery
					</h4>
					{deliveries.slice(0, 12).map((delivery) => (
						<div
							key={delivery.id}
							className="grid gap-1 rounded bg-surface-2 px-2.5 py-2 sm:grid-cols-[1fr_auto]"
						>
							<div>
								<span className="text-xs text-text">
									{delivery.monitor_name} →{" "}
									{delivery.channel_name}
								</span>
								<p className="m-0 font-mono text-[10px] text-subtle">
									{delivery.event_type} · attempt{" "}
									{delivery.attempts}/{8}
									{delivery.manual_retries > 0
										? ` · ${delivery.manual_retries} manual retries`
										: ""}
									{delivery.response_status
										? ` · HTTP ${delivery.response_status}`
										: ""}
								</p>
								{delivery.error_summary ? (
									<p className="m-0 text-[10px] text-muted">
										{delivery.error_summary}
									</p>
								) : null}
							</div>
							<div className="flex items-center gap-2">
								<span
									className={monitorStatusClass(
										delivery.state,
									)}
								>
									{delivery.state}
								</span>
								{delivery.state === "dead" ? (
									<button
										type="button"
										disabled={retrying}
										onClick={() => onRetry(delivery.id)}
										className="rounded border border-danger/40 px-2 py-1 text-[10px] text-danger disabled:opacity-50"
									>
										Retry
									</button>
								) : null}
							</div>
						</div>
					))}
				</div>
			) : null}
		</details>
	);
}

function monitorStatusClass(state: string): string {
	const tone =
		state === "dead" || state === "open"
			? "bg-danger-soft text-danger"
			: state === "delivered" || state === "resolved"
				? "bg-success-soft text-success"
				: "bg-bg-2 text-subtle";
	return `rounded px-1.5 py-0.5 font-mono text-[10px] uppercase ${tone}`;
}

function formatMonitorTime(value: string): string {
	const parsed = Date.parse(value);
	return Number.isNaN(parsed) ? value : new Date(parsed).toLocaleString();
}
