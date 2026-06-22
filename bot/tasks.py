"""Background repository polling task for the CMDP Doc Bot.

This module wires :class:`~core.git_manager.RepositoryManager` into a
periodic background task built on :class:`discord.ext.tasks.loop`. On
every cycle each configured repository is cloned or pulled into
``data/repos`` so the OpenCode ``docbot`` agent can read the latest
documentation directly from disk when answering user questions.

If a repository is freshly cloned or updated, the bot also generates
(overwrites) an ``AGENT.md`` file in the repository's root via the
long-lived ``opencode serve`` HTTP API. The generation uses a
throwaway session with the opencode default agent (so the
CommandPrompter-scoped ``docbot`` persona is NOT used — the AGENT.md
is codebase metadata, not a CommandPrompter answer) and the same
provider / model / variant the bot is configured for.

The single-iteration logic lives in :func:`poll_repositories`, which is a
plain coroutine and can be unit-tested in isolation. :func:`build_polling_task`
wraps that coroutine in a :class:`discord.ext.tasks.Loop` configured with
the application's poll interval. The returned loop is **not** started;
the caller starts it (typically from ``setup_hook`` or ``on_ready``) and
cancels it on shutdown.
"""

from __future__ import annotations

import asyncio
import re
from collections.abc import Sequence
from pathlib import Path

from discord.ext import tasks

from core.git_manager import RepositoryManager
from core.logger import get_logger
from core.opencode_client import OpencodeClient, OpencodeClientError

_logger = get_logger(__name__)

#: Default root directory under which repository clones are stored.
DEFAULT_REPOS_ROOT: Path = Path("data/repos")

#: Template for the AGENT.md-generation prompt. ``{repo_dir}`` is
#: substituted with the absolute path to the repository at call time so
#: the agent knows exactly where to write the file. The agent's tools
#: (``read``, ``grep``, ``list``, ``bash``) operate relative to the
#: opencode server's working directory (``data/repos``), so the absolute
#: path keeps the write target unambiguous.
#:
#: The prompt explicitly allows up to 3 subagents to explore the
#: repository in parallel — the opencode server's default agent
#: (``build``) supports subagent orchestration transparently, and
#: splitting the analysis across subagents significantly speeds up
#: AGENT.md generation for large repositories.
_AGENT_MD_PROMPT_TEMPLATE: str = """
Analyze the repository at {repo_dir} and generate an overview of its structure, purpose, and key files. You may orchestrate up to 3 subagents in parallel to explore the repository — for example, one for the source tree, one for key components, and one for navigation tips — to speed up the analysis.

<goal>
The main goal of this file is to help the docbot agent quickly understand the repository's contents and how to navigate it when answering user questions.
</goal>

<important>
Follow this format:

# Repository Overview

# Source Tree

# Key Components

# Navigation Tips
</important>

Do NOT include any information that cannot be directly observed from the repository's files and structure.
DO NOT include any information that is not directly supported by the repository's content.
Do NOT attempt to infer or guess any information that is not explicitly stated in the repository.
If you don't see it in the repo, don't include it in the overview.

Save this overview to a file named AGENT.md at {repo_dir}. Overwrite the file if it already exists.
Try keeping this file as short as possible while still providing a useful overview, ideally under 250 lines.
"""


def derive_repo_name(repo_url: str) -> str:
    """Derive a local directory name from a repository URL.

    Strips surrounding whitespace, a trailing slash, and a trailing
    ``.git`` suffix (case-insensitive), then returns the final path
    segment after splitting on ``/`` or ``:``. This handles the common
    ``https://host/org/repo.git`` and ``git@host:org/repo.git`` forms.

    Args:
        repo_url: The Git URL to derive a name from.

    Returns:
        The repository name, e.g. ``repo`` for
        ``https://github.com/org/repo.git``.
    """
    url = repo_url
    if "#" in url:
        url = url.split("#", 1)[0]
    cleaned: str = url.strip().rstrip("/")
    cleaned = re.sub(r"\.git$", "", cleaned, flags=re.IGNORECASE)
    segments: list[str] = re.split(r"[:/]+", cleaned)
    return segments[-1]


def inject_github_token(url: str, token: str | None) -> str:
    """Inject a GitHub token into a HTTPS GitHub repository URL.

    If a token is provided and the URL starts with ``https://github.com/``
    (or ``https://www.github.com/``), it is rewritten to include the token in
    the form ``https://oauth2:{token}@github.com/...``. Otherwise, the URL is
    returned unmodified.

    Args:
        url: The original repository URL.
        token: The GitHub token to inject, or None.

    Returns:
        The URL with the token injected if applicable, otherwise the original URL.
    """
    if not token:
        return url

    stripped = url.strip()
    lower_url = stripped.lower()
    if lower_url.startswith("https://github.com/"):
        return f"https://oauth2:{token}@github.com/{stripped[19:]}"
    elif lower_url.startswith("https://www.github.com/"):
        return f"https://oauth2:{token}@github.com/{stripped[23:]}"

    return url


