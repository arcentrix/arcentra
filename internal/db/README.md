# internal/db

数据库相关资源与生成代码目录。

## 目录说明

- **schema/**：全量 DDL（当前为 `arcentra.sql`），按业务模块分节，供迁移/初始化或 sqlc schema 使用。
- **migrations/**：DDL 迁移文件（按版本存放）；若使用 sqlc，可将 schema 路径指向本目录或 `schema/`。
- **sql/**：DML 查询 SQL 文件，供 sqlc 或手写 DAL 使用。
  - 每条语句格式：`-- name: QueryName :one|:many|:exec`、`-- 中文说明`，后接 SQL。
  - 占位符使用 `?`（MySQL）；使用 sqlc 时可在生成前改为 `sqlc.arg(name)`。
- **queries/**：sqlc 生成代码输出目录（需在项目根或本目录配置 `sqlc.yaml` 并执行 `sqlc generate`）。

## sql 文件与表对应关系

| 文件 | 表/说明 |
|------|---------|
| team.sql | t_team |
| agent.sql | t_agent |
| pipeline.sql | t_pipeline |
| pipeline_run.sql | t_pipeline_run |
| project.sql | t_project |
| secret.sql | t_secret |
| step_run.sql | t_step_run, t_step_run_artifact |
| general_settings.sql | t_general_settings |
| identity.sql | t_identity |
| user.sql | t_user |
| user_ext.sql | t_user_ext |
| user_role_binding.sql | t_user_role_binding |
| project_member.sql | t_project_member |
| project_team_access.sql | t_project_team_access |
| team_member.sql | t_team_member |
| role.sql | t_role |
| role_menu_binding.sql | t_role_menu_binding |
| menu.sql | t_menu |
| notification_template.sql | t_notification_templates |
| notification_channel.sql | t_notification_channels |
| storage_config.sql | t_storage_config |
| task_record.sql | l_task_records |
