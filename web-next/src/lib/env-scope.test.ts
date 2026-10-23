import { describe, expect, it } from "vitest";
import {
  COMPARE_ENVIRONMENTS_SCOPE,
  concreteEnvironmentId,
  isCompareEnvironmentScope,
  parseEnvSearch,
  selectDefaultEnvironmentId,
} from "./env-scope";

const environments = [
  {
    id: "env_dev",
    slug: "development",
    name: "Development",
    tier: "dev",
  },
  {
    id: "env_prod",
    slug: "production",
    name: "Production",
    tier: "prod",
  },
];

describe("environment scope", () => {
  it("keeps omitted scope distinct from explicit compare scope", () => {
    expect(parseEnvSearch({})).toEqual({});
    expect(parseEnvSearch({ env: COMPARE_ENVIRONMENTS_SCOPE })).toEqual({
      env: COMPARE_ENVIRONMENTS_SCOPE,
    });
    expect(isCompareEnvironmentScope(COMPARE_ENVIRONMENTS_SCOPE)).toBe(true);
    expect(concreteEnvironmentId(COMPARE_ENVIRONMENTS_SCOPE)).toBeUndefined();
  });

  it("retains concrete environment ids for mutation scope", () => {
    expect(concreteEnvironmentId("env_prod")).toBe("env_prod");
  });

  it("selects Production even when it is not the first API result", () => {
    expect(selectDefaultEnvironmentId(environments)).toBe("env_prod");
  });

  it("falls back through default name and then stable API order", () => {
    expect(
      selectDefaultEnvironmentId([
        { id: "env_preview", slug: "preview" },
        { id: "env_default", name: "Default" },
      ]),
    ).toBe("env_default");
    expect(
      selectDefaultEnvironmentId([
        { id: "env_first", slug: "alpha" },
        { id: "env_second", slug: "beta" },
      ]),
    ).toBe("env_first");
    expect(selectDefaultEnvironmentId([])).toBeUndefined();
  });
});
