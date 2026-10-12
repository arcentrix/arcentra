package main

import (
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func init() {
	var configPath, username string
	command := &cobra.Command{Use: "bootstrap-admin", Short: "Explicitly grant the first administrator role to an existing registered user", RunE: func(cmd *cobra.Command, _ []string) error {
		if username == "" {
			return fmt.Errorf("--username is required")
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
		if err := repo.BootstrapAdmin(cmd.Context(), manager.Default(), username); err != nil {
			return err
		}
		cmd.Printf("Administrator role explicitly granted to %s. Restart every control-plane instance to reload authorization, then sign in again.\n", username)
		return nil
	}}
	command.Flags().StringVar(&configPath, "conf", "conf.d/config.toml", "Private configuration file for target database")
	command.Flags().StringVar(&username, "username", "", "Existing registered account selected by the operator")
	rootCmd.AddCommand(command)
}
