import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/api/hooks", () => ({
	useEnvironmentChangeset: () => ({ data: { changeset: null } }),
}));

vi.mock("./panels/ServiceSettings", () => ({
	ServiceSettings: ({
		authority,
		mode,
		serviceId,
		environmentId,
	}: {
		authority: "human" | "operator";
		mode: string;
		serviceId: string;
		environmentId?: string;
	}) => (
		<div
			data-testid={`service-settings-${mode}`}
			data-authority={authority}
		>
			{serviceId}:{environmentId}
			{mode === "runtime" ? (
				<label>
					Health check
					<select defaultValue="tcp">
						<option value="tcp">TCP</option>
					</select>
				</label>
			) : null}
		</div>
	),
}));

vi.mock("./panels/VariablesEditor", () => ({
	VariablesEditor: () => <div data-testid="variables" />,
}));

vi.mock("./panels/variables/TenantVariablesEditor", () => ({
	TenantVariablesEditor: ({
		projectId,
		serviceId,
		environmentId,
	}: {
		projectId: string;
		serviceId: string;
		environmentId: string;
	}) => (
		<div data-testid="tenant-variables">
			{projectId}:{serviceId}:{environmentId}
		</div>
	),
}));

vi.mock("./panels/VolumeRecoveryPanel", () => ({
	VolumeRecoveryPanel: () => <div data-testid="storage" />,
}));

vi.mock("./panels/github/ServiceSourcePanel", () => ({
	ServiceSourcePanel: ({
		projectId,
		serviceId,
		environmentId,
	}: {
		projectId: string;
		serviceId: string;
		environmentId?: string;
	}) => (
		<div data-testid="service-source-panel" data-environment={environmentId}>
			{projectId}:{serviceId}
			<button type="button">Connect source</button>
		</div>
	),
}));

import {
	ConfigurePanel,
	defaultConfigureSection,
	HumanServiceConfigure,
	HumanServiceSourceConfigure,
	rememberConfigureSection,
	serviceDetailAuthority,
} from "./ServiceDetailDrawer";

