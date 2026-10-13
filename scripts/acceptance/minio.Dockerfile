# CI-only packaging of an unmodified official MinIO source build.
# Source: minio/minio RELEASE.2025-10-15T17-29-55Z
# Commit: 9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a
# AGPL-3.0 and upstream notices are retained. This image is never published.
FROM scratch
COPY minio /minio
COPY LICENSE NOTICE /licenses/minio/
USER 65532:65532
ENV HOME=/tmp
ENTRYPOINT ["/minio"]
