import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/api/client";

const mocks = vi.hoisted(() => ({ onboard: vi.fn(), login: vi.fn(), navigate: vi.fn(), status: "anonymous" }));
vi.mock("@/components/panels/invitations/api", () => ({ onboardIdentityInvitation: mocks.onboard }));
vi.mock("@/auth/AuthProvider", () => ({ useAuth: () => ({ state: { status: mocks.status }, login: mocks.login }) }));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => mocks.navigate }));
import { InvitationOnboarding } from "./InvitationOnboarding";

beforeEach(() => {
  vi.clearAllMocks();
  mocks.status = "anonymous";
  mocks.login.mockImplementation(async () => { mocks.status = "human"; });
  mocks.navigate.mockResolvedValue(undefined);
});

async function enterAccount() {
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("Invitation credential"), "midn_inv_fixture-private");
  await user.type(screen.getByLabelText("Your name"), "Invited Person");
  await user.type(screen.getByLabelText(/^Password/), "a long private password");
  await user.type(screen.getByLabelText("Confirm password"), "a long private password");
  await user.click(screen.getByRole("button", { name: "Create account and join" }));
}

describe("invitation-bound enrollment", () => {
  it("enrolls with no caller-selected role/email/scope, then uses normal login and exact project", async () => {
    mocks.onboard.mockResolvedValue({ email: "invited@example.test", invitation: { project_id: "prj_invited" } });
    render(<InvitationOnboarding />);
    await enterAccount();
    await waitFor(() => expect(mocks.navigate).toHaveBeenCalledWith({ to: "/projects/prj_invited", replace: true }));
    expect(mocks.onboard).toHaveBeenCalledWith({ invitation_token: "midn_inv_fixture-private", display_name: "Invited Person", password: "a long private password" });
    expect(mocks.login).toHaveBeenCalledWith({ email: "invited@example.test", password: "a long private password", client_kind: "dashboard" });
    expect(window.location.search).not.toContain("midn_inv");
    expect(sessionStorage.getItem("midn_inv_fixture-private")).toBeNull();
  });

  it("explains existing-account refusal without resetting the password", async () => {
    mocks.onboard.mockRejectedValue(new ApiError(409, { error: { code: "INVITATION_ACCOUNT_EXISTS" } }));
    render(<InvitationOnboarding />);
    await enterAccount();
    expect(await screen.findByRole("alert")).toHaveTextContent("This account already exists");
    expect(mocks.login).not.toHaveBeenCalled();
    expect(screen.getByLabelText(/^Password/)).toHaveValue("");
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/login");
  });

  it("waits for committed human authentication before opening the protected project", async () => {
    mocks.onboard.mockResolvedValue({ email: "invited@example.test", invitation: { project_id: "prj_invited" } });
    mocks.login.mockImplementationOnce(async () => undefined);
    const { rerender } = render(<InvitationOnboarding />);
    await enterAccount();
    await waitFor(() => expect(mocks.login).toHaveBeenCalled());
    expect(mocks.navigate).not.toHaveBeenCalled();
    mocks.status = "human";
    rerender(<InvitationOnboarding />);
    await waitFor(() => expect(mocks.navigate).toHaveBeenCalledWith({ to: "/projects/prj_invited", replace: true }));
  });

  it("preserves committed-account recovery when the following login fails", async () => {
    mocks.onboard.mockResolvedValue({ email: "invited@example.test", invitation: { project_id: "prj_invited" } });
    mocks.login.mockRejectedValue(new Error("network unavailable"));
    render(<InvitationOnboarding />);
    await enterAccount();
    expect(await screen.findByRole("alert")).toHaveTextContent("Your account was created and the invitation accepted");
    expect(screen.queryByLabelText("Invitation credential")).not.toBeInTheDocument();
    expect(mocks.onboard).toHaveBeenCalledTimes(1);
  });

  it("directs an existing signed-in human to authenticated acceptance", () => {
    mocks.status = "human";
    render(<InvitationOnboarding />);
    expect(screen.getByRole("status")).toHaveTextContent("already signed in");
    expect(screen.queryByRole("button", { name: "Create account and join" })).not.toBeInTheDocument();
  });
});
