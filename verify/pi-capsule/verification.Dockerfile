FROM golang:1.27.0-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/capsule-provider ./verification/cmd/capsule-provider \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/capsule-isolation ./verification/cmd/capsule-isolation \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/capsule-control ./verification/cmd/capsule-control \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ref0 ./cmd/ref0

FROM scratch AS provider
COPY --from=build /out/capsule-provider /capsule-provider
USER 1002:1002
ENTRYPOINT ["/capsule-provider"]

FROM scratch AS isolation
COPY --from=build /out/capsule-isolation /capsule-isolation
ENTRYPOINT ["/capsule-isolation"]

FROM debian:bookworm-slim AS control
COPY --from=build /out/capsule-control /usr/local/bin/capsule-control
COPY --from=build /out/ref0 /usr/local/bin/ref0
CMD ["/usr/local/bin/capsule-control"]
