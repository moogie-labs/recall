import { act, render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { DeleteDialog } from "./DeleteDialog";
const note = { id: "a", path: "a.md", name: "a", kind: "note" };
const noop = async () => {};
it("says how many attached images a note deletion removes", async () => {
  render(
    <DeleteDialog
      entry={note}
      attachments={async () => 3}
      onDelete={noop}
      onCancel={() => {}}
    />,
  );
  expect(
    await screen.findByText(/Also deletes 3 attached images\./),
  ).toBeInTheDocument();
});
it("uses the singular for one image", async () => {
  render(
    <DeleteDialog
      entry={note}
      attachments={async () => 1}
      onDelete={noop}
      onCancel={() => {}}
    />,
  );
  expect(
    await screen.findByText(/Also deletes 1 attached image\./),
  ).toBeInTheDocument();
});
it("adds nothing when a note has no images or the count fails", async () => {
  for (const attachments of [
    async () => 0,
    async () => {
      throw new Error("gone");
    },
  ]) {
    const { unmount } = render(
      <DeleteDialog
        entry={note}
        attachments={attachments}
        onDelete={noop}
        onCancel={() => {}}
      />,
    );
    await act(async () => {});
    expect(screen.queryByText(/attached image/)).toBeNull();
    unmount();
  }
});
