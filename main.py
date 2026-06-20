"""Entry point for the CMDP Doc Bot.

Wires the core modules — OpenCode configuration, repository syncing, rate
limiting, and the OpenCode-backed LLM client — into a Discord client and
a background polling task, then runs the bot using ``DISCORD_TOKEN``.

The startup sequence is:

1. Load :class:`~core.config.Settings` (with a graceful message if the
   environment is not yet configured).
2. Generate the OpenCode configuration (``opencode.json`` + agent
   persona) via :func:`~core.opencode_config.setup_opencode`.
3. Build :class:`~core.git_manager.RepositoryManager`,
   :class:`~core.llm_client.LLMClient`, and
   :class:`~core.rate_limiter.RateLimiter`.
4. Build the :class:`~bot.client.DocBot`.
5. Build the repository polling task that keeps the cloned repositories
   under ``data/repos`` up to date.
6. Start the polling task from a ``setup_hook`` and run the bot.
"""

from __future__ import annotations

from pathlib import Path

from pydantic import ValidationError

from bot.client import DocBot
from bot.tasks import build_polling_task
from core.config import Settings, get_settings
from core.git_manager import RepositoryManager
from core.llm_client import LLMClient
from core.logger import get_logger, setup_logging
from core.opencode_config import setup_opencode
from core.rate_limiter import RateLimiter

#: Filesystem layout for persistent data.
_DATA_DIR: Path = Path("data")
_REPOS_ROOT: Path = _DATA_DIR / "repos"


def main() -> None:
    """Initialize all components and run the Discord bot.

    Loads settings, writes the OpenCode configuration, builds the
    repository manager, LLM client, and rate limiter, wires them into a
    :class:`DocBot` plus a polling task, then blocks on
    :meth:`discord.Client.run` until the bot is stopped. If required
    configuration is missing the function logs a guidance message and
    returns instead of raising, so the entry point can be run before a
    ``.env`` file is in place.
    """
    setup_logging()
    logger = get_logger(__name__)

    logger.info("CMDP Doc Bot starting up")

    try:
        settings: Settings = get_settings()
    except ValidationError as exc:
        logger.warning("Configuration incomplete: %s", exc)
        print(
            "CMDP Doc Bot: configuration incomplete. "
            "Copy .env.example to .env and fill in the required values."
        )
        return

    logger.info("Configured repositories: %d", len(settings.REPO_URLS))
    logger.info(
        "Poll interval: %d minute(s)", settings.POLL_INTERVAL_MINUTES
    )

    # --- OpenCode configuration -----------------------------------------
    setup_opencode(settings)

    # --- Core components ------------------------------------------------
    repo_manager = RepositoryManager()
    rate_limiter = RateLimiter()
    llm_client = LLMClient(working_dir=_REPOS_ROOT)

    # --- Discord client -------------------------------------------------
    bot = DocBot(
        rate_limiter=rate_limiter,
        llm_client=llm_client,
    )

    # --- Repository polling task ----------------------------------------
    poll_task = build_polling_task(
        repo_manager=repo_manager,
        repo_urls=settings.REPO_URLS,
        poll_interval_minutes=settings.POLL_INTERVAL_MINUTES,
        repos_root=_REPOS_ROOT,
        token=settings.GITHUB_TOKEN,
    )

    # ``setup_hook`` is the idiomatic place to start background tasks in
    # discord.py 2.x: it runs once, after login but before the gateway
    # connection is established. It is assigned as an instance attribute
    # (a no-argument coroutine) so it shadows the default no-op hook
    # without colliding with ``DocBot.on_ready``.
    async def _start_polling() -> None:
        if not poll_task.is_running():
            logger.info("Starting repository polling task")
            poll_task.start()

    bot.setup_hook = _start_polling  # type: ignore[assignment]

    try:
        bot.run(settings.DISCORD_TOKEN)
    finally:
        if poll_task.is_running():
            try:
                poll_task.cancel()
            except RuntimeError:
                # The event loop may already be closed during shutdown.
                logger.debug("Polling task cancel skipped: loop closed")


if __name__ == "__main__":
    main()
