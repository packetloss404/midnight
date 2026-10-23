// Browser-side TOTP enrollment for the first-administrator setup flow.
//
// The secret has to be generated where the operator can read it, and the
// confirmation code has to be checked before the one-shot claim is spent, so
// both live in the console. Everything here is `window.crypto` and WebCrypto:
// RFC 4648 Base32 plus RFC 6238 HOTP over HMAC-SHA-1. Adding an npm dependency
// for ~60 lines of well-specified arithmetic would be a worse trade, and the
// server re-verifies the code before it writes anything.

const BASE32_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";

/** Panel-locked TOTP parameters; these mirror internal/bootstrap/totp.go. */
export const TOTP_DIGITS = 6;
export const TOTP_PERIOD_SECONDS = 30;
export const TOTP_SECRET_BYTES = 20;

/** Server-enforced minimum from internal/bootstrap/password.go. */
export const MIN_ADMIN_PASSWORD_LENGTH = 12;

export function base32Encode(bytes: Uint8Array): string {
  let bits = 0;
  let value = 0;
  let output = "";
  for (const byte of bytes) {
    value = (value << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      output += BASE32_ALPHABET[(value >>> (bits - 5)) & 31];
      bits -= 5;
    }
  }
  if (bits > 0) {
    output += BASE32_ALPHABET[(value << (5 - bits)) & 31];
  }
  return output;
}

export function base32Decode(secret: string): Uint8Array {
  const clean = secret.replace(/[\s=]/g, "").toUpperCase();
  let bits = 0;
  let value = 0;
  const output: number[] = [];
  for (const character of clean) {
    const index = BASE32_ALPHABET.indexOf(character);
    if (index < 0) throw new Error("The enrollment secret is not valid Base32.");
    value = (value << 5) | index;
    bits += 5;
    if (bits >= 8) {
      output.push((value >>> (bits - 8)) & 0xff);
      bits -= 8;
    }
  }
  return Uint8Array.from(output);
}

/** Generates a fresh 160-bit secret, Base32 encoded without padding. */
export function generateTotpSecret(): string {
  const raw = new Uint8Array(TOTP_SECRET_BYTES);
  crypto.getRandomValues(raw);
  return base32Encode(raw);
}

/** Returns the current RFC 6238 code for a Base32 secret. */
export async function totpCode(
  secretBase32: string,
  atMs: number = Date.now(),
): Promise<string> {
  const counter = Math.floor(atMs / 1000 / TOTP_PERIOD_SECONDS);
  const message = new Uint8Array(8);
  // JavaScript bitwise math is 32-bit, so write the counter as two halves.
  new DataView(message.buffer).setUint32(0, Math.floor(counter / 2 ** 32));
  new DataView(message.buffer).setUint32(4, counter >>> 0);

  const secret = base32Decode(secretBase32);
  const key = await crypto.subtle.importKey(
    "raw",
    secret.slice().buffer as ArrayBuffer,
    { name: "HMAC", hash: "SHA-1" },
    false,
    ["sign"],
  );
  const digest = new Uint8Array(await crypto.subtle.sign("HMAC", key, message));

  // RFC 4226 dynamic truncation.
  const offset = digest[digest.length - 1] & 0x0f;
  const binary =
    ((digest[offset] & 0x7f) << 24) |
    ((digest[offset + 1] & 0xff) << 16) |
    ((digest[offset + 2] & 0xff) << 8) |
    (digest[offset + 3] & 0xff);
  return String(binary % 10 ** TOTP_DIGITS).padStart(TOTP_DIGITS, "0");
}

/**
 * Verifies a code against the same +/-1 step window the server allows, so an
 * obvious typo is caught before the one-shot claim is spent.
 */
export async function verifyTotpCode(
  secretBase32: string,
  code: string,
  atMs: number = Date.now(),
): Promise<boolean> {
  const candidate = code.trim();
  if (candidate.length !== TOTP_DIGITS) return false;
  for (const step of [-1, 0, 1]) {
    const at = atMs + step * TOTP_PERIOD_SECONDS * 1000;
    if ((await totpCode(secretBase32, at)) === candidate) return true;
  }
  return false;
}

/** Builds the otpauth:// URI an authenticator app reads. */
export function otpauthUri(
  issuer: string,
  account: string,
  secretBase32: string,
): string {
  const label = `${encodeURIComponent(issuer)}:${encodeURIComponent(account)}`;
  const query = new URLSearchParams({
    secret: secretBase32,
    issuer,
    algorithm: "SHA1",
    digits: String(TOTP_DIGITS),
    period: String(TOTP_PERIOD_SECONDS),
  });
  return `otpauth://totp/${label}?${query.toString()}`;
}

/**
 * Derives a workspace slug the API accepts:
 * `^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`.
 */
export function workspaceSlugFrom(displayName: string): string {
  const slug = displayName
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 64);
  return slug;
}

export function isValidWorkspaceSlug(slug: string): boolean {
  return /^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$/.test(slug);
}

export function isValidEmail(email: string): boolean {
  const trimmed = email.trim();
  const at = trimmed.lastIndexOf("@");
  return (
    at > 0 &&
    at < trimmed.length - 1 &&
    trimmed.length <= 254 &&
    !/\s/.test(trimmed)
  );
}
