import { describe, expect, it } from "vitest";
import { isImmutableImageReference } from "./AddServiceForm";

describe("isImmutableImageReference", () => {
  it("accepts a digest-pinned image", () => {
    expect(
      isImmutableImageReference(`registry.example/team/app@sha256:${"a".repeat(64)}`),
    ).toBe(true);
  });

  it("rejects mutable tags and malformed digests", () => {
    expect(isImmutableImageReference("nginx:latest")).toBe(false);
    expect(isImmutableImageReference(`nginx@sha256:${"A".repeat(64)}`)).toBe(false);
    expect(isImmutableImageReference(`nginx@sha256:${"a".repeat(63)}`)).toBe(false);
  });
});
