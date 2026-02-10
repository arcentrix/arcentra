# 贡献指南

简体中文 | [English](./CONTRIBUTING.md)

感谢你愿意为 Arcentra 做贡献。本文档说明如何提交问题、提出需求以及贡献代码。

---

## 行为准则

本项目遵循 Contributor Covenant。参与本项目即表示你同意遵守我们的[行为准则](./CODE_OF_CONDUCT.md)。

## 如何参与贡献

### 报告 Bug

- 提交前先搜索现有 Issue，避免重复
- 描述清楚问题现象、期望行为与实际行为
- 提供可复现步骤，以及相关日志/配置（注意脱敏）

### 提出新功能或改进建议

- 提交前先搜索现有 Issue/讨论
- 说明要解决的痛点、使用场景，以及为什么重要
- 建议优先提供可落地的最小范围方案，便于快速迭代

### 贡献代码

1. Fork 仓库
2. 创建分支（例如 `git checkout -b feat/your-change`）
3. 开发改动，并按需补充测试
4. 本地运行检查（见下文“开发环境”和“检查项”）
5. 提交 commit
6. 推送分支并创建 Pull Request

## 开发环境

### 依赖

- Go（以 `go.mod` 要求为准）
- 可选：Docker（用于构建镜像）

本仓库使用 Make 统一管理常用任务，部分工具会在需要时由 Makefile 自动安装（例如 `wire`、`buf`、`golangci-lint`）。

### 构建

- 构建服务端二进制：
  - `make build`

- 构建 Agent 二进制：
  - `make build-agent`

- 使用单一参数构建指定目标：
  - `make build-target TARGET=arcentra`
  - `make build-target TARGET=arcentra-agent`

### 本地运行

- 运行服务端：
  - `make run`

- 运行 Agent：
  - `make run-agent`

## 检查项

- 格式化与基础检查：
  - `go fmt ./...`
  - `go vet ./...`

- 代码规范检查：
  - `make lint`

- 静态分析：
  - `make staticcheck`

## Pull Request 流程建议

- 尽量保持 PR 小而聚焦，便于 review
- 在 PR 描述中写清楚改动摘要与测试计划
- 行为变更尽量补充测试；如无法覆盖请说明原因
- 及时响应 review 意见并修订

## 许可说明

当你向本项目提交代码时，你同意你的贡献将以项目的 Apache 2.0 License 进行许可。

