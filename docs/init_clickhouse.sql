-- ================================================================
-- Arcentra CI/CD 平台 ClickHouse 初始化脚本（最终版）
-- 数据库: arcentra
-- 说明:
--   - 面向日志 / 事件 / 队列类高写入场景
--   - 设计目标：长期稳定、低维护、可扩展
-- ================================================================

CREATE DATABASE IF NOT EXISTS Arcentra;
USE Arcentra;

-- ================================================================
-- Table 1: l_task_queue_records
-- 任务队列记录表
-- ================================================================
CREATE TABLE IF NOT EXISTS l_task_queue_records
(
    task_id String,
    task_type LowCardinality(String),

    status LowCardinality(String),
    queue LowCardinality(String),
    priority Int32,

    pipeline_id String,
    pipeline_run_id String,
    stage_id String,
    agent_id String,

    payload String,                -- JSON
    create_time DateTime,
    start_time Nullable(DateTime),
    end_time Nullable(DateTime),
    duration Nullable(Int64),       -- ms

    retry_count Int32,
    current_retry Int32,
    error_message Nullable(String),

    INDEX idx_pipeline_run_id pipeline_run_id TYPE minmax GRANULARITY 3,
    INDEX idx_agent_id agent_id TYPE minmax GRANULARITY 3
)
ENGINE = MergeTree
ORDER BY (pipeline_run_id, create_time)
PRIMARY KEY (pipeline_run_id, create_time)
SETTINGS index_granularity = 8192;


-- ================================================================
-- Table 2: l_task_logs
-- 任务执行日志表
-- ================================================================
CREATE TABLE IF NOT EXISTS l_task_logs
(
    log_id String,
    task_id String,
    pipeline_run_id String,
    agent_id String,

    line_number Int32,
    content String,

    timestamp DateTime,
    level LowCardinality(String),
    created_at DateTime,

    INDEX idx_pipeline_run_id pipeline_run_id TYPE minmax GRANULARITY 3,
    INDEX idx_agent_id agent_id TYPE minmax GRANULARITY 3
)
ENGINE = MergeTree
ORDER BY (task_id, timestamp, line_number)
PRIMARY KEY (task_id, timestamp)
TTL timestamp + INTERVAL 90 DAY
SETTINGS index_granularity = 8192;


-- ================================================================
-- Table 3: l_terminal_logs
-- 终端 / 构建 / 调试日志表
-- ================================================================
CREATE TABLE IF NOT EXISTS l_terminal_logs
(
    session_id String,

    session_type LowCardinality(String),   -- build / deploy / release / debug
    environment LowCardinality(String),    -- dev / test / staging / prod

    task_id String,
    pipeline_id String,
    pipeline_run_id String,
    user_id String,

    hostname String,
    working_directory String,
    command String,
    exit_code Nullable(Int32),

    logs String,            -- JSON: TerminalLogLine[]
    metadata String,        -- JSON: TerminalLogMetadata

    status LowCardinality(String),          -- running / completed / failed / timeout
    start_time DateTime,
    end_time Nullable(DateTime),

    created_at DateTime,
    updated_at DateTime,

    INDEX idx_pipeline_run_id pipeline_run_id TYPE minmax GRANULARITY 3,
    INDEX idx_user_id user_id TYPE minmax GRANULARITY 3
)
ENGINE = MergeTree
ORDER BY (pipeline_run_id, created_at)
PRIMARY KEY (pipeline_run_id, created_at)
TTL created_at + INTERVAL 180 DAY
SETTINGS index_granularity = 8192;


-- ================================================================
-- Table 4: l_build_artifacts_logs
-- 构建产物操作日志表
-- ================================================================
CREATE TABLE IF NOT EXISTS l_build_artifacts_logs
(
    artifact_id String,
    task_id String,

    operation LowCardinality(String),       -- upload / download / delete
    file_name String,
    file_size Int64,

    storage_type LowCardinality(String),    -- minio / s3 / oss / gcs / cos
    storage_path String,

    user_id String,
    status LowCardinality(String),           -- success / failed
    error_message String,

    duration_ms Int64,
    timestamp DateTime,

    INDEX idx_task_id task_id TYPE minmax GRANULARITY 3,
    INDEX idx_user_id user_id TYPE minmax GRANULARITY 3
)
ENGINE = MergeTree
ORDER BY (artifact_id, timestamp)
PRIMARY KEY (artifact_id, timestamp)
TTL timestamp + INTERVAL 90 DAY
SETTINGS index_granularity = 8192;
