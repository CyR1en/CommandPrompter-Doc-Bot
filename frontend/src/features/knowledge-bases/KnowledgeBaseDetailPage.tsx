import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";

import {
  deleteKnowledgeBase,
  getKnowledgeBase,
  restoreKnowledgeBase,
  safeErrorMessage,
  updateKnowledgeBase,
} from "../../api/client";
import { queryKeys } from "../../api/queries";
import { ConfirmationDialog } from "../../app/ConfirmationDialog";
import { Select } from "../../app/Select";
import { ErrorNotice, StatusBadge } from "../../app/StatusBadge";
import { useCsrfToken } from "../../app/auth";
import { useIdempotencyKey } from "../../app/useIdempotencyKey";
import { GenerateWikiPanel } from "../documentation/GenerateWikiPanel";
import { JobEnqueueNotice } from "../jobs/JobEnqueueNotice";
import { ModelAssignmentsPanel } from "./ModelAssignmentsPanel";

type Access = "public" | "restricted";
type UpdateMutationInput = Omit<
  Parameters<typeof updateKnowledgeBase>[0],
  "csrfToken"
>;
type RestoreMutationInput = Omit<
  Parameters<typeof restoreKnowledgeBase>[0],
  "csrfToken"
>;

function parseAccess(value: string): Access {
  return value === "public" ? "public" : "restricted";
}

