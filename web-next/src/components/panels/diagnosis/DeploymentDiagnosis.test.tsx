import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { DeploymentResource } from "@/api/client";
const state = vi.hoisted(() => ({ principal: "usr_1", load: vi.fn() }));
vi.mock("@/auth/AuthProvider", () => ({ useAuth: () => ({ state: { status: "human", me: { principal: { user_id: state.principal, session_id: "session_1" } } } }) }));
vi.mock("./api", () => ({ loadDiagnosis: state.load }));
import { DeploymentDiagnosis } from "./DeploymentDiagnosis";
const deployment = { id: "dpl_1", deployment_id: "dpl_1", operation_id: "op_1", project_id: "prj_1", service_id: "svc_1", environment_id: "env_1", operation_state: "FAILED" } as DeploymentResource;
afterEach(() => { cleanup(); vi.useRealTimers(); state.load.mockReset(); state.principal = "usr_1"; });
describe("deployment diagnosis", () => {
  it("finds an attachment after the deployment was already terminal and removes it after access is revoked", async () => {
    vi.useFakeTimers();
    state.load.mockResolvedValueOnce(null).mockResolvedValueOnce({ id: "c1", text: ["Investigate <script>untrusted</script>"] }).mockRejectedValueOnce(new Error("forbidden"));
    render(<DeploymentDiagnosis deployment={deployment} />);
    await act(async () => {});
    expect(screen.getByText(/No diagnosis attached yet/)).toBeInTheDocument();
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000); });
    expect(screen.getByText("Investigate <script>untrusted</script>")).toBeInTheDocument();
    expect(document.querySelector("script")).toBeNull();
    expect(screen.queryByRole("textbox")).toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000); });
    expect(screen.queryByText("Investigate <script>untrusted</script>")).toBeNull();
    expect(screen.getByText(/Diagnosis unavailable/)).toBeInTheDocument();
  });
  it("hides the old report immediately when principal or selected scope changes", async () => {
    state.load.mockResolvedValueOnce({ id: "c1", text: ["Private old report"] }).mockImplementation(() => new Promise(() => {}));
    const { rerender } = render(<DeploymentDiagnosis deployment={deployment} />);
    expect(await screen.findByText("Private old report")).toBeInTheDocument();
    state.principal = "usr_2";
    rerender(<DeploymentDiagnosis deployment={{ ...deployment, environment_id: "env_2" }} />);
    expect(screen.queryByText("Private old report")).toBeNull();
    expect(state.load.mock.calls[0][1].aborted).toBe(true);
  });
});
