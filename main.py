"""Entry point for the CMDP Doc Bot.

Wires the core modules — OpenCode server, HTTP client, per-user session
manager, OpenCode configuration, repository syncing, rate limiting, and
the OpenCode-backed LLM client — into a Discord client and two
background tasks (repository polling + expired-session sweeping), then
runs the bot using ``DISCORD_TOKEN``.

The startup sequence is:

1. Load :class:`~core.config.Settings` (with a graceful message if the
   environment is not yet configured).
2. Map ``LLM_API_KEY`` to the active provider's declared env var (e.g.
   ``OPENCODE_API_KEY`` for the ``opencode`` provider) so OpenCode's
   built-in provider SDK picks the key up automatically.
3. Generate the OpenCode configuration (``opencode.json`` + agent
   persona) via :func:`~core.opencode_config.setup_opencode`.
4. Start the long-lived ``opencode serve`` subprocess via
   :class:`~core.opencode_server.OpencodeServer` and build the
   :class:`~core.opencode_client.OpencodeClient` HTTP client that
   talks to it.
5. Build the :class:`~core.session_manager.SessionManager` that maps
   Discord users to opencode sessions.
6. Build :class:`~core.git_manager.RepositoryManager`,
   :class:`~core.llm_client.LLMClient`, and
   :class:`~core.rate_limiter.RateLimiter`.
7. Build the :class:`DocBot`.
8. Build the repository polling task and the expired-session sweeper
   task.
9. Start both tasks from a ``setup_hook`` and run the bot.

Shutdown (in the ``finally`` block) cancels both tasks, stops the
opencode server, and closes the HTTP client.
"""

from __future__ import annotations

import logging
import os
from datetime import timedelta
from pathlib import Path

from discord.ext import tasks
from pydantic import ValidationError

from bot.client import DocBot
from bot.tasks import build_polling_task
from core.config import Settings, get_settings
from core.git_manager import RepositoryManager
from core.llm_client import LLMClient
from core.logger import get_logger, setup_logging
from core.opencode_client import OpencodeClient
from core.opencode_config import setup_opencode
from core.opencode_server import OpencodeServer
from core.rate_limiter import RateLimiter
from core.session_manager import SessionManager

#: Filesystem layout for persistent data.
_DATA_DIR: Path = Path("data")
_REPOS_ROOT: Path = _DATA_DIR / "repos"

#: Mapping from a built-in OpenCode provider ID (as known to
#: https://models.dev/api.json) to the env var its SDK reads the API key
#: from. At startup ``LLM_API_KEY`` is copied into the matching env var
#: via ``os.environ.setdefault`` (an explicit value already in the
#: environment wins). Providers that authenticate via OAuth or
#: cloud-native credentials (``github-copilot``, ``google-vertex``,
#: ``amazon-bedrock``) are intentionally absent: users are expected to
#: run ``opencode auth login`` once on the host or set the appropriate
#: env vars themselves. The legacy ``custom-llm`` shim maps to an empty
#: string as a sentinel — its key is written into ``opencode.json`` by
#: :func:`setup_opencode`, not an env var.
_PROVIDER_ENV_VAR: dict[str, str] = {
    "opencode": "OPENCODE_API_KEY",
    "anthropic": "ANTHROPIC_API_KEY",
    "openai": "OPENAI_API_KEY",
    "google": "GOOGLE_GENERATIVE_AI_API_KEY",
    "groq": "GROQ_API_KEY",
    "mistral": "MISTRAL_API_KEY",
    "deepseek": "DEEPSEEK_API_KEY",
    "openrouter": "OPENROUTER_API_KEY",
    "xai": "XAI_API_KEY",
    "cohere": "COHERE_API_KEY",
    "vercel": "AI_GATEWAY_API_KEY",
    # ``azure`` additionally requires ``AZURE_RESOURCE_NAME`` in the
    # environment (the Azure resource name is used to build endpoint
    # URLs). ``LLM_API_KEY`` only carries the API key, so azure users
    # must set ``AZURE_RESOURCE_NAME`` themselves in addition.
    "azure": "AZURE_API_KEY",
    "perplexity": "PERPLEXITY_API_KEY",
    "deepinfra": "DEEPINFRA_API_KEY",
    "togetherai": "TOGETHER_API_KEY",
    "cerebras": "CEREBRAS_API_KEY",
    "custom-llm": "",  # sentinel: no env-var mapping (key goes into opencode.json)
}


