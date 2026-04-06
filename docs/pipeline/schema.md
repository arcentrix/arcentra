## Pipeline YAML Schema (Draft) v3

```yaml
title: CI/CD Pipeline Schema v2
description: Plugin-driven pipeline definition with jobs, steps, actions, conditions & approvals
type: object
required: ["namespace", "jobs"]

properties:

  namespace:
    type: string
    description: Pipeline namespace (prod, staging, org, etc.)

  variables:
    type: object
    description: Global environment variables
    additionalProperties:
      type: string

  jobs:
    type: array
    description: Pipeline jobs (formerly tasks)
    items:
      type: object
      required: ["name", "steps"]
      additionalProperties: false

      properties:

        name:
          type: string
          description: Unique job name

        description:
          type: string

        env:
          type: object
          description: Job-level env vars
          additionalProperties:
            type: string

        concurrency:
          type: string
          description: Concurrency lock key (prevents parallel runs)

        depends_on:
          type: array
          description: >-
            Upstream job names that must complete before this job runs (DAG edges).
            Serializes as dependsOn in api/pipeline/v1 Job (proto); used by Executor BuildDAG.
          items:
            type: string

        timeout:
          type: string
          description: e.g. "30m"

        retry:
          type: object
          properties:
            max_attempts:
              type: integer
            delay:
              type: string

        when:
          type: string
          description: Condition expression (e.g., branch == 'main')

        source:
          $ref: "#/definitions/source"

        approval:
          $ref: "#/definitions/approval"

        steps:
          type: array
          minItems: 1
          items:
            $ref: "#/definitions/step"

        target:
          $ref: "#/definitions/target"

        notify:
          $ref: "#/definitions/notify"

        triggers:
          $ref: "#/definitions/triggers"


definitions:

  # ----- Source (Git / Artifact / S3 / Custom) -----
  source:
    type: object
    required: ["type"]
    properties:
      type:
        type: string
        enum: ["git", "artifact", "s3", "custom"]
      repo:
        type: string
      branch:
        type: string
      auth:
        type: object
        properties:
          username: {type: string}
          password: {type: string}
          token: {type: string}


  # ----- Step -----
  step:
    type: object
    required: ["name", "uses"]
    properties:
      name:
        type: string
      uses:
        type: string
        description: Plugin name (e.g., docker-build, python-test)
      action:
        type: string
        description: Plugin action (defaults to Execute, e.g., clone, run, push, build)
      args:
        type: object
        description: Plugin-specific arguments (arbitrary JSON)
        additionalProperties: true
      env:
        type: object
        description: Step-level env vars (overrides job/env)
        additionalProperties:
          type: string
      continue_on_error:
        type: boolean
      timeout:
        type: string
      when:
        type: string
        description: Condition expression for this step


  # ----- Approval -----
  approval:
    type: object
    required: ["required"]
    properties:
      required: {type: boolean}
      type:
        type: string
        enum: ["manual", "auto"]
      plugin: {type: string}
      args:
        type: object
        description: Approval plugin arguments (arbitrary JSON)
        additionalProperties: true


  # ----- Target -----
  target:
    type: object
    required: ["type"]
    properties:
      type:
        type: string
        enum: ["k8s", "vm", "docker", "s3", "custom"]
      config:
        type: object
        additionalProperties: true


  # ---- Notify ----
  notify:
    type: object
    properties:
      on_success: { $ref: "#/definitions/notifyItem" }
      on_failure: { $ref: "#/definitions/notifyItem" }

  notifyItem:
    type: object
    required: ["plugin", "action"]
    properties:
      plugin: {type: string}
      action:
        type: string
        description: Notify plugin action ("Send" or "SendTemplate")
      args:
        type: object
        description: Notify plugin arguments (arbitrary JSON)
        additionalProperties: true


  # ----- Trigger -----
  triggers:
    type: array
    items:
      type: object
      required: ["type"]
      properties:
        type:
          type: string
          enum: ["manual", "cron", "event"]
        options:
          type: object
          additionalProperties: true
```


