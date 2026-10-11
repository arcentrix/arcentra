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
	command := &cobra.Command{
		Use: "migrate", Short: "Initialize control-plane schema and built-in role permissions",
		Long: "Apply additive GORM schema migrations and idempotent built-in role/permission seeds. Back up an existing database first. Does not create an administrator account or grant user access.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := viper.New()
			cfg.SetConfigFile(configPath)
			if err := cfg.ReadInConfig(); err != nil {
				return fmt.Errorf("read configuration: %w", err)
			}
			var settings database.Database
			if err := cfg.UnmarshalKey("database", &settings); err != nil {
				return fmt.Errorf("read database configuration: %w", err)
			}
			manager, err := database.NewManager(settings)
			if err != nil {
				return err
			}
			defer manager.Close()
			if err := repo.MigrateSchema(cmd.Context(), manager.Default()); err != nil {
				return err
			}
			cmd.Println("Schema and built-in authorization data initialized. No user privileges changed.")
			return nil
		},
	}
	command.Flags().StringVar(&configPath, "conf", "conf.d/config.toml", "Configuration file with explicit target database")
	rootCmd.AddCommand(command)
}
