---
name: Bug Report
about: Report a bug or unexpected behavior
title: "[Bug]: "
labels: ["bug", "triage"]
assignees: []
---

## Description

[Describe the bug clearly and concisely. What happened and what did you expect?]

**Related PR(s):** #(optional)

**Security considerations:** [If applicable, e.g. authn/authz, secrets handling, data exposure]

## Component(s) Affected

- [ ] API contracts (`api/**`, protobuf, gRPC)
- [ ] Pipeline engine (`internal/control/**`)
- [ ] Agent / executor / plugin system
- [ ] Storage / repository layer
- [ ] Observability (logs/metrics/tracing)
- [ ] CLI / tooling / scripts
- [ ] CI workflow / release process
- [ ] Documentation

## Environment

- **Arcentra version:** [e.g. tag or git commit]
- **OS:** [e.g. macOS 14, Ubuntu 24.04, AlmaLinux 9]
- **Deploy mode:** [local / Docker / Kubernetes / bare metal]
- **Go version (if building from source):** [e.g. 1.24.x]

## Steps to Reproduce

1.
2.
3.

## Actual vs Expected

- **Actual:**
- **Expected:**

## Logs / Screenshots

[Paste relevant log output or attach screenshots. Use code blocks for logs.]

```
(paste logs here)
```

## Additional Notes

[Optional: workarounds, similar issues, etc.]
