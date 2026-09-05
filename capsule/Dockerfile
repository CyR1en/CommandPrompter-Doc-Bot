FROM golang:1.27.0-bookworm AS supervisor-build
WORKDIR /src
COPY capsule/supervisor/main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /capsule-supervisor main.go

FROM node:22.19.0-bookworm-slim AS capsule-build
WORKDIR /build
COPY capsule/package.json capsule/package-lock.json ./
RUN npm ci --ignore-scripts
COPY capsule/tsconfig.json capsule/wire.schema.json ./
COPY capsule/src ./src
COPY capsule/test ./test
RUN npm run format:check \
    && npm run typecheck \
    && npm run build \
    && npm test \
    && npm prune --omit=dev --ignore-scripts

FROM debian:bookworm-slim AS hostile-build
RUN apt-get update \
    && apt-get install -y --no-install-recommends gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*
COPY verify/pi-capsule/hostile-child.c /src/hostile-child.c
RUN gcc -static -O2 -Wall -Wextra -Werror -o /hostile-child /src/hostile-child.c

FROM node:22.19.0-bookworm-slim AS runtime
LABEL org.opencontainers.image.version="pi-0.84.4-r9"
RUN apt-get update \
    && apt-get upgrade -y \
    && rm -rf /var/lib/apt/lists/* \
        /usr/local/lib/node_modules/npm \
        /usr/local/lib/node_modules/corepack \
    && rm -f /usr/local/bin/npm /usr/local/bin/npx \
        /usr/local/bin/corepack /usr/local/bin/yarn /usr/local/bin/yarnpkg \
    && groupadd --gid 1001 capsule \
    && groupadd --gid 2000 capsule-worker \
    && useradd --uid 1001 --gid capsule --home-dir /nonexistent --shell /usr/sbin/nologin capsule \
    && mkdir -p /opt/capsule /run/capsule \
    && chown root:capsule-worker /run/capsule \
    && chmod 2750 /run/capsule
COPY --from=supervisor-build /capsule-supervisor /usr/local/bin/capsule-supervisor
COPY --from=capsule-build /build/node_modules /opt/capsule/node_modules
COPY --from=capsule-build /build/dist/src /opt/capsule/dist/src
COPY capsule/wire.schema.json /opt/capsule/wire.schema.json
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/licenses/ref0/
HEALTHCHECK --interval=2s --timeout=1s --retries=15 CMD ["/usr/local/bin/capsule-supervisor", "--check"]
ENTRYPOINT ["/usr/local/bin/capsule-supervisor"]

FROM runtime AS verification
COPY --from=hostile-build /hostile-child /opt/verify/hostile-child
CMD ["--child", "/opt/verify/hostile-child"]

FROM runtime AS production
