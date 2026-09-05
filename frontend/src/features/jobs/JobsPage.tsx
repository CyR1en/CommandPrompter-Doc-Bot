import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";

import { listJobs, type JobStatus } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { Select } from "../../app/Select";
import { EmptyState, StatusBadge } from "../../app/StatusBadge";

const statuses: JobStatus[] = [
  "pending",
  "leased",
  "succeeded",
  "retry_wait",
  "failed",
  "cancel_requested",
  "cancelled",
];

function parseStatus(value: string): JobStatus | undefined {
  return statuses.find((status) => status === value);
}

export function JobsPage(): ReactNode {
  const [status, setStatus] = useState<JobStatus | undefined>();
  const jobs = useQuery({
    queryKey: [...queryKeys.jobs, status ?? "all"],
    queryFn: () => listJobs(status),
  });

  return (
    <section className="page">
      <header className="page-heading">
        <div>
          <p className="eyebrow">jobs</p>
          <h1>Durable job ledger</h1>
        </div>
        <label className="compact-field">
          Filter status
          <Select
            onChange={(next) => setStatus(parseStatus(next))}
            options={[{ label: "All statuses", value: "all" }, ...statuses.map((value) => ({ label: value.replaceAll("_", " "), value }))]}
            value={status ?? "all"}
          />
        </label>
      </header>
      <div className="table-wrap">
        <table>
          <caption className="sr-only">Jobs matching the selected status</caption>
          <thead>
            <tr><th scope="col">Operation</th><th scope="col">Status</th><th scope="col">Progress</th><th scope="col">Attempt</th><th scope="col">Updated</th></tr>
          </thead>
          <tbody>
            {jobs.data?.map((job) => (
              <tr key={job.id}>
                <th scope="row"><Link params={{ id: job.id }} to="/jobs/$id">{job.job_type.replaceAll("_", " ")}</Link><small>{job.id}</small></th>
                <td><StatusBadge value={job.status} /></td>
                <td>{job.progress}%</td>
                <td>{job.attempt_count} / {job.max_attempts}</td>
                <td><time dateTime={job.updated_at}>{new Date(job.updated_at).toLocaleString()}</time></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {jobs.data?.length === 0 ? <EmptyState>No jobs match this filter.</EmptyState> : null}
    </section>
  );
}
