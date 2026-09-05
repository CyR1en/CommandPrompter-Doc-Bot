import { useMemo, useRef } from "react";

import { actionId } from "../api/client";

interface IdempotencyKey {
  current(): string;
  reset(): void;
}

export function useIdempotencyKey(): IdempotencyKey {
  const value = useRef(actionId());
  return useMemo(
    () => ({
      current: () => value.current,
      reset: () => {
        value.current = actionId();
      },
    }),
    [],
  );
}
