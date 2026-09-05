# ref0 documentation platform

This context defines the language for the self-hosted documentation platform. The platform turns approved sources into maintained documentation and evidence-backed answers.

## Language

**Knowledge base**:
A named collection of sources that shares one access policy, generated wiki, and documentation model assignments.
_Avoid_: Workspace, project, corpus

**Agent**:
A selectable answer identity with one immutable current configuration version, one answer model, an ordered knowledge-base corpus, guardrails, and delivery routes.
_Avoid_: Answer engine, bot, model

**Agent version**:
An immutable capture of an Agent's identity, model selection, ordered knowledge bases, and configurable guardrails.
_Avoid_: Agent settings row, mutable Agent config

**Agent run**:
A completed execution receipt that captures the exact Agent, model, provider endpoint, knowledge-base, wiki, usage, tools, citations, and outcome metadata used by one invocation.
_Avoid_: Query run, conversation, saved chat

**Source**:
An external body of information that belongs to one knowledge base.
_Avoid_: Context, data source

**Repository source**:
A source backed by one Git repository and one selected branch or commit.
_Avoid_: Repo URL, checkout

**Source revision**:
An immutable version of a source used by one documentation run.
_Avoid_: Latest source, working copy

**Documentation run**:
A durable attempt to create or update the wiki for a fixed set of source revisions.
_Avoid_: Indexing job, preprocessing

**Wiki version**:
An immutable, published set of linked Markdown pages for one knowledge base.
_Avoid_: Index, cache

**Claim**:
A material statement in a wiki page that is supported by one or more evidence records.
_Avoid_: Fact, assertion

**Evidence**:
A reference to an exact location in a source revision that supports a claim.
_Avoid_: Citation, context

**Provider endpoint**:
An OpenAI-compatible HTTP service that exposes one or more language models.
_Avoid_: Provider, LLM connection

**Model profile**:
A discovered or manually entered model plus the capabilities and limits that this application uses.
_Avoid_: Model config, variant

**Model assignment**:
The model profile selected for the documentation planner or documentation writer role in a knowledge base.
_Avoid_: Default model

**Chat access token**:
A secret-once bearer credential scoped to an explicit set of Agents for the OpenAI-compatible API.
_Avoid_: Provider key, operator session, reader session

**Discord connection**:
An authenticated Discord bot identity that can join servers and receive events.
_Avoid_: Bot config, Discord token

**Channel binding**:
A mapping from one Discord connection/server/listen-channel route to one Agent, one or more concrete triggers, and a reply policy.
_Avoid_: Server config, channel config, integration

**Discord context**:
A bounded sequence of successfully delivered Discord question/answer messages for one binding, Agent version, user, and destination.
_Avoid_: Chat session, HTTP conversation, OpenCode session
