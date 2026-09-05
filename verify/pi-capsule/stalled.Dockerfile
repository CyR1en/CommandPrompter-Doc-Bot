FROM golang:1.27.0-bookworm AS supervisor-build
WORKDIR /src
COPY capsule/supervisor/main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /capsule-supervisor main.go

FROM debian:bookworm-slim AS child-build
RUN apt-get update \
    && apt-get install -y --no-install-recommends gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*
COPY verify/pi-capsule/stalled-child.c /src/stalled-child.c
RUN gcc -static -O2 -Wall -Wextra -Werror -o /stalled-child /src/stalled-child.c

FROM debian:bookworm-slim
RUN groupadd --gid 1001 capsule \
    && groupadd --gid 2000 capsule-worker \
    && useradd --uid 1001 --gid capsule --home-dir /nonexistent --shell /usr/sbin/nologin capsule \
    && mkdir -p /opt/verify /run/capsule \
    && chown root:capsule-worker /run/capsule \
    && chmod 2750 /run/capsule
COPY --from=supervisor-build /capsule-supervisor /usr/local/bin/capsule-supervisor
COPY --from=child-build /stalled-child /opt/verify/stalled-child
HEALTHCHECK --interval=2s --timeout=1s --retries=15 CMD ["/usr/local/bin/capsule-supervisor", "--check"]
ENTRYPOINT ["/usr/local/bin/capsule-supervisor"]
CMD ["--child", "/opt/verify/stalled-child"]
