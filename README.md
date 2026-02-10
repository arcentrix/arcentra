# Arcentra

English | [简体中文](./README_zh_CN.md)
> **A Cloud-native CI/CD architecture hub and automation platform**

Arcentra is an open-source, cloud-native **CI/CD control plane** designed to orchestrate pipelines, schedule agents, and unify automation workflows at scale.

Rather than being another pipeline runner, Arcentra focuses on providing a **stable architectural center** for modern engineering systems — enabling teams to build, evolve, and govern their CI/CD and automation practices over time.

---

## Project Vision

Modern engineering environments are increasingly complex: repositories multiply, pipelines fragment, and execution environments diversify. While tools are abundant, **a unifying control layer is often missing**.

Arcentra aims to fill this gap by acting as a **central coordination layer** for CI/CD and automation systems.

Arcentra is designed for teams who:

* Operate multiple repositories and pipelines
* Require distributed agent / runner execution
* Care about observability, auditability, and governance
* Want a long-lived platform rather than a short-term tool

---

## Name Origin

**Arcentra** is derived from two roots:

* **Arc** — Architecture, Flow, Lifecycle
* **Centra** — Center, Control, Hub

Together, the name represents:

> **An architectural center for orchestrating engineering workflows**

---

## Core Capabilities

Arcentra is built around a small set of durable abstractions:

* **Pipeline Orchestration**

  * Multi-stage, conditional, and DAG-based workflows
  * Decoupled pipeline definition and execution

* **Agent Scheduling**

  * Centralized control with distributed execution
  * Support for heterogeneous environments and resource pools

* **Control Plane Architecture**

  * Unified modeling of pipelines, executions, and state
  * Designed for platform-level governance

* **Observability and Auditing**

  * Native integration with logging, tracing, and metrics
  * End-to-end visibility into workflow execution

* **Extensibility**

  * API-first and plugin-oriented design
  * Easy integration with existing build, deploy, and ops tooling

---

## Cloud-Native by Design

Arcentra embraces cloud-native principles:

* Kubernetes-native runtime model
* Horizontally scalable agents
* Deep integration with modern observability stacks
* Suitable as a long-term engineering platform

---

## Use Cases

* Organization-wide CI/CD platforms
* Multi-cluster or multi-cloud build and delivery systems
* Teams evolving from tool-based pipelines to platform governance
* Engineering organizations seeking consistency and visibility

---

## Project Status

Arcentra is currently under **active development**. The project prioritizes:

* Clear and stable core abstractions
* Extensibility and long-term maintainability
* Practical integration with real-world engineering systems

Contributions, discussions, and design feedback are welcome.

---

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md) (English) for development setup, checks, and the pull request process.

---

## Security

For reporting security issues, see [SECURITY.md](./SECURITY.md) (English).

---

## Code of Conduct

Please read and follow our [Code of Conduct](./CODE_OF_CONDUCT.md) to keep the community welcoming and respectful.

---

## License
Copyright 2025 The Arcentra Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.