import { test, expect } from "@playwright/test";
// A valid 1×1 PNG.
const png =
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==";
test("pasting an image links it in the note and Preview shows it", async ({
  page,
}) => {
  // The fixture has no Go backend; stand in for the confined image route.
  await page.route("**/note-image/**", (route) =>
    route.fulfill({
      status: 200,
      contentType: "image/png",
      body: Buffer.from(png, "base64"),
    }),
  );
  await page.goto("/test-app.html");
  await page.getByRole("treeitem", { name: "Work", exact: true }).click();
  await page.getByRole("treeitem", { name: "Reading", exact: true }).click();
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  const editor = page.locator(".cm-content");
  await editor.click();
  await page.keyboard.press("Meta+ArrowDown");
  await editor.evaluate((el, data) => {
    const bytes = Uint8Array.from(atob(data), (c) => c.charCodeAt(0));
    const transfer = new DataTransfer();
    transfer.items.add(new File([bytes], "", { type: "image/png" }));
    el.dispatchEvent(
      new ClipboardEvent("paste", {
        clipboardData: transfer,
        bubbles: true,
        cancelable: true,
      }),
    );
  }, png);
  await expect(editor).toContainText(
    /!\[pasted-\d{4}-\d{2}-\d{2}-\d{6}\]\(Reading\.assets\/pasted-\d{4}-\d{2}-\d{2}-\d{6}\.png\)/,
  );
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  const image = page.locator(".markdown img");
  await expect(image).toHaveAttribute(
    "src",
    /^\/note-image\/Work\/Reading\.assets\/pasted-\d{4}-\d{2}-\d{2}-\d{6}\.png$/,
  );
  await expect
    .poll(() => image.evaluate((img: HTMLImageElement) => img.naturalWidth))
    .toBe(1);
});
