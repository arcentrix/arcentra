// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/process"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/bytedance/sonic"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func init() {
	rootCmd.AddCommand(newPruneArtifactsCommand())
}

func newPruneArtifactsCommand() *cobra.Command {
	var configPath, projectID, runID, before string
	var apply, stopped bool
	var limit int
	command := &cobra.Command{
		Use:   "prune-artifacts",
		Short: "Preview expired artifacts in one project; offline deletion requires explicit approval flags",
		Long: "Preview a bounded batch of expired, registered artifacts in one explicitly selected project and optional run. " +
			"No migrations, controllers, agents, schedulers, or cache services are started. " +
			"Before applying, back up the database and artifact contents, stop ALL controllers and agents, " +
			"and verify no downloads or external artifact consumers remain active. " +
			"Deletion is irreversible unless you have an independent backup. " +
			"Both --apply and --executors-stopped are required; neither flag alone enables deletion.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := artifact.Identity(projectID); err != nil {
				return fmt.Errorf("an explicit valid --project-id is required")
			}
			if runID != "" && artifact.Identity(runID) != nil {
				return fmt.Errorf("invalid --run-id")
			}
			if limit < 1 || limit > 500 {
				return fmt.Errorf("--limit must be between 1 and 500")
			}
			if apply && !stopped {
				return fmt.Errorf("--apply requires --executors-stopped: stop all controllers/agents and verify no artifact consumers remain active")
			}
			cutoff := time.Now().UTC()
			if before != "" {
				parsed, err := time.Parse(time.RFC3339, before)
				if err != nil {
					return fmt.Errorf("--before must be an RFC3339 timestamp with a timezone")
				}
				if parsed.After(cutoff) {
					return fmt.Errorf("--before cannot be in the future")
				}
				cutoff = parsed.UTC()
			}
			if err := cmd.Context().Err(); err != nil {
				return err
			}

			cfg := viper.New()
			cfg.SetConfigFile(configPath)
			if err := cfg.ReadInConfig(); err != nil {
				return fmt.Errorf("read configuration: %w", err)
			}
			var settings database.Database
			if err := cfg.UnmarshalKey("database", &settings); err != nil {
				return fmt.Errorf("read database configuration: %w", err)
			}
			// 只连接明确选择的数据库，SQLite 预览强制只读且应用模式不新建库
			switch settings.Driver {
			case "", "mysql":
				settings.SQLite.DSN = ""
			case "sqlite":
				settings.MySQL.DSN = ""
				dsn, err := pruneSQLiteDSN(settings.SQLite.DSN, apply)
				if err != nil {
					return err
				}
				settings.SQLite.DSN = dsn
			default:
				return fmt.Errorf("artifact cleanup supports only explicitly configured mysql or sqlite databases")
			}
			manager, err := database.NewManager(settings)
			if err != nil {
				return fmt.Errorf("connect artifact cleanup database: %w", err)
			}
			defer func() { _ = manager.Close() }()
			repositories := repo.NewRepositories(database.ProvideIDatabase(manager), nil)
			artifacts := service.NewArtifactService(repositories.Artifact, repositories.Storage, artifact.Root())
			cleaner := &process.ArtifactCleaner{
				Artifacts: repositories.Artifact, Pipelines: repositories.Pipeline, ResolveStorage: artifacts.ResolveStorage,
			}
			options := process.ArtifactCleanupOptions{
				ProjectID: projectID, RunID: runID, Before: cutoff, Limit: limit, Apply: apply, ExecutorsStopped: stopped,
			}
			result, pruneErr := cleaner.Prune(cmd.Context(), options)
			if result != nil {
				// 安全字段采用 JSON 转义，避免制品名称污染终端，保留失败前的部分结果
				report := struct {
					ProjectID string                         `json:"projectId"`
					RunID     string                         `json:"runId,omitempty"`
					Before    time.Time                      `json:"before"`
					Limit     int                            `json:"limit"`
					Result    *process.ArtifactCleanupResult `json:"result"`
				}{projectID, runID, cutoff, limit, result}
				encoded, err := sonic.MarshalIndent(report, "", "  ")
				if err != nil {
					return fmt.Errorf("encode artifact cleanup result: %w", err)
				}
				cmd.Println(string(encoded))
			}
			if pruneErr != nil {
				return fmt.Errorf("artifact cleanup stopped: %w", pruneErr)
			}
			if result.Failed > 0 {
				return fmt.Errorf("artifact cleanup had %d failed items; registry entries were retained where possible, review the report before retrying", result.Failed)
			}
			return nil
		},
	}
	command.Flags().StringVar(&configPath, "conf", "conf.d/config.toml", "Private configuration selecting the existing target database")
	command.Flags().StringVar(&projectID, "project-id", "", "Required project business ID; no cross-project cleanup")
	command.Flags().StringVar(&runID, "run-id", "", "Restrict cleanup to this pipeline run within the selected project")
	command.Flags().StringVar(&before, "before", "", "Include expiry timestamps at or before this RFC3339 cutoff (default: current UTC time)")
	command.Flags().IntVar(&limit, "limit", 100, "Maximum candidates in this single invocation (1-500); no automatic pagination")
	command.Flags().BoolVar(&apply, "apply", false, "Delete the reviewed expired objects and their matching registry versions")
	command.Flags().BoolVar(&stopped, "executors-stopped", false, "Confirm all controllers/agents are stopped and no artifact consumers remain active")
	return command
}

func pruneSQLiteDSN(dsn string, apply bool) (string, error) {
	if dsn == "" || dsn == ":memory:" {
		return "", fmt.Errorf("artifact cleanup requires an existing persistent SQLite database")
	}
	if !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + dsn
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("invalid SQLite database URI")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || query.Get("mode") == "memory" || parsed.Opaque == ":memory:" {
		return "", fmt.Errorf("artifact cleanup requires an existing persistent SQLite database")
	}
	mode := query.Get("mode")
	if !apply {
		query.Set("mode", "ro")
	} else if mode == "" || mode == "rwc" {
		query.Set("mode", "rw")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