def _publish_provider_env_var(
    settings: Settings, logger: logging.Logger
) -> None:
    """Map ``LLM_API_KEY`` to the active provider's declared env var.

    For built-in providers (``opencode``, ``anthropic``, ``openai`` ...)
    the key is published via ``os.environ.setdefault`` so OpenCode's
    provider SDK picks it up automatically — an explicit env var already
    set in the environment wins over ``LLM_API_KEY``. For the legacy
    ``custom-llm`` shim (sentinel empty-string mapping) the call is
    skipped: the key is written into ``opencode.json`` by
    :func:`setup_opencode` instead. For providers not listed in
    :data:`_PROVIDER_ENV_VAR` (e.g. ``github-copilot`` which uses OAuth,
    ``google-vertex`` / ``amazon-bedrock`` which use ADC / AWS creds) a
    warning is logged and no env var is set — the user is expected to
    have authenticated via ``opencode auth login`` or set the env var
    themselves.

    Args:
        settings: The loaded application :class:`Settings`.
        logger: The module logger used for the unknown-provider warning.
    """
    env_var: str | None = _PROVIDER_ENV_VAR.get(settings.LLM_PROVIDER)
    if env_var is None:
        # Unknown provider — cannot map the key automatically. The user
        # is expected to have run `opencode auth login` or set the env
        # var themselves.
        logger.warning(
            "LLM_PROVIDER=%r is not in the built-in env-var map; "
            "skipping automatic LLM_API_KEY mapping. Run "
            "`opencode auth login` or set the provider's API-key env "
            "var yourself before starting the bot.",
            settings.LLM_PROVIDER,
        )
        return
    if env_var == "":
        # Sentinel for the custom-llm shim: the key goes into the
        # opencode.json provider block, not an env var.
        return
    os.environ.setdefault(env_var, settings.LLM_API_KEY)


def build_session_sweeper_task(
    session_manager: SessionManager,
    client: OpencodeClient,
    interval_minutes: int,
) -> tasks.Loop:
    """Build the expired-session sweeper background task.

    The returned :class:`discord.ext.tasks.Loop` runs every
    ``interval_minutes`` minutes, asks the session manager for the
    snapshot of expired entries, deletes each one on the opencode
    server, and removes it from the in-memory mapping. The loop is
    **not** started; the caller starts it from ``setup_hook`` and
    cancels it on shutdown.

    Args:
        session_manager: The :class:`SessionManager` to sweep.
        client: The :class:`OpencodeClient` used to delete sessions on
            the server.
        interval_minutes: Minutes between sweep cycles.

    Returns:
        A configured :class:`discord.ext.tasks.Loop` (not yet started).
    """
    logger = get_logger(__name__)

    @tasks.loop(minutes=interval_minutes)
    async def _sweep() -> None:
        expired = session_manager.cleanup_expired()
        if not expired:
            return
        for user_id, session_id in expired:
            try:
                await client.delete_session(session_id)
            except Exception:
                logger.warning(
                    "Failed to delete session %s for user %s",
                    session_id,
                    user_id,
                    exc_info=True,
                )
            # Pass ``session_id`` so we do not clobber an entry that was
            # refreshed (new session created) between this snapshot and
            # the now-completed delete.
            session_manager.remove(user_id, session_id)
        logger.info("Swept %d expired session(s)", len(expired))

    return _sweep


