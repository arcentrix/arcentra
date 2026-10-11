# Copyright 2026 Arcentra Authors. Licensed under the Apache License, Version 2.0.
FROM golang:1.25.6-bookworm AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Generated protobuf and Wire files are intentionally absent from source control.
RUN make codegen && make build-target TARGET=arcentra && \
    make build-target TARGET=arcentra-agent && make build-target TARGET=cli

# Both processes use git; an execution agent also needs a real shell.
FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git bash && \
    rm -rf /var/lib/apt/lists/* && mkdir -p /conf.d /var/lib/arcentra/workspace /var/lib/arcentra/temp
WORKDIR /var/lib/arcentra

FROM runtime AS arcentra
COPY --from=builder /app/arcentra /usr/local/bin/arcentra
COPY --from=builder /app/cli /usr/local/bin/arcentra-cli
EXPOSE 8080 9090
ENTRYPOINT ["/usr/local/bin/arcentra", "-conf", "/conf.d/config.toml", "-plugin-conf", "/conf.d/plugins.toml"]

FROM runtime AS arcentra-agent
COPY --from=builder /app/arcentra-agent /usr/local/bin/arcentra-agent
ENTRYPOINT ["/usr/local/bin/arcentra-agent", "-conf", "/conf.d/agent.toml"]
