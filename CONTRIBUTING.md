# Contributing Guide

English | [简体中文](./CONTRIBUTING_zh_CN.md)

Thank you for considering contributing to Arcentra. This guide explains how to report issues, propose changes, and contribute code.

---

## Code of Conduct

This project follows the Contributor Covenant. By participating, you are expected to uphold our [Code of Conduct](./CODE_OF_CONDUCT.md).

## How to Contribute

### Reporting Bugs

- Search existing Issues first to avoid duplicates
- Provide a clear description, expected behavior, and actual behavior
- Include reproduction steps and relevant logs/configuration

### Suggesting Features

- Search existing Issues/discussions first
- Explain the problem you are trying to solve and why it matters
- Propose a minimal scope for the first iteration if possible

### Contributing Code

1. Fork the repository
2. Create a feature branch (`git checkout -b feat/your-change`)
3. Make your changes and add tests where appropriate
4. Run checks locally (see "Development setup" and "Checks")
5. Commit your changes
6. Push your branch and open a Pull Request

## Development Setup

### Prerequisites

- Go (see `go.mod` for the required version)
- Optional: Docker (for building container images)

This repository uses Make targets to manage common tasks. Some tools are installed automatically by the Makefile when needed (for example `wire`, `buf`, `golangci-lint`).

### Build

- Build server binary:

  - `make build`

- Build agent binary:

  - `make build-agent`

- Build a selected target using a single parameter:

  - `make build-target TARGET=arcentra`
  - `make build-target TARGET=arcentra-agent`

### Run locally

- Run server:

  - `make run`

- Run agent:

  - `make run-agent`

## Checks

- Format and basic checks:
  - `go fmt ./...`
  - `go vet ./...`

- Lint:
  - `make lint`

- Static analysis:
  - `make staticcheck`

## Pull Request Process

- Keep PRs focused and small when possible
- Include a clear summary and test plan in the PR description
- If you change behavior, add tests or explain why tests are not applicable
- Be responsive to review feedback

## License

By submitting code to this project, you agree that your contributions will be licensed under the project's Apache 2.0 License.
