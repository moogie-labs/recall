import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { Search } from "./Search";
import { fakeBridge } from "../api/fakeBridge";
import { SaveCoordinator } from "../saving/coordinator";
import { Workspace } from "../navigation/workspace";
import type { SearchReply } from "../api/types";
const doc = {
  id: "a",
  path: "Work/a.note.md",
  name: "a",
  title: "A",
  body: "needle",
  revision: "r0",
  kind: "note",
  state: "",
};
function setup() {
  const api = fakeBridge([doc], ["Work"]);
  const c = new SaveCoordinator(api);
  const w = new Workspace(api, c);
  w.setCollection("test-session");
  return { api, c, w };
}
afterEach(() => vi.useRealTimers());
it("debounces 250ms and ignores older replies", async () => {
  vi.useFakeTimers();
  const { api, c, w } = setup();
  const calls: string[] = [];
  const replies: ((r: SearchReply) => void)[] = [];
  api.search = (input) => {
    calls.push(input.query);
    return new Promise((resolve) => replies.push(resolve));
  };
  const { unmount } = render(
    <Search api={api} workspace={w} session="test-session" reveal={() => {}} />,
  );
  fireEvent.change(screen.getByRole("textbox", { name: "Search" }), {
    target: { value: "first" },
  });
  await act(() => vi.advanceTimersByTimeAsync(249));
  expect(calls).toHaveLength(0);
  await act(() => vi.advanceTimersByTimeAsync(1));
  expect(calls).toEqual(["first"]);
  fireEvent.change(screen.getByRole("textbox", { name: "Search" }), {
    target: { value: "second" },
  });
  await act(() => vi.advanceTimersByTimeAsync(250));
  await act(async () => {
    replies[1]({ hits: [{ ...doc, snippet: "latest" }], problem: null });
  });
  await act(async () => {
    replies[0]({
      hits: [{ ...doc, title: "Old", snippet: "old" }],
      problem: null,
    });
  });
  expect(screen.getByText("latest")).toBeInTheDocument();
  expect(screen.queryByText("Old")).toBeNull();
  unmount();
  c.dispose();
});
it("uses cmd-f and escape without resetting draft save timers", async () => {
  vi.useFakeTimers();
  const { api, c, w } = setup();
  await w.openDocument("a");
  const { unmount } = render(
    <Search api={api} workspace={w} session="test-session" reveal={() => {}} />,
  );
  c.edit("draft");
  await act(() => vi.advanceTimersByTimeAsync(500));
  fireEvent.keyDown(document, { key: "f", metaKey: true });
  expect(screen.getByRole("textbox", { name: "Search" })).toHaveFocus();
  fireEvent.change(screen.getByRole("textbox", { name: "Search" }), {
    target: { value: "needle" },
  });
  fireEvent.keyDown(document, { key: "Escape" });
  expect(screen.getByRole("textbox", { name: "Search" })).toHaveValue("");
  expect(w.snapshot().searchActive).toBe(false);
  await act(() => vi.advanceTimersByTimeAsync(500));
  expect(c.snapshot().status).toBe("autosaving");
  expect(c.snapshot().body).toBe("draft");
  unmount();
  c.dispose();
});
it("offers an explicit keyword retry and flushes before opening results", async () => {
  vi.useFakeTimers();
  const { api, c, w } = setup();
  await w.openDocument("a");
  c.edit("draft");
  const modes: string[] = [];
  api.search = async (input) => {
    modes.push(input.mode);
    return input.mode === "hybrid"
      ? {
          hits: [],
          problem: {
            code: "runtime_unavailable",
            message: "Model missing",
            path: "",
          },
        }
      : { hits: [{ ...doc, snippet: "Found" }], problem: null };
  };
  const revealed: string[] = [];
  const { unmount } = render(
    <Search
      api={api}
      workspace={w}
      session="test-session"
      reveal={(path) => revealed.push(path)}
    />,
  );
  fireEvent.change(screen.getByRole("textbox", { name: "Search" }), {
    target: { value: "needle" },
  });
  await act(() => vi.advanceTimersByTimeAsync(250));
  expect(screen.getByText("Model missing")).toBeInTheDocument();
  fireEvent.click(
    screen.getByRole("button", { name: "Search keywords instead" }),
  );
  await act(() => vi.advanceTimersByTimeAsync(250));
  expect(modes).toEqual(["hybrid", "keyword"]);
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: /A.*Found/ }));
  });
  expect(revealed).toEqual(["Work/a.note.md"]);
  expect((await api.openDocument("test-session", "a")).document?.body).toBe(
    "draft",
  );
  unmount();
  c.dispose();
});
