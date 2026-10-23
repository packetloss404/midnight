import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/api/hooks", () => ({
  useDeployments: () => ({
    data: [],
    error: null,
    isPending: false,
    refetch: vi.fn(),
  }),
}));

vi.mock("./previews/PreviewEnvironmentCapability", () => ({
  PreviewEnvironmentCapability: ({ serviceId }: { serviceId: string }) => (
    <div data-testid="preview-capability">{serviceId}</div>
  ),
}));

import { DeploymentList } from "./DeploymentList";

describe("DeploymentList capability evidence", () => {
  it("keeps preview setup status visible before the first deployment exists", () => {
    render(<DeploymentList serviceId="svc_one" />);

    expect(screen.getByTestId("preview-capability")).toHaveTextContent("svc_one");
    expect(screen.getByText("No deployments yet")).toBeInTheDocument();
  });
});
