import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useRef, useState } from "react";
import { afterEach, expect, test, vi } from "vitest";

import { ConfirmationDialog } from "./ConfirmationDialog";

afterEach(cleanup);

test("requires the exact case-sensitive name and returns focus", async () => {
  const confirm = vi.fn(async () => undefined);
  const user = userEvent.setup();

  function Harness() {
    const [open, setOpen] = useState(false);
    return (
      <>
        <button onClick={() => setOpen(true)} type="button">Delete record</button>
        <ConfirmationDialog
          confirmLabel="Schedule deletion"
          error={null}
          expectedText="Exact Name"
          onClose={() => setOpen(false)}
          onConfirm={confirm}
          open={open}
          title="Delete this record?"
        >
          <p>Deferred purge.</p>
        </ConfirmationDialog>
      </>
    );
  }

  render(<Harness />);
  const opener = screen.getByRole("button", { name: "Delete record" });
  await user.click(opener);
  expect(screen.getByRole("button", { name: "Keep it" })).toHaveFocus();

  const input = screen.getByLabelText(/Type Exact Name exactly/);
  await user.type(input, "exact name");
  expect(screen.getByRole("button", { name: "Schedule deletion" })).toBeDisabled();
  await user.clear(input);
  await user.type(input, "Exact Name");
  await user.click(screen.getByRole("button", { name: "Schedule deletion" }));

  expect(confirm).toHaveBeenCalledOnce();
  expect(opener).toHaveFocus();
});

test("renders handled failures, allows retry, and uses fallback focus", async () => {
  const user = userEvent.setup();
  let attempts = 0;

  function Harness() {
    const [open, setOpen] = useState(false);
    const [error, setError] = useState<string | null>(null);
    const [showOpener, setShowOpener] = useState(true);
    const fallbackRef = useRef<HTMLHeadingElement>(null);

    async function confirm(): Promise<void> {
      attempts += 1;
      if (attempts === 1) {
        setError("The operation could not be completed.");
        throw new Error("handled failure");
      }
      setShowOpener(false);
      setError(null);
      await Promise.resolve();
    }

    return (
      <>
        <h2 ref={fallbackRef} tabIndex={-1}>Persistent lifecycle heading</h2>
        {showOpener ? <button onClick={() => setOpen(true)} type="button">Delete record</button> : null}
        <ConfirmationDialog
          confirmLabel="Schedule deletion"
          error={error}
          fallbackFocusRef={fallbackRef}
          onClose={() => setOpen(false)}
          onConfirm={confirm}
          open={open}
          title="Delete this record?"
        >
          <p>Deferred purge.</p>
        </ConfirmationDialog>
      </>
    );
  }

  render(<Harness />);
  await user.click(screen.getByRole("button", { name: "Delete record" }));
  await user.click(screen.getByRole("button", { name: "Schedule deletion" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("The operation could not be completed.");
  expect(screen.getByRole("dialog")).toHaveAttribute("open");

  await user.click(screen.getByRole("button", { name: "Schedule deletion" }));
  await waitFor(() => expect(screen.getByRole("heading", { name: "Persistent lifecycle heading" })).toHaveFocus());
  expect(attempts).toBe(2);
});

test("multiple ledger dialogs keep distinct accessible names", () => {
  const noOp = async (): Promise<void> => undefined;
  render(
    <>
      <ConfirmationDialog confirmLabel="Delete first" error={null} onClose={() => undefined} onConfirm={noOp} open={false} title="Delete first token?"><p>First.</p></ConfirmationDialog>
      <ConfirmationDialog confirmLabel="Delete second" error={null} onClose={() => undefined} onConfirm={noOp} open title="Delete second token?"><p>Second.</p></ConfirmationDialog>
    </>,
  );

  expect(screen.getByRole("dialog", { name: "Delete second token?" })).toHaveAttribute("open");
  expect(screen.queryByRole("dialog", { name: "Delete first token?" })).not.toBeInTheDocument();
});
