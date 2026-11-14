import { expect, it } from "vitest";
import { imageSource } from "./imageSource";
it.each([
  [
    "plan.assets/shot.png",
    "Ideas/plan.md",
    "/note-image/Ideas/plan.assets/shot.png",
  ],
  [
    "my%20plan.assets/a%20b.jpg",
    "my plan.md",
    "/note-image/my%20plan.assets/a%20b.jpg",
  ],
  ["./plan.assets/x.webp", "plan.md", "/note-image/plan.assets/x.webp"],
  [
    "../Other/n.assets/x.GIF",
    "Ideas/plan.md",
    "/note-image/Other/n.assets/x.GIF",
  ],
  [
    "plan.assets/shot.png?v=1#top",
    "plan.md",
    "/note-image/plan.assets/shot.png",
  ],
])("maps %s from %s", (src, note, want) => {
  expect(imageSource(src, note)).toBe(want);
});
it.each([
  ["https://evil.example/plan.assets/x.png", "plan.md"],
  ["file:///etc/plan.assets/x.png", "plan.md"],
  ["data:image/png;base64,AAAA", "plan.md"],
  ["/abs/plan.assets/x.png", "plan.md"],
  ["//host/plan.assets/x.png", "plan.md"],
  ["../plan.assets/x.png", "plan.md"],
  ["secret.png", "plan.md"],
  ["plan.assets/x.svg", "plan.md"],
  ["plan.assets/nested/x.png", "plan.md"],
  ["plan.assets%2F..%2F..%2Fx.png", "plan.md"],
  ["plan.assets/%E0%A4%A.png", "plan.md"],
  ["plan.assets/x.png", undefined],
  ["", "plan.md"],
])("rejects %s from %s", (src, note) => {
  expect(imageSource(src, note)).toBeNull();
});
