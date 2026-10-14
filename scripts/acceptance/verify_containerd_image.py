#!/usr/bin/env python3
"""Verify the pinned public Docker Official Image without executing it."""
import hashlib
import json
import re
import sys
import urllib.request


def verify_image(image):
    prefix = "docker.io/library/alpine@sha256:"
    if not image.startswith(prefix) or not re.fullmatch(r"[0-9a-f]{64}", image.removeprefix(prefix)):
        raise ValueError("only the pinned Docker Official Alpine digest is supported")
    digest = image.split("@", 1)[1]
    # This short-lived anonymous public-pull token is neither printed nor saved.
    with urllib.request.urlopen("https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/alpine:pull", timeout=30) as response:
        token = json.load(response)["token"]
    request = urllib.request.Request(
        "https://registry-1.docker.io/v2/library/alpine/manifests/" + digest,
        headers={"Authorization": "Bearer " + token, "Accept": "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"},
    )
    with urllib.request.urlopen(request, timeout=30) as response:
        content = response.read(1024 * 1024)
        if response.headers.get("Docker-Content-Digest") != digest or "sha256:" + hashlib.sha256(content).hexdigest() != digest:
            raise ValueError("official registry manifest digest mismatch")
    manifest = json.loads(content)
    config_digest = manifest["config"]["digest"]
    config_request = urllib.request.Request(
        "https://registry-1.docker.io/v2/library/alpine/blobs/" + config_digest,
        headers={"Authorization": "Bearer " + token},
    )
    with urllib.request.urlopen(config_request, timeout=30) as response:
        content = response.read(1024 * 1024)
        if "sha256:" + hashlib.sha256(content).hexdigest() != config_digest:
            raise ValueError("official registry image config digest mismatch")
    config = json.loads(content)
    if config.get("os") != "linux" or config.get("architecture") != "amd64":
        raise ValueError("acceptance image must be linux/amd64")
    print(json.dumps({"image": image, "os": config["os"], "architecture": config["architecture"], "manifestVerified": True, "configVerified": True}, indent=2))


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("usage: verify_containerd_image.py image@sha256:digest")
    verify_image(sys.argv[1])
