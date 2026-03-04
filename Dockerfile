# Copyright 2026 Arcentra Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

FROM golang:1.25.6 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY ./ /app/

ARG TARGET=arcentra
RUN apt-get update && apt-get install -y --no-install-recommends unzip && rm -rf /var/lib/apt/lists/* && \
    make build-target TARGET=$TARGET

FROM gcr.io/distroless/base-debian12 AS arcentra

WORKDIR /conf.d

COPY --from=builder /app/arcentra /arcentra

EXPOSE 8080

ENTRYPOINT ["/arcentra", "-conf", "/conf.d/config.toml"]

FROM gcr.io/distroless/base-debian12 AS arcentra-agent

WORKDIR /conf.d

COPY --from=builder /app/arcentra-agent /arcentra-agent

ENTRYPOINT ["/arcentra-agent"]
