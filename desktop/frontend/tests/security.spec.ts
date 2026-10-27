import { test, expect } from "@playwright/test";
test("authored content cannot execute or load remote resources", async ({
  page,
}) => {
  const requests: string[] = [];
  page.on("request", (r) => {
    if (!r.url().startsWith("http://127.0.0.1:4175/")) requests.push(r.url());
  });
  await page.addInitScript(() => {
    (window as any).__fixtureBody =
      '# Safe heading\n\n<script>window.__pwned=true</script>\n\n<img src=x onerror="window.__pwned=true">\n\n![remote](https://evil.example/track)\n\n[bad](javascript:alert(1)) [local](file:///etc/passwd)';
  });
  await page.goto("/test-app.html");
  await page.getByRole("treeitem", { name: "Work", exact: true }).click();
  await page.getByRole("treeitem", { name: "Reading", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Safe heading" }),
  ).toBeVisible();
  await expect(
    page.locator(".markdown img,.markdown script,.markdown iframe,.markdown a"),
  ).toHaveCount(0);
  expect(await page.evaluate(() => (window as any).__pwned)).toBeUndefined();
  expect(requests).toEqual([]);
  expect(
    await page.evaluate(() => (window as any).__test.api.readFile),
  ).toBeUndefined();
});
