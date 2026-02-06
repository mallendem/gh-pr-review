package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "pr-approver",
	Short: "Bulk-review and approve GitHub PRs requesting your review",
	Long:  `Fetches GitHub notifications for review-requested PRs, groups changes by content hash, and provides CLI and TUI interfaces to approve or decline them.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
