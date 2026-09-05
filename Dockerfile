FROM node:24.20.0-bookworm-slim AS frontend-build

WORKDIR /frontend
COPY frontend/package.json frontend/package-lock.json frontend/.npmrc ./
RUN npm ci --ignore-scripts
COPY frontend/ ./
RUN npm run build

FROM golang:1.27.0-bookworm AS go-build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY db/ ./db/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux go build \
    -buildvcs=false -trimpath -ldflags="-s -w" \
    -o /out/ref0 ./cmd/ref0

FROM debian:bookworm-slim AS production

RUN apt-get update \
    && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends ca-certificates git wget \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 2000 ref0 \
    && useradd --system --uid 1000 --gid ref0 --home-dir /app --shell /usr/sbin/nologin ref0 \
    && mkdir -p /app/data /app/frontend/dist /app/capsule \
    && chown -R ref0:ref0 /app

WORKDIR /app
COPY --from=go-build /out/ref0 /usr/local/bin/ref0
COPY --from=frontend-build /frontend/dist/ ./frontend/dist/
COPY capsule/wire.schema.json ./capsule/wire.schema.json
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/licenses/ref0/

USER ref0
VOLUME ["/app/data"]

ENTRYPOINT ["/usr/local/bin/ref0"]
CMD ["api"]
