import { readSync, writeSync } from "node:fs";

import {
  parseHostMessage,
  parseCapsuleMessage,
  validateComplexity,
} from "./wire.js";
import type { CapsuleMessage, ComplexityLimits, HostMessage } from "./wire.js";

const BOOTSTRAP_MAX_FRAME = 4_194_304;

function readExact(length: number): Buffer {
  const buffer = Buffer.alloc(length);
  let offset = 0;
  while (offset < length) {
    const count = readSync(0, buffer, offset, length - offset, null);
    if (count === 0) throw new Error("unexpected protocol EOF");
    offset += count;
  }
  return buffer;
}

type SyncWriter = (
  fileDescriptor: number,
  buffer: Uint8Array,
  offset: number,
  length: number,
) => number;

export function writeAll(
  fileDescriptor: number,
  buffer: Uint8Array,
  writer: SyncWriter = (fd, value, offset, length) =>
    writeSync(fd, value, offset, length, null),
): void {
  let offset = 0;
  while (offset < buffer.byteLength) {
    const count = writer(
      fileDescriptor,
      buffer,
      offset,
      buffer.byteLength - offset,
    );
    if (count <= 0) throw new Error("protocol write made no progress");
    offset += count;
  }
}

export class FrameIO {
  private aggregate = 0;
  private maxFrame = BOOTSTRAP_MAX_FRAME;
  private maxAggregate = 33_554_432;
  private complexity: ComplexityLimits = {
    maxStringBytes: 2_097_152,
    maxDepth: 64,
    maxKeys: 100_000,
  };

  applyLimits(message: Extract<HostMessage, { type: "start" }>): void {
    this.maxFrame = message.limits.max_frame_bytes;
    this.maxAggregate = message.limits.max_aggregate_bytes;
    this.complexity = {
      maxStringBytes: message.limits.max_string_bytes,
      maxDepth: message.limits.max_depth,
      maxKeys: message.limits.max_keys,
    };
  }

  readHost(): HostMessage {
    const header = readExact(4);
    const length = header.readUInt32BE(0);
    if (length === 0 || length > this.maxFrame)
      throw new Error("invalid protocol frame length");
    this.aggregate += length + 4;
    if (this.aggregate > this.maxAggregate)
      throw new Error("protocol aggregate exceeds limit");
    const bytes = readExact(length);
    let value: unknown;
    try {
      value = JSON.parse(bytes.toString("utf8"));
    } catch {
      throw new Error("invalid protocol JSON");
    }
    validateComplexity(value, this.complexity);
    return parseHostMessage(value);
  }

  write(message: CapsuleMessage): void {
    const validated = parseCapsuleMessage(message);
    validateComplexity(validated, this.complexity);
    const bytes = Buffer.from(JSON.stringify(validated), "utf8");
    if (bytes.byteLength === 0 || bytes.byteLength > this.maxFrame)
      throw new Error("outbound frame exceeds limit");
    this.aggregate += bytes.byteLength + 4;
    if (this.aggregate > this.maxAggregate)
      throw new Error("protocol aggregate exceeds limit");
    const header = Buffer.alloc(4);
    header.writeUInt32BE(bytes.byteLength, 0);
    writeAll(1, header);
    writeAll(1, bytes);
  }
}
