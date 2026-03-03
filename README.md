<div align="center">

# Arcentra

[![GitHub Repository](https://img.shields.io/badge/GitHub-Repository-black.svg?logo=github)](https://github.com/arcentrix/arcentra)
[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8.svg?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-Apache%202.0-red.svg?logo=apache)](./LICENSE)
[![Last Commit](https://img.shields.io/github/last-commit/arcentrix/arcentra)](https://github.com/arcentrix/arcentra)
[![GitHub Stars](https://img.shields.io/github/stars/arcentrix/arcentra?style=flat&logo=github&color=yellow&label=Star)](https://github.com/arcentrix/arcentra/stargazers)
[![GitHub Forks](https://img.shields.io/github/forks/arcentrix/arcentra?style=flat&logo=github&color=purple&label=Fork)](https://github.com/arcentrix/arcentra/network)

[[English](./README.md)] [[简体中文](./README_zh_CN.md)]

**A Cloud-native CI/CD Control Plane for modern engineering systems.**

</div>

Arcentra is an open-source CI/CD control plane built for teams that need centralized orchestration with distributed execution.
It provides a stable architectural center for pipeline governance, agent scheduling, and long-term automation evolution.

## Why Arcentra

- **Control plane first**: model pipelines, runs, and state in one coherent system
- **Distributed execution**: schedule work to heterogeneous agents and resource pools
- **Cloud-native architecture**: designed for Kubernetes and scalable runtime topologies
- **Governance-ready**: observability, auditability, and consistent workflow operations
- **Extensible by design**: API-first and plugin-oriented integration model

## Core Capabilities

- **Pipeline orchestration**
  - Multi-stage and DAG-based workflows
  - Decoupled pipeline definition and execution
- **Agent scheduling**
  - Central control with distributed task execution
  - Support for mixed runtime environments
- **Observability and auditing**
  - Unified logs, metrics, and trace-friendly integration
  - End-to-end execution visibility
- **Plugin and action model**
  - Interface-driven extension points
  - Evolvable execution model for platform growth

## Quick Start

Prerequisites:

- Go (see `go.mod`)
- Optional: Docker for containerized build and run workflows

Common commands:

```bash
make build
make run
make lint
go test ./...
```

For full setup and contribution workflow, see [CONTRIBUTING.md](./CONTRIBUTING.md).

## Project Status

Arcentra is under active development.
Contributions, issue reports, feature proposals, and architecture discussions are welcome.

## Security

To report security vulnerabilities, see [SECURITY.md](./SECURITY.md).

## Code of Conduct

Please read [CODE_OF_CONDUCT.md](./CODE_OF_CONDUCT.md).

## License

Copyright 2025 The Arcentra Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at:

http://www.apache.org/licenses/LICENSE-2.0