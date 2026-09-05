import { useInfiniteQuery } from "@tanstack/react-query";

import {
  listAgentRunsPage,
  listAgentsPage,
  listAgentVersionsPage,
} from "../../api/client";
import { queryKeys } from "../../api/queries";

const AGENT_PAGE_SIZE = 50;
const HISTORY_PAGE_SIZE = 25;

export function useAgentPages(enabled = true) {
  return useInfiniteQuery({
    enabled,
    queryKey: queryKeys.agents,
    queryFn: ({ pageParam }) => listAgentsPage({ cursor: pageParam || undefined, limit: AGENT_PAGE_SIZE }),
    initialPageParam: "",
    getNextPageParam: (page) => page.next_cursor,
  });
}

export function useAgentRunPages(agentId: string, enabled = true) {
  return useInfiniteQuery({
    enabled: enabled && agentId !== "",
    queryKey: [...queryKeys.agentRuns, agentId],
    queryFn: ({ pageParam }) => listAgentRunsPage({
      agentId,
      cursor: pageParam || undefined,
      limit: HISTORY_PAGE_SIZE,
    }),
    initialPageParam: "",
    getNextPageParam: (page) => page.next_cursor,
    refetchInterval: enabled ? 30_000 : false,
  });
}

export function useAgentVersionPages(agentId: string, enabled = true) {
  return useInfiniteQuery({
    enabled: enabled && agentId !== "",
    queryKey: [...queryKeys.agentVersions, agentId],
    queryFn: ({ pageParam }) => listAgentVersionsPage({
      agentId,
      cursor: pageParam || undefined,
      limit: HISTORY_PAGE_SIZE,
    }),
    initialPageParam: "",
    getNextPageParam: (page) => page.next_cursor,
  });
}
