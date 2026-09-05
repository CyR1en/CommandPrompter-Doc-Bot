import { expect, test } from "vitest";

import type { DiscordChannel } from "./types";
import { bindingChecks } from "./DiscordPage";

const common = [10, 11, 14, 16];
const sendInThread = 38;
const createPublicThread = 35;

test("same-channel replies to an existing thread require send-in-thread but not create-thread", () => {
  expect(review("same_channel", thread([...common, sendInThread])).ok).toBe(true);
  expect(review("same_channel", thread(common)).messages).toContain("Replies to an existing thread also need Send Messages in Threads.");
});

test("selected existing threads require send-in-thread but not create-thread", () => {
  expect(review("selected_channel", thread([...common, sendInThread])).ok).toBe(true);
  expect(review("selected_channel", thread(common)).ok).toBe(false);
});

test("thread policy on a non-thread destination requires create-thread and send-in-thread", () => {
  expect(review("thread", text([...common, createPublicThread, sendInThread])).ok).toBe(true);
  expect(review("thread", text([...common, sendInThread])).messages).toContain("Creating a thread reply also needs Create Public Threads and Send Messages in Threads.");
});

function review(policy: "same_channel" | "selected_channel" | "thread", reply: DiscordChannel) {
  return bindingChecks(text(common), reply, policy, "public", [], [], ["mention"]);
}

function text(bits: number[]): DiscordChannel {
  return channel(0, bits);
}

function thread(bits: number[]): DiscordChannel {
  return channel(11, bits);
}

function channel(channelType: number, bits: number[]): DiscordChannel {
  return {
    channel_id: "400000000000000001",
    channel_type: channelType,
    connection_id: "00000000-0000-0000-0000-000000000020",
    effective_bot_permissions: bits.reduce((value, bit) => value + 2 ** bit, 0),
    everyone_can_view: false,
    name: "delivery",
    parent_id: null,
    permission_status: "ready",
    position: 1,
    refreshed_at: "2026-08-28T12:05:00Z",
    server_id: "300000000000000001",
    viewer_role_ids: [],
    viewer_user_ids: [],
  };
}
