import { expect, test } from "@playwright/test";
import { openDeterministicConsole } from "./support/app";

test("stages a tenant generated domain through Configure > Networking", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openDeterministicConsole(
    page,
    "/projects/project_gate0?env=env_production",
    { operatorRecovery: false, instanceAdmin: false },
  );

  await page.getByRole("button", { name: "Open API" }).click();
  await page.getByRole("tab", { name: "Configure" }).click();

  const configure = page.getByRole("tablist", {
    name: "Configure sections",
  });
  await expect(
    configure.getByRole("tab", { name: "Variables" }),
  ).toHaveAttribute("aria-selected", "true");
  await configure.getByRole("tab", { name: "Networking" }).click();

  await expect(page).toHaveURL(
    /\/services\/svc_api\/configure\?env=env_production&configure=networking/,
  );
  await expect(
    page.getByRole("heading", { name: "Networking", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Generated HTTP domain" }),
  ).toBeVisible();
  await expect(
    page.getByText("No tenant-managed generated domain has been created."),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Custom HTTP hostnames" }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Manage custom hostnames" }),
  ).toBeVisible();
  await expect(page.getByRole("textbox")).toHaveCount(0);
  await expect(page.getByRole("checkbox")).toHaveCount(0);

  await page.getByRole("button", { name: "Stage domain" }).click();
  await expect(
    page.getByText(/Generated HTTP intent is ready in Changes/),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Open HTTP ↗" }),
  ).toHaveCount(0);
  await expect(
    page
      .getByRole("region", { name: "Configuration workflow" })
      .getByText("1 edit in Changes"),
  ).toBeVisible();
  await page.getByRole("button", { name: "Review" }).click();
  await expect(
    page.getByText("generated public domain", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: /Save for review|Apply & deploy/ }),
  ).toBeEnabled();
});
