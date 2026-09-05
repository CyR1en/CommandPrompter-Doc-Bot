const FETCH_FRAME_RESERVE_BYTES = 8192;

export type AttemptLimits = Readonly<{
  max_frame_bytes: number;
  max_aggregate_bytes: number;
  max_string_bytes: number;
  max_fetch_body_bytes: number;
  max_fetches: number;
  max_model_requests: number;
}>;

export function validateAttemptLimits(limits: AttemptLimits): void {
  const requiredFetchFrame =
    4 * Math.ceil(limits.max_fetch_body_bytes / 3) + FETCH_FRAME_RESERVE_BYTES;
  if (
    limits.max_aggregate_bytes < Math.max(4096, limits.max_frame_bytes) ||
    limits.max_string_bytes > limits.max_frame_bytes ||
    requiredFetchFrame > limits.max_frame_bytes ||
    limits.max_model_requests > limits.max_fetches
  ) {
    throw new Error("invalid protocol cross-field limits");
  }
}

export class AttemptBudget {
  private fetches = 0;
  private modelRequests = 0;
  private readonly limits: AttemptLimits;

  constructor(limits: AttemptLimits) {
    this.limits = limits;
  }

  beginModelRequest(): number {
    this.modelRequests += 1;
    if (this.modelRequests > this.limits.max_model_requests)
      throw new Error("model request budget exceeded");
    return this.modelRequests;
  }

  beginFetch(): void {
    this.fetches += 1;
    if (this.fetches > this.limits.max_fetches)
      throw new Error("fetch budget exceeded");
  }
}

export function submissionBatchAllowed(
  toolNames: readonly string[],
  alreadySubmitted: boolean,
): boolean {
  if (alreadySubmitted) return false;
  const submissions = toolNames.filter((name) => name === "submit_result");
  return (
    submissions.length === 0 ||
    (submissions.length === 1 && toolNames.length === 1)
  );
}

export class SubmissionPolicy {
  private submitted: Record<string, unknown> | undefined;
  private invalid = false;

  observeToolBatch(toolNames: readonly string[]): void {
    if (!submissionBatchAllowed(toolNames, this.submitted !== undefined))
      this.invalid = true;
  }

  assertActivityAllowed(): void {
    if (this.submitted !== undefined || this.invalid)
      throw new Error("activity after result submission is forbidden");
  }

  blockToolCall():
    | { block: true; reason: string; terminate: true }
    | undefined {
    if (!this.invalid) return undefined;
    return {
      block: true,
      reason: "invalid result submission batch",
      terminate: true,
    };
  }

  submit(value: Record<string, unknown>): void {
    this.assertActivityAllowed();
    this.submitted = value;
  }

  shouldStop(): boolean {
    return this.invalid || this.submitted !== undefined;
  }

  result(): Record<string, unknown> {
    if (this.invalid || this.submitted === undefined)
      throw new Error("agent returned no structured result");
    return this.submitted;
  }
}
