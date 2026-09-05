# Architecture synthesis

> Historical design record, completed before the Go ref0 cutover. The
> [current platform architecture](openwiki-platform.md) defines runtime
> ownership and security boundaries.

## Rubric

The comparison used release coverage, load-bearing domain types, crash and retry behavior, security boundaries, migration proof, and interface depth. The question was whether an implementer could ship the specified platform without inventing the missing ownership rules.

## Selected base

Candidate A won because it connects the caller contract to strict domain values, fenced durable work, immutable artifacts, explicit SQL ownership, and migration gates. Its crash handling covers the awkward gap between accepting a domain result and marking the generic job complete. Its first migration unit also names exact files, non-goals, and acceptance checks. That makes it a credible first change rather than an architecture wish list.

## Applied grafts

- Candidate B contributes material answer spans. The verifier can remove unsupported spans while retaining independently supported material. It returns insufficient evidence only when no supported material remains.
- Candidate B makes `submit_page` an explicit ownership boundary. It validates the assigned attempt, staged draft, complete Claim set, evidence, hashes, and finalization scheduling.
- Candidate B specifies the Mermaid fallback. Deterministic finalization converts an invalid diagram to readable text and records that conversion.
- Candidate C contributes a closed `JobCommand` union and handler table. Job dispatch cannot depend on ad hoc string payload parsing.
- Candidate C adds `retry_wait.not_before`. Retry delay is durable scheduling state.
- Candidate C contributes the route-group-to-service map. It makes HTTP ownership reviewable without moving business policy into routes.

## Rejected choices

- Candidate B captures run inputs in the worker. The selected design captures them when the accepted run is created, so the returned run has fixed inputs immediately.
- Candidate B keeps new runtime paths inert until cutover. The migration instead exercises each replacement path behind concrete gates before the release cutover.
- Candidate C leaves several security-sensitive values as strings. The selected design uses typed IDs, remote URLs, artifact and resource keys, validated extra bodies, secret leases, and safe external URLs.
- Candidate C exposes separate planner, page writer, and answer factory ports. The selected `AgentEngine` hides those implementation details behind three operations.
- Candidate C uses slug-only draft paths. Attempt-scoped page-job paths prevent retries and stale workers from sharing a file.

## Candidate status

Candidate A is the selected base. Candidate B and Candidate C are not discarded wholesale. Their listed improvements are included where they strengthen the selected shape. Neither remains an active architecture candidate.

## Verification result

The package was checked against the specification, repository vocabulary, arena verdict, and the required graft and rejection list. The architecture document contains caller usage first, domain types, state machines, service and port boundaries, crash semantics, security, SQL ownership, layout, process and frontend ownership, and migration gates. The first implementation unit remains Candidate A's precise Discord seam.
