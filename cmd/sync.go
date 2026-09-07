package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync [repository]",
		Short: "Run a single sync cycle and exit",
		Long: `Run one stamp / commit / fetch / rebase / push cycle and exit.

Useful from cron or a systemd timer, and for trying the tool out with --dry-run
before letting it watch a real vault.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, _, err := open(cmd, args)
			if err != nil {
				return err
			}
			res, err := s.Sync(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), res.String())
			return nil
		},
	}
}
