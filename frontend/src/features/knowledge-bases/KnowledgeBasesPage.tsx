import { Link } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";

import {
  createKnowledgeBase,
  listKnowledgeBases,
  safeErrorMessage,
} from "../../api/client";
import { queryKeys } from "../../api/queries";
import { Select } from "../../app/Select";
import { EmptyState, ErrorNotice, StatusBadge } from "../../app/StatusBadge";
import { useCsrfToken } from "../../app/auth";
import { useIdempotencyKey } from "../../app/useIdempotencyKey";

type Access = "public" | "restricted";

function parseAccess(value: string): Access {
  return value === "public" ? "public" : "restricted";
}

export function KnowledgeBasesPage(): ReactNode {
  const knowledgeBases = useQuery({ queryKey: queryKeys.knowledgeBases, queryFn: listKnowledgeBases });

  return (
    <section className="page">
      <header className="page-heading">
        <div>
          <p className="eyebrow">knowledge bases</p>
          <h1>Knowledge base register</h1>
        </div>
        <p className="lede">Each record has its own access policy and reversible lifecycle.</p>
      </header>
      <CreateKnowledgeBaseForm />
      <section aria-labelledby="knowledge-base-list-title" className="ledger-section">
        <div className="section-heading">
          <h2 id="knowledge-base-list-title">Registered knowledge bases</h2>
          <span>{knowledgeBases.data?.length ?? 0} records</span>
        </div>
        <div className="record-grid">
          {knowledgeBases.data?.map((knowledgeBase) => (
            <Link className="record-card" key={knowledgeBase.id} params={{ id: knowledgeBase.id }} to="/knowledge-bases/$id">
              <div>
                <span className="credential-kind">{knowledgeBase.access}</span>
                <h3>{knowledgeBase.name}</h3>
                <p>{knowledgeBase.language} · version {knowledgeBase.version}</p>
              </div>
              <StatusBadge value={knowledgeBase.lifecycle} />
            </Link>
          ))}
        </div>
        {knowledgeBases.data?.length === 0 ? <EmptyState>No knowledge bases have been created.</EmptyState> : null}
      </section>
    </section>
  );
}

function CreateKnowledgeBaseForm(): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [access, setAccess] = useState<Access>("restricted");
  const [language, setLanguage] = useState("en");
  const [instructions, setInstructions] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const actionKey = useIdempotencyKey();

  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    setSubmitting(true);
    setError(null);
    try {
      await createKnowledgeBase({
        access,
        csrfToken,
        idempotencyKey: actionKey.current(),
        instructions,
        language,
        name,
      });
      setName("");
      setInstructions("");
      actionKey.reset();
      await queryClient.invalidateQueries({ queryKey: queryKeys.knowledgeBases });
    } catch (caught: unknown) {
      setError(safeErrorMessage(caught));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <details className="folio-panel disclosure">
      <summary>Create knowledge base</summary>
      <form className="form-grid" onSubmit={(event) => void submit(event)}>
        <label>
          Name
          <input maxLength={255} onChange={(event) => { setName(event.currentTarget.value); actionKey.reset(); }} required value={name} />
        </label>
        <label>
          Access
          <Select
            onChange={(next) => { setAccess(parseAccess(next)); actionKey.reset(); }}
            options={[{ label: "Restricted", value: "restricted" }, { label: "Public", value: "public" }]}
            value={access}
          />
        </label>
        <label>
          Language
          <input maxLength={35} onChange={(event) => { setLanguage(event.currentTarget.value); actionKey.reset(); }} required value={language} />
        </label>
        <label className="full-field">
          Instructions
          <textarea onChange={(event) => { setInstructions(event.currentTarget.value); actionKey.reset(); }} rows={4} value={instructions} />
        </label>
        <ErrorNotice message={error} />
        <button className="button primary" disabled={submitting} type="submit">{submitting ? "Creating…" : "Create knowledge base"}</button>
      </form>
    </details>
  );
}
