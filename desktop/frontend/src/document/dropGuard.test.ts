import { afterEach, expect, it } from "vitest";
import { guardFileDrops } from "./dropGuard";
let release = () => {};
afterEach(() => {
  release();
  document.body.innerHTML = "";
});
function drag(target: Element, type: "dragover" | "drop", types: string[]) {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", {
    value: { types, dropEffect: "copy" },
  });
  target.dispatchEvent(event);
  return event;
}
it("cancels file drags outside the editor text only", () => {
  release = guardFileDrops(window);
  document.body.innerHTML =
    '<div class="preview"></div><div class="cm-editor"><div class="cm-gutters"></div><div class="cm-content" contenteditable="true"><p>x</p></div></div><div class="cm-editor"><div class="cm-content" contenteditable="false"><p class="busy">y</p></div></div>';
  const preview = document.querySelector(".preview")!,
    gutter = document.querySelector(".cm-gutters")!,
    content = document.querySelector(".cm-content p")!,
    busy = document.querySelector(".busy")!;
  expect(drag(preview, "drop", ["Files"]).defaultPrevented).toBe(true);
  expect(drag(preview, "dragover", ["Files"]).defaultPrevented).toBe(true);
  expect(drag(gutter, "drop", ["Files"]).defaultPrevented).toBe(true);
  expect(drag(content, "drop", ["Files"]).defaultPrevented).toBe(false);
  expect(drag(busy, "drop", ["Files"]).defaultPrevented).toBe(true);
  expect(drag(preview, "drop", ["text/plain"]).defaultPrevented).toBe(false);
  release();
  expect(drag(preview, "drop", ["Files"]).defaultPrevented).toBe(false);
});
