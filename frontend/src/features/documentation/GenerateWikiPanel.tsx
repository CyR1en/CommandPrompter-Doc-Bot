import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { generateKnowledgeBase, safeErrorMessage, type KnowledgeBase } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { ErrorNotice } from "../../app/StatusBadge";
import { useCsrfToken } from "../../app/auth";
import { useIdempotencyKey } from "../../app/useIdempotencyKey";
import { JobEnqueueNotice } from "../jobs/JobEnqueueNotice";

type GenerateInput = {
  expectedVersion: number;
  id: string;
  idempotencyKey: string;
};

export function GenerateWikiPanel({ knowledgeBase }: { knowledgeBase: KnowledgeBase }): ReactNode {
  const csrfToken = useCsrfToken();
  const queryClient = useQueryClient();
  const generateKey = useIdempotencyKey();
  const generate = useMutation({
    mutationFn: (input: GenerateInput) => generateKnowledgeBase({ ...input, csrfToken }),
    onSuccess: async () => {
      generateKey.reset();
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeys.jobs }),
        queryClient.invalidateQueries({ queryKey: queryKeys.runs }),
      ]);
    },
  });

  return (
    <section aria-labelledby="generate-wiki-title" className="folio-panel generation-panel">
      <p className="eyebrow">Documentation generation</p>
      <h2 id="generate-wiki-title">Build the linked wiki</h2>
      <p>Generation captures the current source revisions and assigned model versions. Private source content is sent to the assigned provider models.</p>
      <button
        className="button primary"
        disabled={generate.isPending || knowledgeBase.lifecycle !== "active"}
        onClick={() => generate.mutate({ expectedVersion: knowledgeBase.version, id: knowledgeBase.id, idempotencyKey: generateKey.current() })}
        type="button"
      >{generate.isPending ? "Enqueueing generation…" : knowledgeBase.published_wiki_id ? "Update wiki" : "Generate first wiki"}</button>
      {knowledgeBase.lifecycle !== "active" ? <p className="field-note">Return this knowledge base to active before generating documentation.</p> : null}
      {generate.data ? <JobEnqueueNotice jobId={generate.data.id} label="Documentation preparation" /> : null}
      <ErrorNotice message={generate.error ? safeErrorMessage(generate.error) : null} />
    </section>
  );
}
