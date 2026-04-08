本文档定义了 CI/CD 平台的 **Pipeline DSL 结构规范**，
用于描述流水线的组成、职责边界以及校验规则。

**相关文档**：[工作负载语义（CI / 大数据 / AI）](./workload_semantics.md)；[执行栈盘点与触发链缺口](./execution_inventory.md)；[编排与 Agent 策略备忘](./architecture_decisions.md)。

---

## 1. 设计原则
+ **Pipeline（流水线）** 是一份**静态定义**，可被多次触发执行。
+ **Stage（阶段）** 是**逻辑结构**，不参与实际执行。
+ **Job（作业）** 是**最小可调度、可执行单元**。
+ **Step（步骤）** 是 Job 内部的顺序操作。
+ 调度器 **只感知 Job**，不感知 Stage。
+ Schema 同时支持：
    - `jobs` 直写模式（隐式默认 Stage）
    - `stages → jobs → steps` 完整结构
+ 插件 / Task 完全可扩展，不做硬编码。
+ 向后兼容是第一设计目标。

---

## 2. 顶层 Pipeline 结构
### Pipeline 对象
```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "Pipeline",
  "type": "object",
  "required": ["namespace"],
  "properties": {
    "namespace": {
      "type": "string",
      "description": "流水线命名空间（租户 / 环境隔离）"
    },

    "variables": {
      "type": "object",
      "description": "Pipeline 级变量",
      "additionalProperties": {
        "type": "string"
      }
    },

    "stages": {
      "type": "array",
      "description": "阶段式流水线定义",
      "items": { "$ref": "#/definitions/stage" }
    },

    "jobs": {
      "type": "array",
      "description": "仅 Job 模式（将被自动包裹在默认 Stage 中）",
      "items": { "$ref": "#/definitions/job" }
    },

    "triggers": {
      "type": "array",
      "description": "流水线触发器",
      "items": { "$ref": "#/definitions/trigger" }
    }
  },

  "oneOf": [
    { "required": ["jobs"] },
    { "required": ["stages"] }
  ]
}
```

---

## 3. Stage（阶段）结构
**Stage 是逻辑结构，不是执行体。**
主要用于流程分段、审批边界和 UI 展示。

```json
{
  "definitions": {
    "stage": {
      "type": "object",
      "required": ["name", "jobs"],
      "properties": {
        "name": {
          "type": "string",
          "description": "阶段名称"
        },

        "approval": {
          "$ref": "#/definitions/approval",
          "description": "进入该阶段前的审批关卡"
        },

        "jobs": {
          "type": "array",
          "description": "该阶段内执行的作业列表",
          "items": { "$ref": "#/definitions/job" }
        }
      }
    }
  }
}
```

---

## 4. Job（作业）结构
**Job 是最小可调度、可执行单元**，
所有资源、日志、超时、并发控制均在 Job 级别生效。

```json
{
  "definitions": {
    "job": {
      "type": "object",
      "required": ["name", "steps"],
      "properties": {
        "name": {
          "type": "string",
          "description": "作业名称（Pipeline 内唯一）"
        },

        "description": {
          "type": "string",
          "description": "作业描述"
        },

        "concurrency": {
          "type": "string",
          "description": "并发控制 key（相同 key 的 Job 串行执行）"
        },

        "timeout": {
          "type": "string",
          "pattern": "^[0-9]+(s|m|h)$",
          "description": "作业最大运行时间"
        },

        "env": {
          "type": "object",
          "description": "Job 级环境变量",
          "additionalProperties": {
            "type": "string"
          }
        },

        "source": {
          "$ref": "#/definitions/source"
        },

        "steps": {
          "type": "array",
          "minItems": 1,
          "description": "作业内顺序执行的步骤",
          "items": { "$ref": "#/definitions/step" }
        },

        "approval": {
          "$ref": "#/definitions/approval",
          "description": "作业完成后的审批关卡"
        },

        "target": {
          "$ref": "#/definitions/target"
        },

        "notify": {
          "$ref": "#/definitions/notify"
        }
      }
    }
  }
}
```

