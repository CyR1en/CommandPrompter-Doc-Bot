"""Background repository polling task for the CMDP Doc Bot.

This module wires :class:`~core.git_manager.RepositoryManager` into a
periodic background task built on :class:`discord.ext.tasks.loop`. On
every cycle each configured repository is cloned or pulled into
``data/repos`` so the OpenCode ``docbot`` agent can read the latest
documentation directly from disk when answering user questions.

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

_logger = get_logger(__name__)

#: Default root directory under which repository clones are stored.
DEFAULT_REPOS_ROOT: Path = Path("data/repos")


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


async def poll_repositories(
    repo_manager: RepositoryManager,
    repo_urls: Sequence[str],
    repos_root: Path,
    token: str | None = None,
) -> None:
    """Synchronize all configured repositories into ``repos_root``.

    Iterates over ``repo_urls`` in order, cloning or pulling each into
    ``repos_root/<repo_name>`` (where ``<repo_name>`` is derived by
    :func:`derive_repo_name`). If a repository is freshly cloned or updated,
    an OpenCode subprocess is spawned to generate an ``AGENT.md`` file in the
    repository's root directory.

    Args:
        repo_manager: Cloner/puller used to sync each repository.
        repo_urls: Repository URLs to synchronize this cycle.
        repos_root: Root directory under which clones are stored.
        token: Optional GitHub token used to authenticate when
            cloning/pulling private repositories.
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
            _logger.info("Running AGENT.md generation preprocessing step for %s", dest)
            try:
                proc = await asyncio.create_subprocess_exec(
                    "opencode",
                    "run",
                    "--dangerously-skip-permissions",
                    "--dir",
                    str(dest),
                    """
                    Analyze this repository and generate an overview of its structure, purpose, and key files.
                    The main goal of this file is to help the docbot agent quickly understand the repository's contents and how to navigate it when answering user questions.

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

                    Save this overview to a file named AGENT.md in the root of the repository. Overwrite the file if it already exists.
                    Try keeping this file as short as possible while still providing a useful overview, ideally under 250 lines.
                    """,
                    stdout=asyncio.subprocess.PIPE,
                    stderr=asyncio.subprocess.PIPE,
                )
                stdout, stderr = await proc.communicate()
                if proc.returncode == 0:
                    _logger.info("Successfully generated AGENT.md for %s", dest)
                else:
                    _logger.error(
                        "Failed to generate AGENT.md for %s (exit code %d). Stderr: %s",
                        dest,
                        proc.returncode,
                        stderr.decode(errors="replace").strip(),
                    )
            except Exception as e:
                _logger.exception("Failed to run AGENT.md generation for %s", dest)


def build_polling_task(
    repo_manager: RepositoryManager,
    repo_urls: Sequence[str],
    poll_interval_minutes: int,
    repos_root: Path = DEFAULT_REPOS_ROOT,
    token: str | None = None,
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
        )

    return _poll
