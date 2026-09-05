import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { getJob, safeErrorMessage } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { ErrorNotice } from "../../app/StatusBadge";

export function JobEnqueueNotice({ jobId, label }: { jobId: string; label: string }): ReactNode {
  const job = useQuery({
    queryKey: [...queryKeys.jobs, jobId],
    queryFn: () => getJob(jobId),
  });

  return (
    <div className="notice" aria-live="polite">
      <p>{job.data ? `${label} job status: ${job.data.status.replaceAll("_", " ")}.` : `${label} job was enqueued.`} <Link params={{ id: jobId }} to="/jobs/$id">Open job</Link>.</p>
      <ErrorNotice message={job.error ? safeErrorMessage(job.error) : null} />
    </div>
  );
}
