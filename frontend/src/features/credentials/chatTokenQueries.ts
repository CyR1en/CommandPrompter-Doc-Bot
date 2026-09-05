import { useInfiniteQuery } from "@tanstack/react-query";

import { listChatAccessTokensPage } from "../../api/client";
import { queryKeys } from "../../api/queries";

export function useChatAccessTokenPages() {
  return useInfiniteQuery({
    queryKey: queryKeys.chatAccessTokens,
    queryFn: ({ pageParam }) => listChatAccessTokensPage({ cursor: pageParam || undefined, limit: 50 }),
    initialPageParam: "",
    getNextPageParam: (page) => page.next_cursor,
  });
}
