export type ErrorPresentation = {
  status?: number;
  message: string;
  requestId?: string;
  recovery?: string;
};

const MAX_MESSAGE_LENGTH = 280;
const MAX_REQUEST_ID_LENGTH = 128;
const HTML_LIKE = /<!doctype|<!--|<\/?[a-z][^>]*>/i;
const SAFE_REQUEST_ID = /^[a-zA-Z0-9][a-zA-Z0-9._:/-]*$/;

/**
 * Convert an untrusted thrown value into bounded, plain-text UI copy.
 *
 * API gateways and reverse proxies sometimes return an HTML error page. React
 * would escape it, but showing that response still leaks implementation detail
 * and creates an unusable wall of markup. HTML-like and server-error messages
 * are therefore replaced, not rendered or parsed.
 */
export function presentError(error: unknown): ErrorPresentation {
  const record = asRecord(error);
  const nested = asRecord(record?.error);
  const status = readStatus(record);
  const requestId = readRequestId(record, nested, asRecord(record?.details), asRecord(nested?.details));
  const rawMessage = readMessage(error, record, nested);
  const networkFailure = isNetworkFailure(error, rawMessage, status);
  const canceled = error instanceof Error && error.name === "AbortError";

  let message: string;
  if (canceled) {
    message = "The request was canceled.";
  } else if (networkFailure) {
    message = "Could not reach the Midnight API.";
  } else {
    message = messageForStatus(status, sanitizeMessage(rawMessage));
  }

  const recovery = recoveryFor(status, networkFailure, canceled);

  return {
    ...(status === undefined ? {} : { status }),
    message,
    ...(requestId === undefined ? {} : { requestId }),
    ...(recovery === undefined ? {} : { recovery }),
  };
}

function messageForStatus(status: number | undefined, safeMessage: string | undefined): string {
  switch (status) {
    case 401:
      return "Authentication is required.";
    case 403:
      return "You do not have permission to perform this action.";
    case 404:
      return "The requested resource was not found.";
    case 409:
    case 412:
      return "The resource changed while you were working.";
    case 429:
      return "The API is receiving too many requests.";
    default:
      if (status !== undefined && status >= 500) {
        return "The Midnight API returned an unexpected response.";
      }
      return safeMessage ?? "The request could not be completed.";
  }
}

function recoveryFor(
  status: number | undefined,
  networkFailure: boolean,
  canceled: boolean,
): string | undefined {
  if (canceled) return undefined;
  if (networkFailure) return "Check the API connection, then retry.";

  switch (status) {
    case 401:
      return "Sign in or update the API token, then retry.";
    case 403:
      return "Ask a project administrator for access.";
    case 404:
      return "Refresh the page and confirm the resource still exists.";
    case 409:
    case 412:
      return "Refresh the latest state, then retry.";
    case 429:
      return "Wait a moment, then retry.";
    default:
      return status !== undefined && status >= 500 ? "Retry in a moment." : undefined;
  }
}

function sanitizeMessage(value: string | undefined): string | undefined {
  if (!value) return undefined;

  const normalized = value
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, " ")
    .replace(/\s+/g, " ")
    .trim();

  if (!normalized || HTML_LIKE.test(normalized)) return undefined;
  if (normalized.length <= MAX_MESSAGE_LENGTH) return normalized;
  return `${normalized.slice(0, MAX_MESSAGE_LENGTH - 1).trimEnd()}…`;
}

function readStatus(record: Record<string, unknown> | undefined): number | undefined {
  const value = record?.status;
  return typeof value === "number" &&
    Number.isInteger(value) &&
    value >= 100 &&
    value <= 599
    ? value
    : undefined;
}

function readMessage(
  error: unknown,
  record: Record<string, unknown> | undefined,
  nested: Record<string, unknown> | undefined,
): string | undefined {
  if (error instanceof Error) return error.message;
  if (typeof error === "string") return error;
  if (typeof record?.message === "string") return record.message;
  if (typeof nested?.message === "string") return nested.message;
  return undefined;
}

function readRequestId(
  ...records: Array<Record<string, unknown> | undefined>
): string | undefined {
  for (const record of records) {
    const value =
      typeof record?.requestId === "string"
        ? record.requestId
        : typeof record?.request_id === "string"
          ? record.request_id
          : typeof record?.traceId === "string"
            ? record.traceId
            : typeof record?.trace_id === "string"
              ? record.trace_id
              : undefined;
    if (!value) continue;

    const trimmed = value.trim();
    if (
      trimmed.length <= MAX_REQUEST_ID_LENGTH &&
      SAFE_REQUEST_ID.test(trimmed)
    ) {
      return trimmed;
    }
  }
  return undefined;
}

function isNetworkFailure(
  error: unknown,
  message: string | undefined,
  status: number | undefined,
): boolean {
  if (status !== undefined) return false;
  const looksLikeFetchFailure =
    /(?:failed to fetch|network error|load failed|network request failed)/i.test(
      message ?? "",
    );
  return looksLikeFetchFailure && (error instanceof Error || typeof error === "string");
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null
    ? (value as Record<string, unknown>)
    : undefined;
}
