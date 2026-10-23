import { webcrypto } from "node:crypto";
import { beforeAll, describe, expect, it } from "vitest";
import {
  base32Decode,
  base32Encode,
  generateTotpSecret,
  isValidWorkspaceSlug,
  otpauthUri,
  totpCode,
  verifyTotpCode,
  workspaceSlugFrom,
} from "./SetupCrypto";

beforeAll(() => {
  if (!globalThis.crypto?.subtle) {
    Object.defineProperty(globalThis, "crypto", {
      configurable: true,
      value: webcrypto,
    });
  }
});

describe("SetupCrypto", () => {
  it("round-trips Base32 without padding", () => {
    const bytes = Uint8Array.from([0, 1, 2, 250, 251, 252, 253, 254, 255]);
    const encoded = base32Encode(bytes);
    expect(encoded).not.toContain("=");
    expect(Array.from(base32Decode(encoded).slice(0, bytes.length))).toEqual(
      Array.from(bytes),
    );
  });

  it("generates a 160-bit secret encoded as 32 Base32 characters", () => {
    const secret = generateTotpSecret();
    expect(secret).toMatch(/^[A-Z2-7]{32}$/);
    expect(base32Decode(secret)).toHaveLength(20);
  });

  // RFC 6238 appendix B test vector for the SHA-1 seed "12345678901234567890".
  it("matches the RFC 6238 reference vector", async () => {
    const seed = base32Encode(new TextEncoder().encode("12345678901234567890"));
    expect(await totpCode(seed, 59_000)).toBe("287082");
    expect(await totpCode(seed, 1_111_111_109_000)).toBe("081804");
  });

  it("accepts codes inside the one-step skew window and rejects others", async () => {
    const secret = generateTotpSecret();
    const now = 1_700_000_000_000;
    expect(await verifyTotpCode(secret, await totpCode(secret, now), now)).toBe(
      true,
    );
    expect(
      await verifyTotpCode(secret, await totpCode(secret, now - 30_000), now),
    ).toBe(true);
    expect(
      await verifyTotpCode(secret, await totpCode(secret, now - 300_000), now),
    ).toBe(false);
    expect(await verifyTotpCode(secret, "12345", now)).toBe(false);
  });

  it("builds an otpauth URI an authenticator can read", () => {
    const uri = otpauthUri("Midnight", "owner@example.com", "ABCDEFGH");
    expect(uri).toContain("otpauth://totp/Midnight:owner%40example.com?");
    expect(uri).toContain("secret=ABCDEFGH");
    expect(uri).toContain("digits=6");
    expect(uri).toContain("period=30");
  });

  it("derives API-valid workspace slugs", () => {
    expect(workspaceSlugFrom("Acme Platform")).toBe("acme-platform");
    expect(workspaceSlugFrom("  --Acme!! Ops--  ")).toBe("acme-ops");
    expect(isValidWorkspaceSlug("acme-platform")).toBe(true);
    expect(isValidWorkspaceSlug("-acme")).toBe(false);
    expect(isValidWorkspaceSlug("ac")).toBe(false);
  });
});
