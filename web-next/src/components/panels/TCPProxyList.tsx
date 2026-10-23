import { useState, type FormEvent } from "react";
import { StatusPill } from "@/components/StatusPill";
import { ErrorState, Loading } from "@/components/states";
import {
	useCreateTCPProxy,
	useTCPProxies,
	useWithdrawTCPProxy,
	type TCPProxyRecord,
} from "./tcp/api";

export type TCPProxyListProps = {
	projectId?: string;
	environmentId?: string;
	serviceId: string;
	defaultBackendPort?: number;
};

export function TCPProxyList({
	projectId,
	environmentId,
	serviceId,
	defaultBackendPort,
}: TCPProxyListProps) {
	const query = useTCPProxies(projectId, environmentId, serviceId);
	const create = useCreateTCPProxy();
	const withdraw = useWithdrawTCPProxy(
		projectId ?? "",
		environmentId ?? "",
		serviceId,
	);
	const [backendPort, setBackendPort] = useState<number | "">(
		defaultBackendPort ?? "",
	);
	const [restrictPeers, setRestrictPeers] = useState(false);
	const [allowlist, setAllowlist] = useState("");
	const validBackendPort =
		typeof backendPort === "number" &&
		Number.isInteger(backendPort) &&
		backendPort >= 1 &&
		backendPort <= 65535;

	if (!projectId || !environmentId) {
		return (
			<Notice>
				Select a concrete project environment to manage TCP exposure.
			</Notice>
		);
	}
	if (query.isPending) return <Loading label="Loading TCP endpoints…" />;
	if (query.error) {
		return (
			<ErrorState
				title="Failed to load TCP endpoints"
				error={query.error}
				onRetry={() => query.refetch()}
			/>
		);
	}

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (
			!validBackendPort ||
			typeof backendPort !== "number"
		) {
			return;
		}
		const allowedSourceCIDRs = allowlist
			.split(/[\s,]+/)
			.map((value) => value.trim())
			.filter(Boolean);
		if (restrictPeers && allowedSourceCIDRs.length === 0) return;
		create.mutate({
			projectId,
			environmentId,
			serviceId,
			backendPort,
			allowedSourceCIDRs: restrictPeers ? allowedSourceCIDRs : [],
		});
	};

	return (
		<div className="grid gap-3 border-t border-line pt-4">
			<header className="grid gap-0.5">
				<h3 className="m-0 text-sm font-semibold text-text">
					TCP proxy
				</h3>
				<p className="m-0 text-xs text-muted">
					Bind a fixed port on this host to the service's ready
					replicas. Midnight resolves private destinations
					server-side; DNS and firewall/NAT remain operator-managed.
				</p>
			</header>
			<form
				onSubmit={submit}
				className="grid gap-2"
			>
				<div className="grid gap-2 md:grid-cols-[8rem_minmax(0,1fr)_auto]">
					<input
						className="field-input"
						type="number"
						min={1}
						max={65535}
						value={backendPort}
						onChange={(event) => {
							const value = event.currentTarget.value;
							setBackendPort(value === "" ? "" : Number(value));
						}}
						placeholder="Internal port"
						aria-label="TCP backend port"
					/>
					<div className="flex items-center">
						<label className="flex items-center gap-2 text-xs text-muted">
							<input
								type="checkbox"
								checked={restrictPeers}
								onChange={(event) =>
									setRestrictPeers(event.currentTarget.checked)
								}
							/>
							Restrict observed peer CIDRs
						</label>
					</div>
					<button
						type="submit"
						disabled={
							create.isPending ||
							!validBackendPort ||
							(restrictPeers && !allowlist.trim())
						}
						className="rounded-[var(--r-sm)] bg-accent px-3 py-2 text-xs font-semibold text-white disabled:opacity-50"
					>
						{create.isPending ? "Allocating…" : "Expose TCP"}
					</button>
				</div>
				{restrictPeers ? (
					<input
						className="field-input"
						value={allowlist}
						onChange={(event) =>
							setAllowlist(event.currentTarget.value)
						}
						placeholder="Observed peer CIDRs"
						aria-label="Observed peer CIDRs"
					/>
				) : null}
			</form>
			<p className="m-0 text-[11px] text-muted">
				Without a restriction this is a public listener. Docker NAT may
				present its bridge gateway instead of the original client IP;
				restrictions match the address Midnight actually observes.
				Confirm the service&apos;s internal port before exposing it.
			</p>
			{create.error ? <Notice>{create.error.message}</Notice> : null}
			{withdraw.error ? <Notice>{withdraw.error.message}</Notice> : null}
			<TCPProxyRows
				records={query.data ?? []}
				withdrawPending={withdraw.isPending}
				withdrawingID={withdraw.variables}
				onWithdraw={(id) => {
					if (
						window.confirm(
							"Withdraw this TCP endpoint? New connections will stop after the controller reconciles.",
						)
					) {
						withdraw.mutate(id);
					}
				}}
			/>
		</div>
	);
}

function TCPProxyRows({
	records,
	onWithdraw,
	withdrawPending,
	withdrawingID,
}: {
	records: TCPProxyRecord[];
	onWithdraw: (id: string) => void;
	withdrawPending: boolean;
	withdrawingID?: string;
}) {
	if (records.length === 0) {
		return (
			<p className="m-0 rounded-lg border border-dashed border-line bg-bg-2 px-4 py-3 text-xs text-muted">
				No TCP endpoint has been allocated.
			</p>
		);
	}
	return (
		<ul className="m-0 grid list-none gap-2 p-0">
			{records.map((record) => (
				<li
					key={record.id}
					className="grid gap-2 rounded-lg border border-line bg-surface-2 px-4 py-3"
				>
					<div className="flex flex-wrap items-center justify-between gap-2">
						<code className="text-sm text-text">
							{record.public_address}
						</code>
						<div className="flex items-center gap-2">
							<StatusPill
								status={
									record.apply_state === "active"
										? "active"
										: record.apply_state === "failed"
											? "error"
											: record.apply_state
								}
							/>
							{record.desired_enabled ? (
								<button
									type="button"
									disabled={withdrawPending}
									onClick={() => onWithdraw(record.id)}
									className="text-[11px] text-danger hover:underline disabled:opacity-50"
								>
									{withdrawingID === record.id
										? "Withdrawing…"
										: "Withdraw"}
								</button>
							) : null}
						</div>
					</div>
					<p className="m-0 text-xs text-muted">
						Listener {record.listener_bound ? "bound" : "not bound"} ·
						backend{" "}
						{record.backend_reachable
							? "reachable"
							: "not verified"}{" "}
						· public reachability{" "}
						{record.public_reachability_verified
							? "verified"
							: "unverified"}
					</p>
					<p className="m-0 text-[11px] text-muted">
						active deployment port {record.backend_port} · observed peers{" "}
						{record.allowed_source_cidrs.length > 0
							? record.allowed_source_cidrs.join(", ")
							: "unrestricted"}
					</p>
					{record.error_detail ? (
						<p className="m-0 text-xs text-danger">
							{record.error_code
								? `${record.error_code}: `
								: ""}
							{record.error_detail}
						</p>
					) : null}
				</li>
			))}
		</ul>
	);
}

function Notice({ children }: { children: React.ReactNode }) {
	return (
		<p
			role="status"
			className="m-0 rounded-[var(--r-sm)] border border-warn/40 bg-warn-soft px-3 py-2 text-xs text-muted"
		>
			{children}
		</p>
	);
}