async def _run_bot(settings: Settings, logger: logging.Logger) -> None:
    """Build and run the bot with the new server + session architecture.

    Encapsulates the post-settings-load wiring so :func:`main` stays
    focused on the configuration-load-or-bail step. The caller is
    responsible for ``setup_logging`` and the ``ValidationError`` guard.

    Args:
        settings: The loaded application :class:`Settings`.
        logger: The module logger.
    """
    # --- Provider API-key env-var mapping -------------------------------
    # Must run BEFORE setup_opencode so the key is visible if any
    # downstream step inspects the environment.
    _publish_provider_env_var(settings, logger)

    # --- OpenCode configuration -----------------------------------------
    setup_opencode(settings)

    # --- opencode server + HTTP client ----------------------------------
    server = OpencodeServer(
        host=settings.OPENCODE_SERVER_HOST,
        port=settings.OPENCODE_SERVER_PORT,
        password=settings.OPENCODE_SERVER_PASSWORD,
        cwd=_REPOS_ROOT,
    )
    await server.start()
    client = OpencodeClient(
        base_url=server.base_url,
        password=server.password,
    )

    # --- Session manager ------------------------------------------------
    session_manager = SessionManager(
        client=client,
        ttl=timedelta(minutes=settings.SESSION_TTL_MINUTES),
    )

    # --- Core components ------------------------------------------------
    repo_manager = RepositoryManager()
    rate_limiter = RateLimiter()
    llm_client = LLMClient(
        client=client,
        agent="docbot",
        provider_id=settings.LLM_PROVIDER,
        model_id=settings.LLM_MODEL,
        variant=settings.LLM_VARIANT,
    )

    # --- Discord client -------------------------------------------------
    bot = DocBot(
        rate_limiter=rate_limiter,
        llm_client=llm_client,
        session_manager=session_manager,
        provider_id=settings.LLM_PROVIDER,
        model_id=settings.LLM_MODEL,
        variant=settings.LLM_VARIANT,
    )

    # --- Background tasks -----------------------------------------------
    # The validator in Settings guarantees AGENT_MD_PROVIDER and AGENT_MD_MODEL
    # are resolved to non-None values (falling back to the LLM_* settings).
    assert settings.AGENT_MD_PROVIDER is not None
    assert settings.AGENT_MD_MODEL is not None

    poll_task = build_polling_task(
        repo_manager=repo_manager,
        repo_urls=settings.REPO_URLS,
        poll_interval_minutes=settings.POLL_INTERVAL_MINUTES,
        repos_root=_REPOS_ROOT,
        token=settings.GITHUB_TOKEN,
        client=client,
        provider_id=settings.AGENT_MD_PROVIDER,
        model_id=settings.AGENT_MD_MODEL,
        variant=settings.AGENT_MD_VARIANT,
    )
    sweep_task = build_session_sweeper_task(
        session_manager=session_manager,
        client=client,
        interval_minutes=1,
    )

    # ``setup_hook`` is the idiomatic place to start background tasks in
    # discord.py 2.x: it runs once, after login but before the gateway
    # connection is established. It is assigned as an instance attribute
    # (a no-argument coroutine) so it shadows the default no-op hook
    # without colliding with ``DocBot.on_ready``.
    async def _start_background_tasks() -> None:
        if not poll_task.is_running():
            logger.info("Starting repository polling task")
            poll_task.start()
        if not sweep_task.is_running():
            logger.info("Starting expired-session sweeper task")
            sweep_task.start()

    bot.setup_hook = _start_background_tasks  # type: ignore[assignment]

    try:
        # ``bot.run`` is the sync entry point and internally calls
        # ``asyncio.run`` — which would raise ``RuntimeError`` because
        # we are already inside an event loop (the one created by
        # ``asyncio.run(_run_bot(...))`` in :func:`main`). Use the
        # async counterpart ``bot.start`` instead, paired with an
        # explicit ``bot.close`` in the ``finally`` block.
        await bot.start(settings.DISCORD_TOKEN)
    finally:
        # --- Close the Discord connection -----------------------------
        if not bot.is_closed():
            try:
                await bot.close()
            except Exception:
                logger.exception("Failed to close Discord bot")

        # --- Shutdown background tasks ---------------------------------
        for task in (poll_task, sweep_task):
            if task.is_running():
                try:
                    task.cancel()
                except RuntimeError:
                    # The event loop may already be closed during
                    # shutdown.
                    logger.debug("Task cancel skipped: loop closed")

        # --- Stop the opencode server ----------------------------------
        if server.is_running:
            try:
                await server.stop()
            except Exception:
                logger.exception("Failed to stop opencode server")

        # --- Close the HTTP client -------------------------------------
        try:
            await client.close()
        except Exception:
            logger.exception("Failed to close opencode client")


def main() -> None:
    """Initialize all components and run the Discord bot.

    Loads settings, publishes the provider API key to the right env var,
    writes the OpenCode configuration, starts the opencode server,
    builds the session manager / repository manager / LLM client / rate
    limiter, wires them into a :class:`DocBot` plus two background
    tasks (repository polling + expired-session sweeping), then blocks
    on :meth:`discord.Client.run` until the bot is stopped. If required
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
    logger.info(
        "Session TTL: %d minute(s)", settings.SESSION_TTL_MINUTES
    )

    # ``main`` is synchronous (it is the module entry point) but the
    # new server/session wiring is async (``server.start()`` is a
    # coroutine). Bridge the two with ``asyncio.run`` so the synchronous
    # entry point contract is preserved.
    import asyncio

    try:
        asyncio.run(_run_bot(settings, logger))
    except KeyboardInterrupt:
        logger.info("Bot interrupted by user")


if __name__ == "__main__":
    main()
