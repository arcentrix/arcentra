<div align="center">

# Arcentra（阿森特拉）

[![GitHub 仓库](https://img.shields.io/badge/GitHub-仓库-black.svg?logo=github)](https://github.com/arcentrix/arcentra)
[![Go 版本](https://img.shields.io/badge/go-1.25%2B-00ADD8.svg?logo=go&label=Go)](https://go.dev/)
[![许可证](https://img.shields.io/badge/license-Apache%202.0-red.svg?logo=apache&label=许可证)](./LICENSE)
[![最后提交](https://img.shields.io/github/last-commit/arcentrix/arcentra)](https://github.com/arcentrix/arcentra)
[![GitHub Star](https://img.shields.io/github/stars/arcentrix/arcentra?style=flat&logo=github&color=yellow&label=Star)](https://github.com/arcentrix/arcentra/stargazers)
[![GitHub Fork](https://img.shields.io/github/forks/arcentrix/arcentra?style=flat&logo=github&color=purple&label=Fork)](https://github.com/arcentrix/arcentra/network)

[[文档（中文）](./README_zh_CN.md)] [[English](./README.md)]

**面向现代工程系统的云原生 CI/CD 控制平面。**

</div>

Arcentra 是一个开源 CI/CD 控制平面，面向需要集中编排、分布执行的大规模工程体系。
它不只是执行流水线，而是为组织提供一个长期可演进的自动化架构中枢。

## 为什么是 Arcentra

- **控制平面优先**：统一建模流水线、执行过程与状态
- **分布式执行**：将任务调度到异构 Agent 和资源池
- **云原生架构**：面向 Kubernetes 与横向扩展场景设计
- **工程治理能力**：强调可观测性、可审计性与一致性
- **可扩展模型**：API 优先，插件化演进路径清晰

## 核心能力

- **流水线编排**
  - 支持多阶段与 DAG 流程
  - 流水线定义与执行解耦
- **Agent 调度**
  - 中央控制，分布式执行
  - 适配多种运行环境
- **可观测与审计**
  - 统一接入日志、指标与链路能力
  - 提供端到端执行可见性
- **插件与动作模型**
  - 基于接口的扩展机制
  - 便于平台长期演进

## 快速开始

前置要求：

- Go（版本要求见 `go.mod`）
- 可选：Docker（用于容器化构建/运行）

常用命令：

```bash
make build
make run
make lint
go test ./...
```

完整开发与贡献流程请参考 [CONTRIBUTING_zh_CN.md](./CONTRIBUTING_zh_CN.md)。

## 项目状态

Arcentra 当前处于持续开发阶段，欢迎提交 Issue、功能建议与 PR。

## 安全

安全漏洞报告方式请参考 [SECURITY_zh_CN.md](./SECURITY_zh_CN.md)。

## 行为准则

请阅读并遵守 [CODE_OF_CONDUCT.md](./CODE_OF_CONDUCT.md)。

## 开源许可

Copyright 2025 The Arcentra Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at:

http://www.apache.org/licenses/LICENSE-2.0