export function KnowledgeBaseDetailPage({ id }: { id: string }): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const knowledgeBase = useQuery({
    queryKey: [...queryKeys.knowledgeBases, id],
    queryFn: () => getKnowledgeBase(id),
  });
  const [name, setName] = useState("");
  const [access, setAccess] = useState<Access>("restricted");
  const [language, setLanguage] = useState("");
  const [instructions, setInstructions] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [deleteJobId, setDeleteJobId] = useState<string | null>(null);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const updateKey = useIdempotencyKey();
  const lifecycleKey = useIdempotencyKey();
  const restoreKey = useIdempotencyKey();
  const deleteKey = useIdempotencyKey();

  useEffect(() => {
    if (!knowledgeBase.data) return;
    setName(knowledgeBase.data.name);
    setAccess(knowledgeBase.data.access);
    setLanguage(knowledgeBase.data.language);
    setInstructions(knowledgeBase.data.instructions);
  }, [knowledgeBase.data]);

  async function settle(): Promise<void> {
    await queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBases });
  }

  const update = useMutation({
    mutationFn: (input: UpdateMutationInput) => updateKnowledgeBase({
      ...input,
      csrfToken,
    }),
    onSuccess: async () => {
      updateKey.reset();
      lifecycleKey.reset();
      await settle();
    },
    onError: (caught: unknown) => setError(safeErrorMessage(caught)),
  });
  const restore = useMutation({
    mutationFn: (input: RestoreMutationInput) => restoreKnowledgeBase({
      ...input,
      csrfToken,
    }),
    onSuccess: async () => {
      restoreKey.reset();
      await settle();
    },
    onError: (caught: unknown) => setError(safeErrorMessage(caught)),
  });

  const record = knowledgeBase.data;

  function save(event: FormEvent<HTMLFormElement>): void {
    event.preventDefault();
    if (!record) return;
    setError(null);
    update.mutate({
      body: {
        access,
        expected_version: record.version,
        instructions,
        language,
        name,
      },
      id,
      idempotencyKey: updateKey.current(),
    });
  }

  function setLifecycle(lifecycle: "active" | "archived"): void {
    if (!record) return;
    setError(null);
    update.mutate({
      body: { expected_version: record.version, lifecycle },
      id,
      idempotencyKey: lifecycleKey.current(),
    });
  }

  async function remove(): Promise<void> {
    if (!record) return;
    setDialogError(null);
    try {
      const deletion = await deleteKnowledgeBase({
        confirmationName: record.name,
        csrfToken,
        expectedVersion: record.version,
        id,
        idempotencyKey: deleteKey.current(),
      });
      setDeleteJobId(deletion.job_id);
      deleteKey.reset();
      await settle();
    } catch (caught: unknown) {
      setDialogError(safeErrorMessage(caught));
      throw caught;
    }
  }

  return (
    <section className="page narrow-page">
      <Link className="back-link" to="/knowledge-bases">← Knowledge base register</Link>
      <header className="page-heading">
        <div>
          <p className="eyebrow">knowledge base detail</p>
          <h1 ref={headingRef} tabIndex={-1}>{record?.name ?? "Loading record…"}</h1>
        </div>
        {record ? <StatusBadge value={record.lifecycle} /> : null}
      </header>
      <ErrorNotice message={error} />
      {record ? (
        <>
          <form className="folio-panel form-grid" onSubmit={save}>
            <label>
              Name
              <input disabled={record.lifecycle === "deleted"} maxLength={255} onChange={(event) => { setName(event.currentTarget.value); updateKey.reset(); }} required value={name} />
            </label>
            <label>
              Access
              <Select
                disabled={record.lifecycle === "deleted"}
                onChange={(next) => { setAccess(parseAccess(next)); updateKey.reset(); }}
                options={[{ label: "Restricted", value: "restricted" }, { label: "Public", value: "public" }]}
                value={access}
              />
            </label>
            <label>
              Language
              <input disabled={record.lifecycle === "deleted"} maxLength={35} onChange={(event) => { setLanguage(event.currentTarget.value); updateKey.reset(); }} required value={language} />
            </label>
            <label className="full-field">
              Instructions
              <textarea disabled={record.lifecycle === "deleted"} onChange={(event) => { setInstructions(event.currentTarget.value); updateKey.reset(); }} rows={7} value={instructions} />
            </label>
            {record.lifecycle !== "deleted" ? <button className="button primary" disabled={update.isPending} type="submit">{update.isPending ? "Saving…" : "Save changes"}</button> : null}
          </form>
          <ModelAssignmentsPanel knowledgeBaseId={record.id} mutable={record.lifecycle === "active"} />
          <GenerateWikiPanel knowledgeBase={record} />
          <section aria-labelledby="lifecycle-title" className="folio-panel danger-zone">
            <p className="eyebrow">Lifecycle</p>
            <h2 id="lifecycle-title">Record state</h2>
            {record.lifecycle === "active" ? (
              <button className="button secondary" disabled={update.isPending} onClick={() => setLifecycle("archived")} type="button">Archive</button>
            ) : null}
            {record.lifecycle === "archived" ? (
              <button className="button secondary" disabled={update.isPending} onClick={() => setLifecycle("active")} type="button">Return to active</button>
            ) : null}
            {record.lifecycle === "pending_delete" ? (
              <button className="button primary" disabled={restore.isPending} onClick={() => restore.mutate({ expectedVersion: record.version, id, idempotencyKey: restoreKey.current() })} type="button">{restore.isPending ? "Restoring…" : "Restore before purge"}</button>
            ) : null}
            {deleteJobId ? <JobEnqueueNotice jobId={deleteJobId} label="Knowledge base purge" /> : null}
            {["active", "archived"].includes(record.lifecycle) ? (
              <button className="button danger" onClick={() => { deleteKey.reset(); setDialogError(null); setConfirmDelete(true); }} type="button">Schedule deletion</button>
            ) : null}
            {record.lifecycle === "deleted" ? <p>This knowledge base has been purged. Audit history remains.</p> : null}
          </section>
          <ConfirmationDialog
            confirmLabel="Schedule deletion"
            error={dialogError}
            expectedText={record.name}
            fallbackFocusRef={headingRef}
            onClose={() => { setConfirmDelete(false); setDialogError(null); }}
            onConfirm={remove}
            open={confirmDelete}
            title={`Delete “${record.name}”?`}
          >
            <p>This schedules a deferred purge. You can restore the record during its grace period.</p>
          </ConfirmationDialog>
        </>
      ) : null}
    </section>
  );
}
