-- Copyright 2026 Arcentra Authors.
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- You may obtain a copy of the License at
--
--      http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing, software
-- distributed under the License is distributed on an "AS IS" BASIS,
-- WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
-- See the License for the specific language governing permissions and
-- limitations under the License.

-- Pipeline Template Library DDL
-- Tables: t_pipeline_template_library, t_pipeline_template

CREATE TABLE IF NOT EXISTS t_pipeline_template_library (
    id                 BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    library_id         VARCHAR(36)   NOT NULL,
    name               VARCHAR(255)  NOT NULL,
    description        TEXT,
    repo_url           VARCHAR(512)  NOT NULL,
    default_ref        VARCHAR(255)  NOT NULL DEFAULT 'main',
    auth_type          TINYINT       NOT NULL DEFAULT 0     COMMENT '0=none,1=token,2=password,3=ssh_key',
    credential_id      VARCHAR(36)   NOT NULL DEFAULT ''    COMMENT 'reference to t_secret.secret_id',
    scope              VARCHAR(32)   NOT NULL DEFAULT 'system' COMMENT 'system/organization/project',
    scope_id           VARCHAR(36)   NOT NULL DEFAULT ''    COMMENT 'org_id or project_id; empty for system',
    sync_interval      INT           NOT NULL DEFAULT 0     COMMENT 'auto-sync interval in minutes; 0=manual only',
    last_sync_status   TINYINT       NOT NULL DEFAULT 0     COMMENT '0=unknown,1=success,2=failed,3=syncing',
    last_sync_message  TEXT,
    last_synced_at     TIMESTAMP     NULL,
    template_dir       VARCHAR(255)  NOT NULL DEFAULT 'templates' COMMENT 'directory path inside repo',
    created_by         VARCHAR(36)   NOT NULL DEFAULT '',
    is_enabled         TINYINT(1)    NOT NULL DEFAULT 1,
    created_at         TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_library_id (library_id),
    INDEX      idx_scope (scope, scope_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS t_pipeline_template (
    id              BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    template_id     VARCHAR(36)   NOT NULL,
    library_id      VARCHAR(36)   NOT NULL,
    name            VARCHAR(255)  NOT NULL,
    description     TEXT,
    category        VARCHAR(128)  NOT NULL DEFAULT ''    COMMENT 'ci/cd/build/test/deploy/custom',
    tags            JSON,
    icon            VARCHAR(512)  NOT NULL DEFAULT '',
    readme          TEXT,
    params          JSON                                 COMMENT 'array of TemplateParam definitions',
    spec_content    MEDIUMTEXT    NOT NULL                COMMENT 'pipeline spec template with ${{ }} placeholders',
    version         VARCHAR(64)   NOT NULL               COMMENT 'semantic version e.g. v1.0.0',
    commit_sha      VARCHAR(64)   NOT NULL DEFAULT '',
    scope           VARCHAR(32)   NOT NULL DEFAULT 'system',
    scope_id        VARCHAR(36)   NOT NULL DEFAULT '',
    is_latest       TINYINT(1)    NOT NULL DEFAULT 0,
    is_published    TINYINT(1)    NOT NULL DEFAULT 1,
    created_at      TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_template_id (template_id),
    UNIQUE KEY uk_lib_name_ver (library_id, name, version),
    INDEX      idx_scope (scope, scope_id),
    INDEX      idx_category (category),
    INDEX      idx_latest (library_id, name, is_latest)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