describe("ServiceDetailDrawer configure integration", () => {
	it("keeps administrator human variable and runtime edits on tenant authority", () => {
		const props = { serviceId: "svc_1", projectId: "prj_1", environmentId: "env_1", authority: "human" as const };
		const view = render(<ConfigurePanel {...props} section="variables" />);
		expect(screen.getByTestId("tenant-variables")).toHaveTextContent("prj_1:svc_1:env_1");
		expect(screen.queryByTestId("variables")).not.toBeInTheDocument();
		view.rerender(<ConfigurePanel {...props} section="runtime" />);
		expect(screen.getByTestId("service-settings-runtime")).toHaveAttribute("data-authority", "human");
		view.rerender(<ConfigurePanel {...props} section="source" />);
		expect(screen.getByTestId("service-source-panel")).toHaveTextContent("prj_1:svc_1");
		expect(screen.getByTestId("service-source-panel")).toHaveAttribute("data-environment", "env_1");
		view.rerender(<ConfigurePanel serviceId="svc_1" authority="human" section="variables" />);
		expect(screen.queryByTestId("variables")).not.toBeInTheDocument();
		expect(screen.getByText(/A concrete project and environment are required/)).toBeVisible();
	});

	it("mounts real settings modes for independently routable networking and runtime sections", () => {
		const props = {
			serviceId: "svc_1",
			projectId: "prj_1",
			environmentId: "env_1",
			onSectionChange: vi.fn(),
		};
		const { rerender } = render(
			<ConfigurePanel {...props} section="networking" />,
		);
		expect(
			screen.getByTestId("service-settings-networking"),
		).toHaveTextContent("svc_1:env_1");
		expect(
			screen.getByTestId("service-settings-networking"),
		).toHaveAttribute("data-authority", "operator");
		expect(
			screen.queryByTestId("service-settings-runtime"),
		).not.toBeInTheDocument();

		rerender(<ConfigurePanel {...props} section="runtime" />);
		expect(
			screen.getByTestId("service-settings-runtime"),
		).toHaveTextContent("svc_1:env_1");
		expect(
			screen.getByTestId("service-settings-runtime"),
		).toHaveAttribute("data-authority", "operator");
		expect(
			screen.queryByTestId("service-settings-networking"),
		).not.toBeInTheDocument();
	});

	it("keeps every tenant configuration area independently routable", async () => {
		const user = userEvent.setup();
		const onSectionChange = vi.fn();
		const view = render(
			<HumanServiceConfigure
				projectId="prj_1"
				serviceId="svc_1"
				environmentId="env_1"
				section="variables"
				onSectionChange={onSectionChange}
			/>,
		);

		expect(screen.getByTestId("tenant-variables")).toHaveTextContent(
			"prj_1:svc_1:env_1",
		);
		expect(screen.queryByTestId("service-source-panel")).not.toBeInTheDocument();
		expect(screen.queryByTestId("service-settings-runtime")).not.toBeInTheDocument();
		expect(
			screen.queryByTestId("service-settings-networking"),
		).not.toBeInTheDocument();

		await user.click(screen.getByRole("tab", { name: "Networking" }));
		expect(onSectionChange).toHaveBeenCalledWith("networking");

		view.rerender(
			<HumanServiceConfigure
				projectId="prj_1"
				serviceId="svc_1"
				environmentId="env_1"
				section="networking"
				onSectionChange={onSectionChange}
			/>,
		);
		expect(
			screen.getByTestId("service-settings-networking"),
		).toHaveAttribute("data-authority", "human");
		expect(screen.queryByTestId("service-source-panel")).not.toBeInTheDocument();
		expect(screen.queryByTestId("tenant-variables")).not.toBeInTheDocument();

		view.rerender(
			<HumanServiceConfigure
				projectId="prj_1"
				serviceId="svc_1"
				environmentId="env_1"
				section="source"
				onSectionChange={onSectionChange}
			/>,
		);
		expect(screen.getByTestId("service-source-panel")).toHaveTextContent(
			"prj_1:svc_1",
		);

		view.rerender(
			<HumanServiceConfigure
				projectId="prj_1"
				serviceId="svc_1"
				environmentId="env_1"
				section="runtime"
				onSectionChange={onSectionChange}
			/>,
		);
		expect(screen.getByTestId("service-settings-runtime")).toHaveAttribute(
			"data-authority",
			"human",
		);

		view.rerender(
			<HumanServiceConfigure
				projectId="prj_1"
				serviceId="svc_1"
				environmentId="env_1"
				section="storage"
				onSectionChange={onSectionChange}
			/>,
		);
		expect(screen.getByTestId("storage")).toBeInTheDocument();
	});

	it("explains the workflow persistently and supports arrow-key section navigation", async () => {
		const user = userEvent.setup();
		const onSectionChange = vi.fn();
		render(
			<HumanServiceConfigure
				projectId="prj_1"
				serviceId="svc_1"
				environmentId="env_1"
				section="source"
				onSectionChange={onSectionChange}
			/>,
		);

		expect(
			screen.getByRole("region", { name: "Configuration workflow" }),
		).toHaveTextContent(
			"Edit→Review changes→Apply & deploy",
		);
		const source = screen.getByRole("tab", { name: "Source" });
		source.focus();
		await user.keyboard("{ArrowRight}");
		expect(onSectionChange).toHaveBeenCalledWith("variables");
	});

	it("finds settings by plain-language aliases and routes to the owning control", async () => {
		const user = userEvent.setup();
		const onSectionChange = vi.fn();
		const view = render(
			<HumanServiceConfigure
				projectId="prj_1"
				serviceId="svc_1"
				environmentId="env_1"
				section="variables"
				onSectionChange={onSectionChange}
			/>,
		);

		const search = screen.getByRole("combobox", { name: "Search settings" });
		await user.type(search, "readiness probe");
		const result = screen.getByRole("option", { name: /Health check/i });
		expect(result).toHaveTextContent("Runtime");
		await user.click(result);
		expect(onSectionChange).toHaveBeenCalledWith("runtime");
		view.rerender(
			<HumanServiceConfigure
				projectId="prj_1"
				serviceId="svc_1"
				environmentId="env_1"
				section="runtime"
				onSectionChange={onSectionChange}
			/>,
		);
		await waitFor(() =>
			expect(screen.getByRole("combobox", { name: "Health check" })).toHaveFocus(),
		);
		expect(screen.getByText("Focused Health check.")).toBeInTheDocument();
	});

	it("focuses action controls whose accessible name differs from the catalog label", async () => {
		const user = userEvent.setup();
		render(
			<HumanServiceConfigure
				projectId="prj_1"
				serviceId="svc_1"
				environmentId="env_1"
				section="source"
				onSectionChange={vi.fn()}
			/>,
		);

		await user.type(screen.getByRole("combobox", { name: "Search settings" }), "repository");
		await user.click(screen.getByRole("option", { name: /GitHub repository/i }));
		await waitFor(() =>
			expect(screen.getByRole("button", { name: "Connect source" })).toHaveFocus(),
		);
		expect(screen.getByText("Focused GitHub repository.")).toBeInTheDocument();
	});

	it("defaults source-free services first, then remembers a section per environment", () => {
		window.localStorage.clear();
		expect(
			defaultConfigureSection({
				serviceId: "svc_1",
				environmentId: "env_1",
				hasSource: false,
			}),
		).toBe("source");

		rememberConfigureSection("svc_1", "env_1", "runtime");
		expect(
			defaultConfigureSection({
				serviceId: "svc_1",
				environmentId: "env_1",
				hasSource: true,
			}),
		).toBe("runtime");
		expect(
			defaultConfigureSection({
				requested: "networking",
				serviceId: "svc_1",
				environmentId: "env_1",
				hasSource: false,
			}),
		).toBe("networking");
	});

	it("mounts the tenant source panel only with exact project and service scope", () => {
		const { rerender } = render(
			<HumanServiceSourceConfigure
				projectId="prj_1"
				serviceId="svc_1"
			/>,
		);
		expect(screen.getByTestId("service-source-panel")).toHaveTextContent(
			"prj_1:svc_1",
		);

		rerender(<HumanServiceSourceConfigure serviceId="svc_1" />);
		expect(
			screen.queryByTestId("service-source-panel"),
		).not.toBeInTheDocument();
		expect(screen.getByText(/project scope is required/i)).toBeInTheDocument();
		expect(
			screen.getByText(/no global installation lookup was sent/i),
		).toBeInTheDocument();
	});

	it("uses installation capability independently from credential kind and resource admission", () => {
		expect(serviceDetailAuthority("human", true)).toEqual({
			humanSession: true,
			operatorEvidenceAvailable: false,
			operatorMutationAvailable: false,
		});
		expect(serviceDetailAuthority("human", true, false)).toEqual({
			humanSession: true,
			operatorEvidenceAvailable: false,
			operatorMutationAvailable: false,
		});
		expect(serviceDetailAuthority("human", true, true, false)).toEqual({
			humanSession: true,
			operatorEvidenceAvailable: true,
			operatorMutationAvailable: false,
		});
		expect(serviceDetailAuthority("human", true, true, true)).toEqual({
			humanSession: true,
			operatorEvidenceAvailable: true,
			operatorMutationAvailable: true,
		});
		expect(serviceDetailAuthority("operator", true)).toEqual({
			humanSession: false,
			operatorEvidenceAvailable: true,
			operatorMutationAvailable: true,
		});
	});
});
