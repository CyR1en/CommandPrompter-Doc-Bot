import {
  useEffect,
  useId,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from "react";

import { ErrorNotice } from "./StatusBadge";

interface ConfirmationDialogProps {
  children: ReactNode;
  confirmLabel: string;
  error: string | null;
  expectedText?: string;
  fallbackFocusRef?: RefObject<HTMLElement | null>;
  onClose(): void;
  onConfirm(): Promise<void>;
  open: boolean;
  title: string;
}

export function ConfirmationDialog({
  children,
  confirmLabel,
  error,
  expectedText,
  fallbackFocusRef,
  onClose,
  onConfirm,
  open,
  title,
}: ConfirmationDialogProps): ReactNode {
  const cancelRef = useRef<HTMLButtonElement>(null);
  const titleID = useId();
  const dialogRef = useRef<HTMLDialogElement>(null);
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const [confirmation, setConfirmation] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return;
    if (open && !dialog.open) {
      returnFocusRef.current =
        document.activeElement instanceof HTMLElement
          ? document.activeElement
          : null;
      setConfirmation("");
      dialog.showModal();
      cancelRef.current?.focus();
    } else if (!open && dialog.open) {
      dialog.close();
    }
  }, [open]);

  function close(): void {
    onClose();
    const returnFocus = returnFocusRef.current;
    queueMicrotask(() => {
      if (returnFocus?.isConnected) {
        returnFocus.focus();
      } else {
        fallbackFocusRef?.current?.focus();
      }
    });
  }

  const confirmed = expectedText === undefined || confirmation === expectedText;

  return (
    <dialog
      aria-labelledby={titleID}
      className="confirmation-dialog"
      onCancel={(event) => {
        event.preventDefault();
        close();
      }}
      ref={dialogRef}
    >
      <form
        method="dialog"
        onSubmit={(event) => {
          event.preventDefault();
          if (!confirmed || submitting) return;
          setSubmitting(true);
          onConfirm()
            .then(close)
            .catch(() => undefined)
            .finally(() => setSubmitting(false));
        }}
      >
        <p className="eyebrow">Confirm operation</p>
        <h2 id={titleID}>{title}</h2>
        <div className="dialog-copy">{children}</div>
        {expectedText !== undefined ? (
          <label>
            Type <strong>{expectedText}</strong> exactly to continue
            <input
              autoComplete="off"
              onChange={(event) => setConfirmation(event.currentTarget.value)}
              value={confirmation}
            />
          </label>
        ) : null}
        <ErrorNotice message={error} />
        <div className="dialog-actions">
          <button className="button secondary" onClick={close} ref={cancelRef} type="button">
            Keep it
          </button>
          <button className="button danger" disabled={!confirmed || submitting} type="submit">
            {submitting ? "Working…" : confirmLabel}
          </button>
        </div>
      </form>
    </dialog>
  );
}
