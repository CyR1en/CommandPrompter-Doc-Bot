import { Link } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type ReactNode } from "react";

import { cancelJob, getJob, safeErrorMessage } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { ConfirmationDialog } from "../../app/ConfirmationDialog";
import { StatusBadge } from "../../app/StatusBadge";
import { useCsrfToken } from "../../app/auth";
import { useIdempotencyKey } from "../../app/useIdempotencyKey";

const cancellable = new Set(["pending", "leased", "retry_wait", "cancel_requested"]);

export function JobDetailPage({ id }: { id: string }): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const job = useQuery({ queryKey: [...queryKeys.jobs, id], queryFn: () => getJob(id) });
  const [confirming, setConfirming] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const cancelKey = useIdempotencyKey();

  async function cancel(): Promise<void> {
    setDialogError(null);
    try {
      await cancelJob({ csrfToken, id, idempotencyKey: cancelKey.current() });
      cancelKey.reset();
      await queryClient.invalidateQueries({ queryKey: queryKeys.jobs });
    } catch (caught: unknown) {
      setDialogError(safeErrorMessage(caught));
      throw caught;
    }
  }

  return (
    <section className="page narrow-page">
      <Link className="back-link" to="/jobs">← Job ledger</Link>
      <header className="page-heading">
        <div>
          <p className="eyebrow">job detail</p>
          <h1 ref={headingRef} tabIndex={-1}>{job.data?.job_type.replaceAll("_", " ") ?? "Loading job…"}</h1>
        </div>
        {job.data ? <StatusBadge value={job.data.status} /> : null}
      </header>
      {job.data ? (
        <article className="folio-panel detail-ledger">
          <dl>
            <div><dt>Job ID</dt><dd>{job.data.id}</dd></div>
            <div><dt>Target</dt><dd>{job.data.target_type} · {job.data.target_id}</dd></div>
            <div><dt>Progress</dt><dd>{job.data.progress}%</dd></div>
            <div><dt>Attempts</dt><dd>{job.data.attempt_count} of {job.data.max_attempts}</dd></div>
            <div><dt>Created</dt><dd>{new Date(job.data.created_at).toLocaleString()}</dd></div>
            <div><dt>Updated</dt><dd>{new Date(job.data.updated_at).toLocaleString()}</dd></div>
            {job.data.not_before ? <div><dt>Not before</dt><dd>{new Date(job.data.not_before).toLocaleString()}</dd></div> : null}
            {job.data.sanitized_error ? <div><dt>Last error</dt><dd>{job.data.sanitized_error}</dd></div> : null}
          </dl>
          {cancellable.has(job.data.status) ? (
            <button className="button danger" onClick={() => { cancelKey.reset(); setDialogError(null); setConfirming(true); }} type="button">Cancel job</button>
          ) : null}
        </article>
      ) : null}
      <ConfirmationDialog
        confirmLabel="Request cancellation"
        error={dialogError}
        fallbackFocusRef={headingRef}
        onClose={() => { setConfirming(false); setDialogError(null); }}
        onConfirm={cancel}
        open={confirming}
        title="Cancel this durable job?"
      >
        <p>The worker will converge the job to a cancelled state unless an accepted result has already committed.</p>
      </ConfirmationDialog>
    </section>
  );
}
