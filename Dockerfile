FROM golang:1.24.4 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY ./ /app/

ARG TARGET=arcentra
RUN apt-get update && apt-get install -y --no-install-recommends unzip && rm -rf /var/lib/apt/lists/* && \
    make build-target TARGET=$TARGET

FROM gcr.io/distroless/static:nonroot AS arcentra

WORKDIR /conf.d

COPY --from=builder /app/arcentra /arcentra

EXPOSE 8080

ENTRYPOINT ["/arcentra", "-conf", "/conf.d/config.toml"]

FROM gcr.io/distroless/static:nonroot AS arcentra-agent

WORKDIR /conf.d

COPY --from=builder /app/arcentra-agent /arcentra-agent

ENTRYPOINT ["/arcentra-agent"]
