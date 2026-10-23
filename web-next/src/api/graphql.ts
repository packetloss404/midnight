// Minimal typed POST client for the richer GraphQL surface at
// `/graphql/v2`. REST (`client.ts`) covers reads and deploy intake; the
// supported mutations below live only on GraphQL, so this is their single
// transport. Schema presence is not a capability claim: unwired mutations
// return typed NOT_IMPLEMENTED errors and the UI must gate them.
//
// Auth: GraphQL currently accepts the same operator Bearer token as REST.
// Midnight does not mint project-scoped tokens yet, so this client must not
// label the shared credential as a `Project-Access-Token`.

import { authHeader } from "./client";

const GRAPHQL_ENDPOINT = "/graphql/v2";

export class GraphQLError extends Error {
  readonly errors: GraphQLErrorEntry[];
  readonly status?: number;
  readonly requestId?: string;

  constructor(errors: GraphQLErrorEntry[], status?: number, requestId?: string) {
    const safeErrors = errors.map((error) => ({
      ...error,
      message: safeGraphQLErrorMessage(error.message),
    }));
    super(safeErrors[0]?.message ?? "GraphQL request failed");
    this.name = "GraphQLError";
    this.errors = safeErrors;
    this.status = status;
    this.requestId = requestId?.trim() || undefined;
  }
}

export type GraphQLErrorEntry = {
  message: string;
  path?: (string | number)[];
  extensions?: Record<string, unknown>;
};

type GraphQLResponse<T> = {
  data?: T;
  errors?: GraphQLErrorEntry[];
};

export async function graphqlRequest<T>(
  query: string,
  variables?: Record<string, unknown>,
  opts: { signal?: AbortSignal; idempotencyKey?: string } = {},
): Promise<T> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    Accept: "application/json",
    ...authHeader("operator"),
    ...(opts.idempotencyKey ? { "Idempotency-Key": opts.idempotencyKey } : {}),
  };

  const res = await fetch(GRAPHQL_ENDPOINT, {
    method: "POST",
    headers,
    body: JSON.stringify({ query, variables: variables ?? {} }),
    signal: opts.signal,
  });

  const text = await res.text();
  let json: GraphQLResponse<T>;
  try {
    json = text ? (JSON.parse(text) as GraphQLResponse<T>) : {};
  } catch {
    throw new GraphQLError(
      [{ message: "The GraphQL endpoint returned an invalid response." }],
      res.status,
      responseRequestId(res),
    );
  }
  if (json.errors && json.errors.length > 0) {
    throw new GraphQLError(json.errors, res.status, responseRequestId(res));
  }
  if (!res.ok || json.data === undefined) {
    throw new GraphQLError([
      { message: `GraphQL HTTP ${res.status}` },
    ], res.status, responseRequestId(res));
  }
  return json.data;
}

function responseRequestId(response: Response): string | undefined {
  return (
    response.headers.get("x-request-id") ??
    response.headers.get("x-midnight-request-id") ??
    undefined
  );
}

function safeGraphQLErrorMessage(value: string): string {
  const normalized = value
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  if (!normalized || /<!doctype|<!--|<\/?[a-z][^>]*>/i.test(normalized)) {
    return "GraphQL request failed.";
  }
  if (normalized.length <= 280) return normalized;
  return `${normalized.slice(0, 279).trimEnd()}…`;
}

// ── Mutation document constants ───────────────────────────────────
// Kept as typed helpers so leaf panels call a named function rather than
// hand-rolling documents. Signatures mirror `internal/graphql/schema.graphql`.

export const gql = {
  deploymentRedeploy: `mutation Redeploy($id: ID!, $key: String) {
    deploymentRedeploy(id: $id, idempotencyKey: $key) { id status operationId }
  }`,
  deploymentRollback: `mutation Rollback($id: ID!, $key: String) {
    deploymentRollback(id: $id, idempotencyKey: $key) { id status operationId }
  }`,
  deploymentStop: `mutation Stop($id: ID!, $key: String) {
    deploymentStop(id: $id, idempotencyKey: $key) { id status operationId }
  }`,
  deploymentRestart: `mutation Restart($id: ID!, $key: String) {
    deploymentRestart(id: $id, idempotencyKey: $key) { id status operationId }
  }`,
  deploymentRemove: `mutation Remove($id: ID!, $key: String) {
    deploymentRemove(id: $id, idempotencyKey: $key) { id status operationId }
  }`,
  environmentCreate: `mutation CreateEnv($input: EnvironmentCreateInput!, $key: String) {
    environmentCreate(input: $input, idempotencyKey: $key) { id slug name }
  }`,
} as const;
