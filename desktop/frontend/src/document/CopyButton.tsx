import { useEffect, useRef, useState } from "react";
import type { OperationReply } from "../api/types";

const copiedDuration = 1500;
const failedDuration = 4000;

export function CopyButton({
  text,
  copy,
}: {
  text: string;
  copy: (text: string) => Promise<OperationReply>;
}) {
  const [outcome, setOutcome] = useState<"idle" | "copied" | "failed">("idle");
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  function show(next: "copied" | "failed") {
    setOutcome(next);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(
      () => setOutcome("idle"),
      next === "copied" ? copiedDuration : failedDuration,
    );
  }
  const label =
    outcome === "copied"
      ? "Copied"
      : outcome === "failed"
        ? "Copy failed"
        : "Copy";
  return (
    <>
      <button
        className={"copy-button" + (outcome === "failed" ? " failed" : "")}
        aria-label={outcome === "idle" ? "Copy Markdown to clipboard" : label}
        onClick={() => {
          void copy(text)
            .then((reply) => show(reply.problem ? "failed" : "copied"))
            .catch(() => show("failed"));
        }}
      >
        {label}
      </button>
      <span className="visually-hidden" aria-live="polite">
        {outcome === "copied"
          ? "Copied to clipboard"
          : outcome === "failed"
            ? "Could not copy the document"
            : ""}
      </span>
    </>
  );
}
