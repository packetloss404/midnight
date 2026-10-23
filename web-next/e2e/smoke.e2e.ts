import { expect, test } from "@playwright/test";
import { openDeterministicConsole } from "./support/app";

test("renders the project index and navigates through the application shell", async ({
  page,
}) => {
  const pageErrors: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));

  await openDeterministicConsole(page);

  await expect(page).toHaveTitle("Midnight Console");
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
  await expect(page.getByRole("link", { name: /Gate Zero/ })).toBeVisible();

  await page.getByRole("link", { name: "Instance settings" }).click();

  await expect(page).toHaveURL(/\/settings$/);
  await expect(page.getByRole("heading", { name: "Server settings" })).toBeVisible();
  expect(pageErrors).toEqual([]);
});
