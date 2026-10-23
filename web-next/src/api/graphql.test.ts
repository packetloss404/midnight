import { afterEach, describe, expect, it, vi } from "vitest";
import { GraphQLError, graphqlRequest } from "./graphql";

afterEach(() => {
  vi.restoreAllMocks();
});

describe("GraphQL error boundary", () => {
  it("does not expose an upstream HTML response", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response("<html><body>private proxy details</body></html>", {
        status: 502,
        headers: { "x-request-id": "req-graphql-1" },
      }),
    );

    const error = await graphqlRequest("query { serverInfo { apiVersion } }").catch(
      (caught: unknown) => caught,
    );
    expect(error).toBeInstanceOf(GraphQLError);
    expect(error).toMatchObject({
      status: 502,
      requestId: "req-graphql-1",
      message: "The GraphQL endpoint returned an invalid response.",
    });
    expect((error as Error).message).not.toContain("private proxy details");
  });

  it("bounds a GraphQL error message", () => {
    const error = new GraphQLError([{ message: "x".repeat(1_000) }], 400);
    expect(error.message.length).toBeLessThanOrEqual(280);
    expect(error.message.endsWith("…")).toBe(true);
  });
});
