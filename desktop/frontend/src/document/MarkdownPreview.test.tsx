import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { MarkdownPreview } from "./MarkdownPreview";
it("renders rich body Markdown semantically", () => {
  render(
    <MarkdownPreview
      body={
        '# Hello\n\n## Section\n\n> Quote\n\n- [ ] Task\n- Item\n\n| Name | Value |\n| --- | --- |\n| A | B |\n\n```go\nfmt.Println("hello")\n```'
      }
    />,
  );
  expect(screen.getByRole("heading", { name: "Hello" })).toBeInTheDocument();
  expect(screen.getByRole("table")).toBeInTheDocument();
  expect(screen.getByRole("checkbox")).toBeDisabled();
  expect(screen.getByText("Quote")).toBeInTheDocument();
});
it("keeps unsafe content inert and does not fetch images", () => {
  const { container } = render(
    <MarkdownPreview
      body={
        '<script>alert(1)</script>\n\n<img src="x" onerror="alert(1)">\n\n[bad](javascript:alert(1)) [local](file:///etc/passwd) [relative](other.note.md)\n\n![remote](https://evil.example/track) ![local image](../secret.png)'
      }
    />,
  );
  expect(container.querySelector("script,img,iframe,[onerror]")).toBeNull();
  expect(container.querySelector("a")).toBeNull();
  expect(screen.getByText(/remote.*unavailable/)).toBeInTheDocument();
});
it("opens only explicit http links through the host", () => {
  const opened: string[] = [];
  render(
    <MarkdownPreview
      body={"[Read](https://example.com/path)"}
      openExternal={(url) => {
        opened.push(url);
      }}
    />,
  );
  fireEvent.click(screen.getByRole("link", { name: "Read" }));
  expect(opened).toEqual(["https://example.com/path"]);
});
