import { useEffect, useRef, type ReactNode } from "react";
export function Modal({
  title,
  onCancel,
  children,
}: {
  title: string;
  onCancel: () => void;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const previous = document.activeElement as HTMLElement;
    ref.current?.showModal();
    // showModal() focuses the first focusable control; React's autoFocus runs
    // before the dialog is open, so honour the explicit choice here.
    ref.current?.querySelector<HTMLElement>("[data-autofocus]")?.focus();
    return () => {
      ref.current?.close();
      previous?.focus();
    };
  }, []);
  return (
    <dialog
      ref={ref}
      aria-label={title}
      onCancel={(event) => {
        event.preventDefault();
        onCancel();
      }}
    >
      <h2>{title}</h2>
      {children}
    </dialog>
  );
}
