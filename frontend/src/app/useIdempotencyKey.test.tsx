import { act, renderHook } from "@testing-library/react";
import { expect, test } from "vitest";

import { useIdempotencyKey } from "./useIdempotencyKey";

test("retries reuse an action key until an edit or success resets it", () => {
  const { result } = renderHook(useIdempotencyKey);
  const firstAttempt = result.current.current();
  expect(result.current.current()).toBe(firstAttempt);

  act(() => result.current.reset());
  expect(result.current.current()).not.toBe(firstAttempt);
});
