// Browser credential storage is intentionally split by authority.
//
// Human session tokens represent one verified user. Installation-admin human
// sessions are also accepted by privileged control-plane, GraphQL, agent, and
// streaming surfaces. The separate recovery token remains a break-glass path
// for installations whose human identity service is unavailable.

export const OPERATOR_RECOVERY_TOKEN_STORAGE_KEY = "midnight.api-token";
export const HUMAN_SESSION_TOKEN_STORAGE_KEY = "midnight.human-session-token";
export const CREDENTIALS_CHANGED_EVENT = "midnight:credentials-changed";

export type CredentialKind = "human" | "operator";

export type CredentialChangeDetail = {
  kind: CredentialKind;
};

export function getHumanSessionToken(): string | undefined {
  return readSessionToken(HUMAN_SESSION_TOKEN_STORAGE_KEY);
}

export function setHumanSessionToken(value?: string): boolean {
  if (!writeSessionToken(HUMAN_SESSION_TOKEN_STORAGE_KEY, value)) return false;
  announceCredentialChange("human");
  return true;
}

export function isHumanSessionToken(value: string): boolean {
  return value.trim().startsWith("midn_user_");
}

export function getOperatorRecoveryToken(): string | undefined {
  const runtimeToken = readSessionToken(OPERATOR_RECOVERY_TOKEN_STORAGE_KEY);
  if (runtimeToken) return runtimeToken;

  try {
    return import.meta.env?.DEV
      ? import.meta.env.VITE_MIDNIGHT_TOKEN?.trim() || undefined
      : undefined;
  } catch {
    return undefined;
  }
}

export function setOperatorRecoveryToken(value?: string): boolean {
  if (!writeSessionToken(OPERATOR_RECOVERY_TOKEN_STORAGE_KEY, value)) return false;
  announceCredentialChange("operator");
  return true;
}

function readSessionToken(key: string): string | undefined {
  try {
    return window.sessionStorage.getItem(key)?.trim() || undefined;
  } catch {
    // Storage may be unavailable in private browsing or during SSR.
    return undefined;
  }
}

function writeSessionToken(key: string, value?: string): boolean {
  const token = value?.trim() ?? "";
  try {
    if (token) window.sessionStorage.setItem(key, token);
    else window.sessionStorage.removeItem(key);
    return true;
  } catch {
    return false;
  }
}

function announceCredentialChange(kind: CredentialKind): void {
  try {
    window.dispatchEvent(
      new CustomEvent<CredentialChangeDetail>(CREDENTIALS_CHANGED_EVENT, {
        detail: { kind },
      }),
    );
  } catch {
    // No window/event target during SSR.
  }
}
