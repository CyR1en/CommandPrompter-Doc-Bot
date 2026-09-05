# Pi capsule isolation

The Pi capsule runs documentation planning and page-writing agent loops without
giving Node direct access to the network, credentials, source snapshots, or the
application database. The trusted Go worker owns those capabilities.

## Deployment topology

Each worker uses exactly two capsule slots. Compose starts each slot in a
separate container with:

- `network_mode: none`;
- a read-only root filesystem;
- all Linux capabilities dropped except the supervisor's process-management
  capabilities;
- `no-new-privileges`;
- fixed CPU, memory, process, and shared-memory limits;
- one private Unix-socket volume mounted read-only into the worker.

The slots do not share a socket volume. `PI_CAPSULE_SOCKET_PATHS` must be a JSON
array of two distinct absolute paths whose base name is `capsule.sock`. Before
it can accept work, the Go slot pool verifies both paths, rejects symlinked path
components, checks distinct parent directory identities, and requires the exact
ownership and modes. The parent directory must be `root:2000` with mode `02750`.
The socket must be `root:2000` with mode `0660`. The worker runs as UID 1000 and
GID 2000.

The capsule image revision is `pi-0.84.4-r9`. It pins
`@earendil-works/pi-agent-core` and `@earendil-works/pi-ai` to `0.84.4`.
The Go host and the Node child both reject another revision.

## The supervisor starts one child per attempt

The Go supervisor listens on `/run/capsule/capsule.sock`. It accepts only a Unix
peer with worker UID 1000. The supervisor handles one connection at a time and
starts a fresh Node child with UID and GID 1001 for that attempt.

The child receives only `HOME`, `PATH`, and `TMPDIR`. It does not inherit
deployment secrets. The supervisor runs as a Linux subreaper. At the end of an
attempt it terminates the process group and any remaining process owned by the
child UID. If cleanup cannot prove that no child remains, the slot exits instead
of accepting another attempt.

## The wire protocol carries capabilities, not credentials

The private protocol is UTF-8 JSON framed by a four-byte big-endian length. Both
sides validate the closed message forms in `capsule/wire.schema.json`. They also
enforce frame, aggregate, string, nesting, key, fetch, model-request, and response
body limits.

The default attempt limits are:

| Limit | Value |
| --- | ---: |
| Frame bytes | 2,097,152 |
| Aggregate protocol bytes | 16,777,216 |
| String bytes | 1,048,576 |
| JSON depth | 32 |
| JSON keys | 50,000 |
| Provider response body bytes | 1,048,576 |
| Relayed fetches | 64 |
| Model requests | 16 |
| Whole-attempt timeout | 660 seconds |

Documentation source tools share a 64-call budget. One tool result is limited
to 131,072 bytes, and all tool results together are limited to 1,048,576 bytes.
Reads are further bounded by line, match, directory-entry, file-size, and scanned
byte limits in `internal/capsuledoc/source_tools.go`.

The Go host starts an attempt with:

- a random attempt ID and a `PLANNER` or `PAGE_WRITER` role;
- the system prompt and task prompt;
- a closed list of read-only source tools and their JSON Schemas;
- the required structured-output schema;
- the model ID, admitted sampling settings, reasoning effort, context window,
  output limit, timeout, and capsule revision;
- the attempt budgets.

The message does not contain a provider URL, credential, custom header, or
source path on the host. A model request from Node contains only its operation
ID and turn number. A source tool call contains only the exact granted tool
name, provider call ID, and validated arguments.

## Go owns provider requests

Go resolves the captured credential identity and version before it leases a
capsule slot.
It constructs the OpenAI Chat Completions request from the captured provider
profile and the host-owned transcript. The request uses one choice, SSE usage,
sequential tool calls, bounded sampling settings, and the captured reasoning
setting. On the final admitted turn, Go requires the named `submit_result` tool.

The safe network client owns DNS resolution, address policy, TLS, origin checks,
redirect rejection, response bounds, and error sanitization. Go retries only
408, 409, 429, 5xx, and classified retryable transport failures. It caps a
server-directed retry delay at 60 seconds and otherwise uses bounded exponential
backoff. The configured provider timeout cannot exceed 60 seconds, and the whole
attempt cannot exceed 660 seconds.

The Go executor also derives a 20-hour capture-and-execution deadline from the
24-hour captured-wiki reservation. Its five-second detached receipt settlement
remains inside the four-hour safety margin.

Go strictly parses provider SSE. It rejects malformed or truncated streams,
unknown or duplicate JSON members, wrong model identity, invalid finish reasons,
multiple or mixed tool calls, incoherent usage, and data outside the configured
limits. It sends Node only a normalized SSE body.

## Go owns source tools and acceptance

Node can request only the tools in the attempt's closed grant. The Go host checks
that each capsule tool call exactly matches the call ID, name, and arguments
accepted from the preceding provider response. Source tools resolve only within
the immutable snapshots captured for the documentation run. They do not expose
a shell or arbitrary host path.

Each model turn may contain either one source tool call or one `submit_result`
call. Parallel and mixed batches fail the attempt. Completion must equal the
accepted `submit_result` arguments and pass the supplied output schema.

`internal/capsuledoc` performs another deterministic check after the capsule
returns. It parses the plan or page submission, validates source references and
evidence against the captured snapshots, and builds accepted artifacts. A model
result alone cannot publish a page or wiki.

## Cancellation and failures release the slot safely

A capsule session is single-use. Context cancellation closes the connection and
sends a bounded cancel message when possible. Protocol, provider, tool, invalid
result, and cleanup failures return sanitized errors while retaining validated
model usage. The worker releases a slot only after the connection closes. Pool
shutdown waits for active leases or the shutdown deadline.

The capsule's lack of networking is deliberate. Pi supplies the pinned agent
loop, while Go keeps every authority that could disclose data or change durable
state.
