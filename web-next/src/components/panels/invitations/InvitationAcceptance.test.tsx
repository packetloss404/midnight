import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  authStatus: "human",
  acceptHookCalls: 0,
  accept: {
    mutateAsync: vi.fn(),
    reset: vi.fn(),
    isPending: false,
    error: null as Error | null,
  },
}));

vi.mock("@/auth/AuthProvider", () => ({
  useAuth: () => ({
    state:
      state.authStatus === "human"
        ? {
            status: "human",
            me: {
              principal: {
                kind: "user",
                user_id: "usr_2",
                session_id: "ses_2",
                auth_method: "password",
                email: "teammate@example.com",
                display_name: "Teammate",
              },
            },
          }
        : { status: state.authStatus },
  }),
}));

vi.mock("./api", () => ({
  useAcceptIdentityInvitation: () => {
    state.acceptHookCalls += 1;
    return state.accept;
  },
}));

import { InvitationAcceptance } from "./InvitationAcceptance";

const acceptedInvitation = {
  id: "inv_1",
  email: "teammate@example.com",
  workspace_id: "wrk_1",
  project_id: "prj_1",
  project_role: "viewer",
  token_prefix: "midn_inv_abcd",
  invited_by_user_id: "usr_1",
  created_at: "2026-07-28T12:00:00Z",
  updated_at: "2026-07-28T13:00:00Z",
  expires_at: "2026-08-04T12:00:00Z",
  accepted_at: "2026-07-28T13:00:00Z",
  accepted_by_user_id: "usr_2",
  resource_version: 2,
};

beforeEach(() => {
  vi.clearAllMocks();
  state.authStatus = "human";
  state.acceptHookCalls = 0;
  state.accept.isPending = false;
  state.accept.error = null;
  localStorage.clear();
  sessionStorage.clear();
});

describe("InvitationAcceptance", () => {
  it("is available only to a signed-in human and does not mount a mutation otherwise", () => {
    state.authStatus = "developer";

    const { container } = render(<InvitationAcceptance />);

    expect(container).toBeEmptyDOMElement();
    expect(state.acceptHookCalls).toBe(0);
  });

  it("rejects non-invitation input locally", async () => {
    const user = userEvent.setup();
    render(<InvitationAcceptance />);

    await user.type(
      screen.getByLabelText(/Invitation credential/),
      "https://midnight.example/invite?token=secret",
    );
    await user.click(
      screen.getByRole("button", { name: "Accept invitation" }),
    );

    expect(
      screen.getByText(/beginning with midn_inv_/),
    ).toBeInTheDocument();
    expect(state.accept.mutateAsync).not.toHaveBeenCalled();
  });

  it("accepts a manually pasted credential, clears it, and never stores it", async () => {
    const user = userEvent.setup();
    const rawCredential = "midn_inv_manual_once_secret";
    state.accept.mutateAsync.mockResolvedValueOnce(acceptedInvitation);

    render(<InvitationAcceptance />);

    expect(
      screen.getByText(/does not accept invitation tokens from a URL or query string/),
    ).toBeInTheDocument();
    await user.type(
      screen.getByLabelText(/Invitation credential/),
      rawCredential,
    );
    await user.click(
      screen.getByRole("button", { name: "Accept invitation" }),
    );

    await waitFor(() =>
      expect(state.accept.mutateAsync).toHaveBeenCalledWith(rawCredential),
    );
    expect(screen.getByText("Invitation accepted")).toBeInTheDocument();
    expect(screen.getByText(/viewer role is active/)).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(rawCredential);
    expect(storageValues(localStorage)).not.toContain(rawCredential);
    expect(storageValues(sessionStorage)).not.toContain(rawCredential);
    expect(state.accept.reset).toHaveBeenCalled();
  });
});

function storageValues(storage: Storage): string[] {
  return Array.from({ length: storage.length }, (_, index) => {
    const key = storage.key(index);
    return key ? storage.getItem(key) ?? "" : "";
  });
}
