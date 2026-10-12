package main

import (
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func init() {
	var configPath string
	var runIDs []string
	var apply, all, stopped bool
	command := &cobra.Command{Use: "recover-interrupted", Short: "Offline-only reconciliation of interrupted execution records (dry-run by default)", Long: "Stop ALL controllers and agents first, and verify no external build/deployment work is still active. This command marks active execution records failed; it never resumes work or rolls back external side effects. Back up the database first. After review, start a fresh manual run.", RunE: func(cmd *cobra.Command, _ []string) error {
		if !all && len(runIDs) == 0 {
			return fmt.Errorf("select --run-id or explicitly --all-active")
		}
		if all && len(runIDs) > 0 {
			return fmt.Errorf("use either --run-id or --all-active")
		}
		if apply && !stopped {
			return fmt.Errorf("--apply requires --executors-stopped: stop all controllers/agents and verify no external work is active")
		}
		cfg := viper.New()
		cfg.SetConfigFile(configPath)
		if err := cfg.ReadInConfig(); err != nil {
			return err
		}
		var settings database.Database
		if err := cfg.UnmarshalKey("database", &settings); err != nil {
			return err
		}
		manager, err := database.NewManager(settings)
		if err != nil {
			return err
		}
		defer manager.Close()
		summary, err := repo.RecoverInterruptedRuns(cmd.Context(), manager.Default(), runIDs, apply)
		if err != nil {
			return err
		}
		mode := "DRY RUN, no changes"
		if apply {
			mode = "RECONCILED AS FAILED"
		}
		cmd.Printf("%s: %d runs, %d jobs, %d steps. Terminal records preserved. No commands executed.\n", mode, summary.Runs, summary.Jobs, summary.Steps)
		if summary.LinkedUnresolvedReleases > 0 {
			cmd.Printf("WARNING: %d linked releases remain unresolved. Release state and target reservations are preserved. Inspect and explicitly recover the same release IDs; do not create another deployment or infer rollback.\n", summary.LinkedUnresolvedReleases)
		}
		return nil
	}}
	command.Flags().StringVar(&configPath, "conf", "conf.d/config.toml", "Private target database configuration")
	command.Flags().StringSliceVar(&runIDs, "run-id", nil, "Specific interrupted run IDs")
	command.Flags().BoolVar(&all, "all-active", false, "Explicitly select every active run")
	command.Flags().BoolVar(&apply, "apply", false, "Apply the reviewed offline reconciliation")
	command.Flags().BoolVar(&stopped, "executors-stopped", false, "Confirm all controllers/agents stopped and no external work remains active")
	rootCmd.AddCommand(command)
}
