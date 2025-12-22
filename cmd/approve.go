package cmd

import (
	"pr-approver/pkg/approve"

	"github.com/spf13/cobra"
)

// approveCmd represents the approve command
var approveCmd = &cobra.Command{
	Use:   "approve",
	Short: "",
	Long:  ``,
	Run: func(cmd *cobra.Command, args []string) {
		if onlyUsers, _ := cmd.Flags().GetBool("only-users"); onlyUsers {
			approve.PrintUsersWithPrs()
			return
		}

		if hashes, _ := cmd.Flags().GetStringSlice("hash"); len(hashes) > 0 {
			approve.ApprovePrByHash(hashes)
			return
		}

		users, _ := cmd.Flags().GetStringSlice("user")
		_ = approve.ApprovePullRequest(users)
	},
}

var manualCmd = &cobra.Command{
	Use:   "manual",
	Short: "Interactive manual approval for a user",
	Run: func(cmd *cobra.Command, args []string) {
		user, _ := cmd.Flags().GetString("user")
		if user == "" {
			cmd.PrintErrln("--user is required for manual mode")
			return
		}
		propagate, _ := cmd.Flags().GetBool("propagate")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		_ = approve.ManualApproval(user, propagate, dryRun)
	},
}

func init() {
	rootCmd.AddCommand(approveCmd)
	approveCmd.AddCommand(manualCmd)

	// Here you will define your flags and configuration settings.
	approveCmd.Flags().StringSliceP("user", "u", nil, "Comma-separated list of users to show changes for (e.g. alice,bob)")
	approveCmd.Flags().StringSliceP("hash", "x", nil, "Comma-separated list of hash values to approve PRs for (e.g. abc123,def456)")
	approveCmd.Flags().StringSlice("approve-user", nil, "Comma-separated list of users whose PRs to approve (e.g. alice,bob)")
	approveCmd.Flags().BoolP("only-users", "o", false, "Return only the list of users with pending PR reviews")

	// manual subcommand flags
	manualCmd.Flags().StringP("user", "m", "", "User to run manual approval for (required)")
	manualCmd.Flags().BoolP("propagate", "p", false, "When approving a hash, automatically approve linked hashes in the same PR")
	manualCmd.Flags().BoolP("dry-run", "d", false, "Dry run: do not submit approvals, only print what would be approved")

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// approveCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// approveCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
