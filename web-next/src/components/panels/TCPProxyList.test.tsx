import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const tcpAPI = vi.hoisted(() => ({
	create: vi.fn(),
	withdraw: vi.fn(),
	records: [] as Array<Record<string, unknown>>,
	withdrawPending: false,
	withdrawingID: undefined as string | undefined,
}));

vi.mock("./tcp/api", () => ({
	useTCPProxies: () => ({
		isPending: false,
		error: null,
		data: tcpAPI.records,
		refetch: vi.fn(),
	}),
	useCreateTCPProxy: () => ({
		mutate: tcpAPI.create,
		isPending: false,
		error: null,
	}),
	useWithdrawTCPProxy: () => ({
		mutate: tcpAPI.withdraw,
		isPending: tcpAPI.withdrawPending,
		variables: tcpAPI.withdrawingID,
		error: null,
	}),
}));

import { TCPProxyList } from "./TCPProxyList";

beforeEach(() => {
	vi.clearAllMocks();
	tcpAPI.records = [];
	tcpAPI.withdrawPending = false;
	tcpAPI.withdrawingID = undefined;
});

afterEach(() => vi.unstubAllGlobals());

describe("TCPProxyList", () => {
	it("defaults to Railway-like public exposure and labels post-NAT restrictions honestly", async () => {
		const user = userEvent.setup();
		render(
			<TCPProxyList
				projectId="prj_1"
				environmentId="env_1"
				serviceId="svc_1"
				defaultBackendPort={5432}
			/>,
		);

		expect(
			screen.getByText(/without a restriction this is a public listener/i),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Observed peer CIDRs")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Expose TCP" }));
		expect(tcpAPI.create).toHaveBeenLastCalledWith({
			projectId: "prj_1",
			environmentId: "env_1",
			serviceId: "svc_1",
			backendPort: 5432,
			allowedSourceCIDRs: [],
		});

		await user.click(
			screen.getByRole("checkbox", {
				name: "Restrict observed peer CIDRs",
			}),
		);
		const observedPeers = screen.getByLabelText("Observed peer CIDRs");
		await user.type(observedPeers, "192.168.65.1/32");
		await user.click(screen.getByRole("button", { name: "Expose TCP" }));
		expect(tcpAPI.create).toHaveBeenLastCalledWith({
			projectId: "prj_1",
			environmentId: "env_1",
			serviceId: "svc_1",
			backendPort: 5432,
			allowedSourceCIDRs: ["192.168.65.1/32"],
		});
	});

	it("requires an explicit internal port when no durable target is available", async () => {
		const user = userEvent.setup();
		render(
			<TCPProxyList
				projectId="prj_1"
				environmentId="env_1"
				serviceId="svc_1"
			/>,
		);
		const expose = screen.getByRole("button", { name: "Expose TCP" });
		expect(expose).toBeDisabled();
		await user.type(screen.getByLabelText("TCP backend port"), "8080");
		expect(expose).toBeEnabled();
		await user.click(expose);
		expect(tcpAPI.create).toHaveBeenCalledWith(
			expect.objectContaining({ backendPort: 8080 }),
		);
	});

	it("confirms withdrawal and disables it while the request is pending", async () => {
		tcpAPI.records = [
			{
				id: "tcp_1",
				public_address: "db.tcp.example.test:20000",
				apply_state: "active",
				desired_enabled: true,
				listener_bound: true,
				backend_reachable: true,
				public_reachability_verified: false,
				backend_port: 5432,
				allowed_source_cidrs: [],
			},
		];
		const confirm = vi.fn(() => true);
		vi.stubGlobal("confirm", confirm);
		const user = userEvent.setup();
		const { rerender } = render(
			<TCPProxyList
				projectId="prj_1"
				environmentId="env_1"
				serviceId="svc_1"
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Withdraw" }));
		expect(confirm).toHaveBeenCalledTimes(1);
		expect(tcpAPI.withdraw).toHaveBeenCalledWith("tcp_1");

		tcpAPI.withdrawPending = true;
		tcpAPI.withdrawingID = "tcp_1";
		rerender(
			<TCPProxyList
				projectId="prj_1"
				environmentId="env_1"
				serviceId="svc_1"
			/>,
		);
		expect(
			screen.getByRole("button", { name: "Withdrawing…" }),
		).toBeDisabled();
	});
});
