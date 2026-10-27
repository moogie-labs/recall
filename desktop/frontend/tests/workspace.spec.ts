import { test, expect } from "@playwright/test";
test("tree, Markdown modes, exact autosave timing, and cmd-f search", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/test-app.html");
  const folder = page.getByRole("treeitem", { name: "Work", exact: true });
  await expect(folder).toHaveAttribute("aria-expanded", "false");
  await folder.click();
  await page.getByRole("treeitem", { name: "Reading", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Reading notes" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  await page.clock.install();
  await page.locator(".cm-content").fill("# Edited note\n\nAutosave sample");
  await expect(page.getByRole("status")).toHaveText("Unsaved changes");
  await page.clock.fastForward(1000);
  await expect(page.getByRole("status")).toHaveText("Autosaving");
  await page.clock.fastForward(2000);
  await expect(page.getByRole("status")).toHaveText("Saved");
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Edited note" }),
  ).toBeVisible();
  await page.keyboard.press("Meta+f");
  await expect(page.getByRole("textbox", { name: "Search" })).toBeFocused();
  await page.getByRole("textbox", { name: "Search" }).fill("Autosave");
  await page.clock.fastForward(250);
  await expect(page.locator(".search-hit")).toContainText("Autosave sample");
  await page.keyboard.press("Escape");
  await expect(
    page.getByRole("heading", { name: "Edited note" }),
  ).toBeVisible();
  await page.screenshot({ path: "test-results/recall-preview.png" });
});
test("create note task and folder in active folder and delete with confirmation", async ({
  page,
}) => {
  await page.goto("/test-app.html");
  await page.getByRole("treeitem", { name: "Work", exact: true }).click();
  for (const [kind, name] of [
    ["folder", "Ideas"],
    ["note", "Outline"],
    ["task", "Release"],
  ]) {
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await page
      .getByRole("menuitem", { name: "New " + kind, exact: true })
      .click();
    await page.getByRole("textbox", { name: "Name", exact: true }).fill(name);
    await page
      .getByRole("button", { name: "Create item", exact: true })
      .click();
    await expect(
      page.getByRole("treeitem", { name, exact: true }),
    ).toBeVisible();
  }
  await page
    .getByRole("button", { name: "Delete selection", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText("permanent deletion");
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(
    page.getByRole("treeitem", { name: "Release", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Delete selection", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Delete permanently", exact: true })
    .click();
  await expect(
    page.getByRole("treeitem", { name: "Release", exact: true }),
  ).toHaveCount(0);
});
test("conflicting external edits block simulated Quit and retain the draft", async ({
  page,
}) => {
  await page.goto("/test-app.html");
  await page.getByRole("treeitem", { name: "Work", exact: true }).click();
  await page.getByRole("treeitem", { name: "Reading", exact: true }).click();
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  await page.locator(".cm-content").fill("My pending draft");
  await page.evaluate(async () => {
    await (window as any).__test.externalEdit();
    (window as any).__test.emit("desktop:quit-requested", {
      token: "test-quit",
    });
  });
  await expect(page.getByRole("status")).toHaveText("Save failed");
  await expect(page.locator(".cm-content")).toContainText("My pending draft");
  expect(await page.evaluate(() => (window as any).__test.quits)).toBe(0);
});
