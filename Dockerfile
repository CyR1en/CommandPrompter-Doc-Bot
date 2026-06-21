# CMDP Doc Bot — container image
#
# Python image that runs the Discord documentation bot. The bot starts
# a single long-lived ``opencode serve`` subprocess at boot and talks
# to its HTTP API for per-user session management, so the image needs
# the ``opencode`` CLI (installed via its official one-line installer)
# in addition to ``git`` (for GitPython-driven repository syncing) and
# the Python dependencies.

FROM python:3.11-slim

# Keep Python output unbuffered so logs stream in real time, skip writing
# .pyc files, and tell pip not to cache downloads so the layer stays small.
ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

# System dependencies required at runtime.
#   git              — GitPython shells out to the ``git`` binary.
#   ca-certificates  — needed for HTTPS git operations.
#   curl             — used by the OpenCode install script.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        git \
        ca-certificates \
        curl \
    && rm -rf /var/lib/apt/lists/*

# Install the OpenCode CLI globally using the official install script.
# OpenCode is the agent orchestrator that the bot spawns as a subprocess
# to answer each question. The script downloads a self-contained
# ``opencode`` binary onto the user's PATH (commonly ``~/.opencode/bin``),
# so that directory is prepended to ``PATH`` to make the binary
# resolvable to subsequent ``RUN`` steps and the final entrypoint.
ENV PATH="/root/.opencode/bin:${PATH}"
RUN curl -fsSL https://opencode.ai/install | bash

# Ensure the OpenCode per-user config directory and agents subdirectory
# exist so ``setup_opencode`` can write ``opencode.json`` and the
# ``docbot.md`` agent definition at runtime.
RUN mkdir -p ~/.config/opencode/agents

WORKDIR /app

# Install Python dependencies first so this layer is cached across code
# changes and only rebuilt when requirements.txt changes.
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy the application code.
COPY bot/ ./bot/
COPY core/ ./core/
COPY agent/ ./agent/
COPY main.py ./main.py

# Persistent state (cloned repos) lives under /app/data and is mounted as
# a volume at runtime so it survives container restarts.
RUN mkdir -p /app/data
VOLUME ["/app/data"]

# Run the bot.
ENTRYPOINT ["python", "main.py"]