async def _generate_agent_md(
    client: OpencodeClient,
    *,
    repo_dir: Path,
    provider_id: str,
    model_id: str,
    variant: str | None = None,
) -> bool:
    """Generate the ``AGENT.md`` file for ``repo_dir`` via the opencode HTTP API.

    Creates a fresh session (with the opencode default agent — the
    CommandPrompter-scoped ``docbot`` persona is not appropriate for
    codebase analysis), prompts the server to write the ``AGENT.md``,
    and best-effort-deletes the session on the way out. The
    provider/model/variant are the same ones the answering flow is
    configured for so the same model is used everywhere.

    Args:
        client: The :class:`OpencodeClient` talking to the
            ``opencode serve`` subprocess.
        repo_dir: Absolute path to the repository whose ``AGENT.md`` to
            generate. Embedded in the prompt so the agent writes to
            the right place.
        provider_id: Provider ID forwarded to the opencode server
            (e.g. ``"opencode"``).
        model_id: Bare model id forwarded to the opencode server
            (e.g. ``"deepseek-v4-flash-free"``).
        variant: Optional reasoning-effort variant forwarded to the
            opencode server (``None`` for the model's default).

    Returns:
        ``True`` on success, ``False`` if any HTTP call failed (already
        logged). The session is best-effort-deleted in both cases.
    """
    session_id: str | None = None
    try:
        session_id = await client.create_session(
            title=f"agent-md:{repo_dir.name}",
            provider_id=provider_id,
            model_id=model_id,
            # agent omitted on purpose — use opencode's default
        )
        prompt_text: str = _AGENT_MD_PROMPT_TEMPLATE.format(
            repo_dir=str(repo_dir)
        )
        await client.prompt(
            session_id=session_id,
            parts=[{"type": "text", "text": prompt_text}],
            provider_id=provider_id,
            model_id=model_id,
            variant=variant,
        )
        _logger.info("Successfully generated AGENT.md for %s", repo_dir)
        return True
    except OpencodeClientError as exc:
        _logger.error(
            "Failed to generate AGENT.md for %s: %s", repo_dir, exc
        )
        return False
    except Exception:
        _logger.exception(
            "Unexpected error generating AGENT.md for %s", repo_dir
        )
        return False
    finally:
        if session_id is not None:
            try:
                await client.delete_session(session_id)
            except Exception:
                # Best-effort: a stray session on the server is
                # cosmetic; the 30-min sweeper will reap it.
                _logger.warning(
                    "Failed to delete AGENT.md session %s for %s",
                    session_id,
                    repo_dir,
                    exc_info=True,
                )


async def poll_repositories(
    repo_manager: RepositoryManager,
    repo_urls: Sequence[str],
    repos_root: Path,
    token: str | None = None,
    *,
    client: OpencodeClient,
    provider_id: str,
    model_id: str,
    variant: str | None = None,
) -> None:
    """Synchronize all configured repositories into ``repos_root``.

    Iterates over ``repo_urls`` in order, cloning or pulling each into
    ``repos_root/<repo_name>`` (where ``<repo_name>`` is derived by
    :func:`derive_repo_name`). If a repository is freshly cloned or
    updated, the opencode HTTP API is invoked (via the injected
    :class:`OpencodeClient`) to generate an ``AGENT.md`` file in the
    repository's root directory. The same provider/model/variant the
    answering flow uses is forwarded so the generation uses the
    configured model.

    Args:
        repo_manager: Cloner/puller used to sync each repository.
        repo_urls: Repository URLs to synchronize this cycle.
        repos_root: Root directory under which clones are stored.
        token: Optional GitHub token used to authenticate when
            cloning/pulling private repositories.
        client: HTTP client for the ``opencode serve`` subprocess.
            Used to create a throwaway session, prompt the model to
            write the ``AGENT.md``, and delete the session.
        provider_id: Provider ID forwarded to the opencode server.
        model_id: Bare model id forwarded to the opencode server.
        variant: Optional reasoning-effort variant forwarded to the
            opencode server.
    """
    root: Path = Path(repos_root)
    repo_names: list[str] = [derive_repo_name(url) for url in repo_urls]

    if not repo_names:
        _logger.info("No repositories configured; nothing to pull")
        return

    for url, name in zip(repo_urls, repo_names, strict=True):
        dest: Path = root / name
        _logger.info("Polling %s -> %s", url, dest)
        injected_url: str = inject_github_token(url, token)
        if repo_manager.clone_or_pull(injected_url, dest):
            _logger.info(
                "Running AGENT.md generation preprocessing step for %s",
                dest,
            )
            await _generate_agent_md(
                client,
                repo_dir=dest,
                provider_id=provider_id,
                model_id=model_id,
                variant=variant,
            )


def build_polling_task(
    repo_manager: RepositoryManager,
    repo_urls: Sequence[str],
    poll_interval_minutes: int,
    repos_root: Path = DEFAULT_REPOS_ROOT,
    token: str | None = None,
    *,
    client: OpencodeClient,
    provider_id: str,
    model_id: str,
    variant: str | None = None,
) -> tasks.Loop:
    """Build the repository polling background task.

    The returned :class:`discord.ext.tasks.Loop` is configured to run
    every ``poll_interval_minutes`` minutes but is **not** started. The
    caller is responsible for invoking ``loop.start()`` after the Discord
    client is ready (typically from a ``setup_hook`` or ``on_ready``
    hook) and ``loop.cancel()`` on shutdown.

    Args:
        repo_manager: Cloner/puller used to sync each repository.
        repo_urls: Repository URLs to synchronize each cycle.
        poll_interval_minutes: Minutes between polling cycles.
        repos_root: Root directory under which clones are stored.
            Defaults to :data:`DEFAULT_REPOS_ROOT`.
        token: Optional GitHub token used to authenticate when
            cloning/pulling private repositories.
        client: HTTP client for the ``opencode serve`` subprocess,
            forwarded to :func:`poll_repositories`.
        provider_id: Provider ID forwarded to the opencode server.
        model_id: Bare model id forwarded to the opencode server.
        variant: Optional reasoning-effort variant forwarded to the
            opencode server.

    Returns:
        A configured :class:`discord.ext.tasks.Loop` (not yet started).
    """
    @tasks.loop(minutes=poll_interval_minutes)
    async def _poll() -> None:
        await poll_repositories(
            repo_manager,
            repo_urls,
            repos_root,
            token=token,
            client=client,
            provider_id=provider_id,
            model_id=model_id,
            variant=variant,
        )

    return _poll
