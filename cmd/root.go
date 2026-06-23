package cmd

import (
	"fmt"
	"os"

	"github.com/mallendem/gh-pr-review/pkg/gui"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "pr-approver",
	Short: "Bulk-review and approve GitHub PRs requesting your review",
	Long:  `Fetches GitHub notifications for review-requested PRs, groups changes by content hash, and provides CLI and TUI interfaces to approve or decline them.`,
	// When invoked without a subcommand, open the approval GUI by default.
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(cmd.OutOrStdout(), "No command provided, opening GUI by default...")
		user, _ := cmd.Flags().GetString("user")
		propagate, _ := cmd.Flags().GetBool("propagate")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		if err := gui.Run(user, propagate, dryRun); err != nil {
			cmd.PrintErrf("failed to run gui: %v\n", err)
		}
	},
}

func init() {
	// Flags for the default (GUI) invocation when no subcommand is given.
	rootCmd.Flags().StringP("user", "u", "", "User to run GUI manual approval for (shows selection panel if omitted)")
	rootCmd.Flags().BoolP("propagate", "p", false, "When approving a hash, automatically approve linked hashes in the same PR")
	rootCmd.Flags().BoolP("dry-run", "d", false, "Dry run: do not submit approvals, only print what would be approved")
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
