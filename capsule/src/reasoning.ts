import type { ThinkingLevel } from "@earendil-works/pi-agent-core";

import type { StartMessage } from "./wire.js";

export function piReasoningState(
  effort: StartMessage["provider"]["reasoning_effort"],
): { modelReasoning: boolean; thinkingLevel: ThinkingLevel } {
  const thinkingLevel = effort === "none" ? "off" : effort;
  return { modelReasoning: thinkingLevel !== "off", thinkingLevel };
}
