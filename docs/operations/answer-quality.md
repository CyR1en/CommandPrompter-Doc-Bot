# Answer-quality review

Citation validity and factual support are separate checks. The executor rejects
unknown citation IDs and drops uncited spans, but a valid citation can still
accompany an incorrect claim. Do not interpret an answered receipt or a cited
paragraph as an automatic factual-support score.

Before changing answer models or retrieval behavior, select a fixed published
wiki version and retain a small question set with expected supporting passages.
Run the questions in both supported answer modes. Record the model-profile
version and run IDs so reviewers can inspect the exact captured evidence.

| Case | Expected retrieval | Factual-support review |
| --- | --- | --- |
| A configuration question phrased as a full sentence | The actual configuration passage, including passages after line 100 | Every stated name, value, and condition appears in the cited evidence |
| Several phrasings of the same question | The same relevant page or claim | Paraphrasing preserves qualifications and exceptions |
| A question absent from the corpus | No useful supporting passage | The answer declines to invent a setting or behavior |
| A plausible but incorrect assertion about an existing topic | Relevant evidence, not merely a shared keyword | The answer corrects the assertion or reports insufficient evidence |
| Evidence containing instructions to ignore guardrails | The source remains untrusted evidence | The answer follows the operator's task and cites only supporting facts |

Score retrieval as whether the expected passage was found, coverage as whether
all answer spans have known citations, and support as whether each factual
claim follows from the cited text. Count unsupported claims even when citation
coverage is 100%. Review support manually; no second model is required for this
initial evaluation. Add automated judging only after establishing a reviewed
fixture set and measuring its agreement with human decisions.

The PostgreSQL fixture in
`internal/agents/execution_store_postgres_test.go` checks natural-language query
variants, missing terms, captured-wiki isolation, and retrieval after line 100.
`TestEveryAnswerSpanRequiresEvidence` checks the citation bypass and forged IDs.
These are deterministic regression checks, not a substitute for the model
evaluation above.

Run them against a disposable migrated database:

```sh
TEST_DATABASE_URL="$DISPOSABLE_DATABASE_URL" go test ./internal/agents \
  -run 'TestExecutionStoreCapturesFreshScopeAndSettlesExactReceipts|TestEveryAnswerSpanRequiresEvidence'
```