---

## 5. Step（步骤）结构
**Step 是 Job 内部的顺序操作单元**，
所有 Step 共享同一个执行上下文和工作目录。

```json
{
  "definitions": {
    "step": {
      "type": "object",
      "required": ["name", "uses"],
      "properties": {
        "name": {
          "type": "string",
          "description": "步骤名称"
        },

        "uses": {
          "type": "string",
          "description": "使用的 Task / Plugin 标识"
        },

        "action": {
          "type": "string",
          "description": "插件内具体动作"
        },

        "args": {
          "type": "object",
          "description": "插件参数",
          "additionalProperties": true
        }
      }
    }
  }
}
```

---

## 6. Approval（审批关卡）
**Approval 是逻辑关卡，不是执行步骤。**

```json
{
  "definitions": {
    "approval": {
      "type": "object",
      "required": ["required", "type"],
      "properties": {
        "required": {
          "type": "boolean",
          "description": "是否必须审批"
        },

        "type": {
          "type": "string",
          "enum": ["manual"],
          "description": "审批类型"
        },

        "plugin": {
          "type": "string",
          "description": "审批插件"
        },

        "args": {
          "type": "object",
          "description": "插件参数",
          "additionalProperties": true
        }
      }
    }
  }
}
```

---

## 7. Source / Target / Notify / Trigger
### Source（源码）
```json
{
  "definitions": {
    "source": {
      "type": "object",
      "required": ["type"],
      "properties": {
        "type": {
          "type": "string",
          "enum": ["git"],
          "description": "源码类型"
        },
        "repo": {
          "type": "string",
          "description": "仓库地址"
        },
        "branch": {
          "type": "string",
          "description": "分支名称"
        }
      }
    }
  }
}
```

---

### Target（部署目标）
```json
{
  "definitions": {
    "target": {
      "type": "object",
      "required": ["type"],
      "properties": {
        "type": {
          "type": "string",
          "description": "目标类型（如 k8s）"
        },
        "config": {
          "type": "object",
          "description": "目标配置",
          "additionalProperties": true
        }
      }
    }
  }
}
```

---

### Notify（通知）
```json
{
  "definitions": {
    "notify": {
      "type": "object",
      "properties": {
        "on_success": { "$ref": "#/definitions/notification" },
        "on_failure": { "$ref": "#/definitions/notification" }
      }
    },

    "notification": {
      "type": "object",
      "required": ["plugin"],
      "properties": {
        "plugin": {
          "type": "string",
          "description": "通知插件"
        },
        "action": {
          "type": "string"
        },
        "args": {
          "type": "object",
          "additionalProperties": true
        }
      }
    }
  }
}
```

---

### Trigger（触发器）
```json
{
  "definitions": {
    "trigger": {
      "type": "object",
      "required": ["type"],
      "properties": {
        "type": {
          "type": "string",
          "enum": ["manual", "cron", "event"],
          "description": "触发类型"
        },
        "options": {
          "type": "object",
          "description": "触发器参数",
          "additionalProperties": true
        }
      }
    }
  }
}
```

---

## 8. 非 Schema 校验规则（服务层实现）
以下规则 **不建议写入 JSON Schema**，应在服务层校验：

+ `jobs` 与 `stages` 必须至少存在一个
+ Stage 不允许直接包含 steps
+ 并发 key 的全局冲突检测
+ 审批顺序的确定性
+ Trigger 权限校验

---

## 9. 概示模型映射
| DSL 概念 | 运行时模型 |
| --- | --- |
| Pipeline | Pipeline |
| Stage | Stage |
| Job | Job |
| Step | Step |
| PipelineRun | PipelineRun |
| JobRun | JobRun |
| StepRun | StepRun |


---