### example
```yaml
############################################
# Pipeline（流水线定义）
# - 一份静态配置（YAML / DSL）
# - 可被多次触发，生成多个 PipelineRun
# - 持有全局变量、触发器、默认策略
############################################
# pipeline
namespace: "prod"   # Pipeline 命名空间（租户 / 环境 / 组织隔离）

############################################
# Pipeline-level Variables
# - 在整个 Pipeline 生命周期内可用
# - 可被 Job / Step 读取
# - 不可在运行时修改（只读）
############################################
variables:
  REGISTRY: "dockerhub.io/myorg"

############################################
# Jobs（作业列表）
#
# 重要说明：
# - 当前 DSL 为「jobs-only 模式」
# - 内部模型应自动包裹为：
#
#   Stage(default)
#     └── Job(build-and-deploy)
#
# - Job 是“最小调度执行单元”
# - 调度器、日志、资源、超时、并发都只认 Job
############################################
jobs:

  ##########################################
  # Job（执行作业）
  # - 一个 Job 对应一次 Runner / Agent 执行
  # - 具备独立的运行上下文
  ##########################################
  - name: "build-and-deploy"      # Job 唯一标识（Pipeline 内）
    description: "构建和部署生产镜像"

    ########################################
    # Job-level Execution Policy
    ########################################
    concurrency: "prod-deploy"    # 并发控制 key（同 key Job 串行）
    timeout: "30m"                # Job 最大运行时间

    ########################################
    # Job-level Environment Variables
    # - 仅在该 Job 内生效
    ########################################
    env:
      ENVIRONMENT: "production"
      BUILD_VERSION: "${{ git.commit }}"

    ########################################
    # Source（源码定义）
    # - Job 的输入之一
    # - 用于生成 Workspace
    ########################################
    source:
      type: git
      repo: https://github.com/example/project.git
      branch: main

    ########################################
    # Steps（步骤列表）
    #
    # - Step 是 Job 内的顺序执行单元
    # - Step 共享 Job 的 Workspace
    # - 任一 Step 失败 → Job 失败
    ########################################
    steps:

      ######################################
      # Step：checkout
      ######################################
      - name: checkout
        uses: git                  # Task / Plugin 名称
        action: clone              # Task 内具体动作
        args:
          depth: 1

      ######################################
      # Step：test
      ######################################
      - name: test
        uses: pytest
        action: run
        args:
          coverage: true

      ######################################
      # Step：build-image
      ######################################
      - name: build-image
        uses: docker-build
        args:
          context: .
          tag: ${{ REGISTRY }}/webapp:${{ BUILD_VERSION }}
          dockerfile: Dockerfile

      ######################################
      # Step：push-image
      ######################################
      - name: push-image
        uses: docker-registry
        action: push
        args:
          registry: ${{ REGISTRY }}
          tag: ${{ BUILD_VERSION }}

    ########################################
    # Approval（人工审批关卡）
    #
    # 语义说明：
    # - 逻辑上属于 Stage Gate
    # - 在 jobs-only DSL 中，默认绑定在 Job 之后
    # - 执行引擎中应表现为：
    #     Job 完成 → 等待审批 → 进入下一个 Stage
    ########################################
    approval:
      required: true
      type: manual
      plugin: approval-slack
      args:
        approvers: ["ops-lead", "dev-manager"]
        message: "请审批生产发布"

    ########################################
    # Target（部署目标）
    #
    # - CD 语义
    # - 描述 Job 的“输出作用对象”
    # - 不直接参与调度
    ########################################
    target:
      type: k8s
      config:
        cluster: prod-cluster
        namespace: webapp
        deployment: webapp
        image: ${{ REGISTRY }}/webapp:${{ BUILD_VERSION }}

    ########################################
    # Notification（生命周期回调）
    #
    # - 不影响 Job 成败
    # - 监听 Job / PipelineRun 状态变化
    ########################################
    notify:
      on_success:
        plugin: notify-slack
        action: Send
        args:
          channel: "#ci-success"
          message: "构建成功: ${{ BUILD_VERSION }}"

      on_failure:
        plugin: notify-slack
        action: Send
        args:
          channel: "#ci-failure"
          message: "构建失败: ${{ BUILD_VERSION }}"

############################################
# Triggers（触发器）
#
# - 定义 PipelineRun 的触发方式
# - 不属于 Job
# - 多触发器 OR 关系
############################################
triggers:
  - type: manual

  - type: cron
    options:
      expression: "0 6 * * *"

  - type: event
    options:
      event_type: push
      branch: main

```
1. tasks → jobs（统一行业标准）
2. stages → steps（对标 GitHub Actions）
3. steps 支持：
 - uses（插件名）
 - action（插件的子方法，例如 push/build/run/scan）
 - args（任意 JSON）
 - when（条件表达式）
 - continue_on_error
 - timeout
 - env（覆盖 job/env）
4. notify 部分改为：plugin + action