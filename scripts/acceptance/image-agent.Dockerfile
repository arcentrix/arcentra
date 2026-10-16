# Test-only derivative. Stock Dockerfile.ci images never gain these tools.
# Official multi-architecture index pins; the builder fixes linux/amd64.
ARG AGENT_BASE
FROM golang:1.25.6-bookworm@sha256:f4490d7b261d73af4543c46ac6597d7d101b6e1755bcdd8c5159fda7046b6b3e AS go-tools
FROM python:3.11.14-slim-bookworm@sha256:65a93d69fa75478d554f4ad27c85c1e69fa184956261b4301ebaf6dbb0a3543d AS python-tools
FROM ${AGENT_BASE}
# Debian repositories remain time-varying: this is deliberately NOT hermetic apt.
# Record actual installed versions for diagnosis, including transitive dependencies.
RUN apt-get update && apt-get install -y --no-install-recommends \
    libbluetooth3 libbz2-1.0 libcrypt1 libdb5.3 libexpat1 libffi8 libgdbm6 libgdbm-compat4 \
    liblzma5 libncursesw6 libtcl8.6 libtk8.6 libreadline8 libsqlite3-0 libssl3 libuuid1 zlib1g \
    && mkdir -p /usr/local/share/arcentra-image-journey \
    && dpkg-query -W -f='${Package}\t${Version}\n' > /usr/local/share/arcentra-image-journey/debian-packages.tsv \
    && rm -rf /var/lib/apt/lists/*
COPY --from=go-tools /usr/local/go /usr/local/go
COPY --from=python-tools /usr/local/ /usr/local/
RUN ldconfig
# Deliberately inherit exact stock ENTRYPOINT, CMD and WORKDIR.
