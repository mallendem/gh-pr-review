package approve

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/google/go-github/v72/github"
	"github.com/mallendem/gh-pr-review/pkg/gh"
)

// ANSI color codes
const (
	cReset  = "\033[0m"
	cYellow = "\033[33m"
	cRed    = "\033[31m"
	cGreen  = "\033[32m"
	cCyan   = "\033[36m"
	cOrange = "\033[38;5;208m"
)

func colorize(col, s string) string {
	return col + s + cReset
}

func ApprovePullRequest(users []string) error {
	c := gh.NewGhClient()
	c.PrintChangesPerUser(users)
	return nil
}

func PrintUsersWithPrs() {
	g := gh.NewGhClient()
	userHashPrMap, _, _, _, err := g.GetPrReviewRequested()
	if err != nil {
		fmt.Printf("%s\n", colorize(cYellow, fmt.Sprintf("Error fetching PR review requests: %v", err)))
		return
	}
	var users []string
	for user := range userHashPrMap {
		users = append(users, user)
	}
	sort.Strings(users)
	for _, user := range users {
		fmt.Println(colorize(cYellow, user))
	}
	return
}

func ApprovePrByHash(hashes []string) {
	g := gh.NewGhClient()
	_, changeMap, hMap, prMap, err := g.GetPrReviewRequested()
	if err != nil {
		fmt.Printf("%s\n", colorize(cYellow, fmt.Sprintf("Error fetching PR review requests: %v", err)))
		return
	}
	for _, h := range hashes {
		if prs, ok := hMap[h]; ok {
			for _, pr := range prs {
				fmt.Printf("%s %s\n", colorize(cYellow, "Found PR for hash"), colorize(cYellow, fmt.Sprintf("%s: %s", h, pr.GetHTMLURL())))
				prKey := pr.GetHTMLURL()
				if linked, ok := prMap[prKey]; ok {
					var extras []string
					for _, ah := range linked {
						if ah == h {
							continue
						}
						extras = append(extras, ah)
					}
					if len(extras) > 0 {
						fmt.Printf("%s\n", colorize(cYellow, "  There are also other hashes linked to this PR:"))
						for _, ex := range extras {
							fmt.Printf("%s\n", colorize(cGreen, fmt.Sprintf("    %s", ex)))
							fmt.Printf("%s\n", colorize(cYellow, fmt.Sprintf("\t  Changes for hash %s:", ex)))
							if changes, ok := changeMap[ex]; ok {
								for _, line := range changes {
									fmt.Printf("%s\n", colorize(cRed, fmt.Sprintf("\t    %s", line)))
								}
							}
						}
					}
				}
			}
		} else {
			fmt.Printf("%s\n", colorize(cYellow, fmt.Sprintf("No PRs found for hash: %s", h)))
		}
	}
}

// ManualApproval interactively reviews hashes for `user` and approves any PR that contains
// only approved hashes. It prompts the operator for each hash (approve/decline/skip).
// propagate: if true, approving a hash will auto-approve other hashes linked in the same PR(s).
// dryRun: if true, do not submit approvals to GitHub; only print actions.

func ManualApproval(user string, propagate bool, dryRun bool) error {
	g := gh.NewGhClient()
	userHashPrMap, changeMap, hashPrMap, prMap, err := g.GetPrReviewRequested()
	if err != nil {
		return fmt.Errorf("error fetching PR review requests: %w", err)
	}

	// Collect hashes for the given user
	hashes := collectHashesForUsers(user, userHashPrMap)
	if len(hashes) == 0 {
		fmt.Printf("%s\n", colorize(cYellow, fmt.Sprintf("No hashes found for user %s", user)))
		return nil
	}

	approved := map[string]bool{}
	declined := map[string]bool{}
	prSkipped := map[string]bool{} // PRs to skip because a hash was declined

	in := bufio.NewReader(os.Stdin)
	// Show duplicate change lines only once across hashes
	firstSeen := map[string]string{}
	total := len(hashes)

	// Build deterministic unique PR list and index map
	uniquePrKeys, prIndexMap := buildUniquePrKeys(hashes, hashPrMap)
	totalPRs := len(uniquePrKeys)

	for idx, h := range hashes {
		// If this hash has been decided (e.g., linked duplicates), skip prompting
		if approved[h] || declined[h] {
			continue
		}

		// If any PR that contains this hash was previously skipped, skip this hash too
		if prs, ok := hashPrMap[h]; ok {
			skip := false
			for _, pr := range prs {
				if prSkipped[pr.GetHTMLURL()] {
					skip = true
					break
				}
			}
			if skip {
				fmt.Printf("%s\n", fmt.Sprintf("Skipping hash %s because one of its PRs was previously skipped", h))
				continue
			}
		}

		// Display changes and PRs, and handle auto-approve for duplicate-only hashes
		if allDup, originals := isAllDuplicateApproved(h, changeMap, firstSeen, approved); allDup {
			approved[h] = true
			var origs []string
			for _, o := range originals {
				origs = append(origs, o)
			}
			sort.Strings(origs)
			fmt.Printf("%s\n", fmt.Sprintf("All changes for hash %s are duplicates of %v and already approved — auto-approving.", h, origs))
			continue
		}

		// Print changes and mark firstSeen
		if changes, ok := changeMap[h]; ok {
			fmt.Println("Changes:")
			printChangesAndMarkFirstSeen(h, changes, firstSeen)
		} else {
			fmt.Println("No changes recorded for this hash.")
		}

		// Show associated PRs
		prCount := 0
		firstPrKey := ""
		// we only need the side-effect of printing inside showAssociatedPRs, no need to keep the return
		_ = showAssociatedPRs(h, hashPrMap, &prCount, &firstPrKey)
		if prCount == 0 {
			fmt.Println("No PRs associated with this hash.")
		}

		// Determine progress index for PRs (global)
		prProgressIndex := 1
		if firstPrKey != "" {
			if v, ok := prIndexMap[firstPrKey]; ok {
				prProgressIndex = v
			}
		}

		// Prompt the user for action and update approved/declined/prSkipped
		promptActionForHash(h, idx, total, prProgressIndex, totalPRs, in, g, propagate, approved, declined, prSkipped, hashPrMap, prMap)
	}

	// After prompting, process approvals for PRs
	processApprovals(prMap, approved, declined, prSkipped, hashPrMap, g, dryRun)

	return nil
}

// collectHashesForUsers normalizes the comma-separated user input and returns a
// sorted list of unique hashes for those users (case-insensitive match against map keys).
func collectHashesForUsers(user string, userHashPrMap gh.GhPrHashMap) []string {
	hashesMap := map[string]struct{}{}
	for _, u := range strings.Split(user, ",") {
		if userMap, ok := userHashPrMap[u]; ok {
			for h := range userMap {
				hashesMap[h] = struct{}{}
			}
		} else {
			for uname, userMap := range userHashPrMap {
				if strings.EqualFold(uname, u) {
					for h := range userMap {
						hashesMap[h] = struct{}{}
					}
					break
				}
			}
		}
	}
	var hashes []string
	for h := range hashesMap {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)
	return hashes
}

// buildUniquePrKeys returns a sorted slice of unique PR keys and an index map (1-based)
// that maps PR key to its index in the sorted list.
func buildUniquePrKeys(hashes []string, hashPrMap gh.HashPrMap) ([]string, map[string]int) {
	prKeySet := map[string]struct{}{}
	var uniquePrKeys []string
	for _, h := range hashes {
		if prs, ok := hashPrMap[h]; ok {
			for _, pr := range prs {
				k := pr.GetHTMLURL()
				if _, seen := prKeySet[k]; !seen {
					prKeySet[k] = struct{}{}
					uniquePrKeys = append(uniquePrKeys, k)
				}
			}
		}
	}
	sort.Strings(uniquePrKeys)
	prIndexMap := map[string]int{}
	for i, k := range uniquePrKeys {
		prIndexMap[k] = i + 1
	}
	return uniquePrKeys, prIndexMap
}

// isAllDuplicateApproved checks whether all change lines for hash `h` were first
// seen under other hashes that have already been approved. It returns (true, list)
// where the list contains the original hashes that covered the changes.
func isAllDuplicateApproved(h string, changeMap gh.HashChangeMap, firstSeen map[string]string, approved map[string]bool) (bool, []string) {
	changes, ok := changeMap[h]
	if !ok {
		return false, nil
	}
	allDup := true
	originalsSet := map[string]struct{}{}
	for _, line := range changes {
		if first, seen := firstSeen[line]; seen {
			if first == h {
				allDup = false
				break
			}
			if !approved[first] {
				allDup = false
				break
			}
			originalsSet[first] = struct{}{}
		} else {
			allDup = false
			break
		}
	}
	if !allDup || len(originalsSet) == 0 {
		return false, nil
	}
	var originals []string
	for o := range originalsSet {
		originals = append(originals, o)
	}
	sort.Strings(originals)
	return true, originals
}

// printChangesAndMarkFirstSeen prints change lines for `h`, marking duplicates and
// recording firstSeen for lines not seen before.
func printChangesAndMarkFirstSeen(h string, changes []string, firstSeen map[string]string) {
	for _, line := range changes {
		if first, seen := firstSeen[line]; seen {
			fmt.Printf("  %s %s\n", colorize(cGreen, fmt.Sprintf("[duplicate of %s]", first)), colorize(cGreen, line))
		} else {
			fmt.Printf("  %s\n", colorize(cCyan, line))
			firstSeen[line] = h
		}
	}
}

// showAssociatedPRs prints associated PRs for a given hash and returns the PR slice.
// It also sets prCount and firstPrKey via pointers for callers to use.
func showAssociatedPRs(h string, hashPrMap gh.HashPrMap, prCount *int, firstPrKey *string) []*github.PullRequest {
	prs := []*github.PullRequest(nil)
	if ps, ok := hashPrMap[h]; ok {
		*prCount = len(ps)
		fmt.Println("Associated PRs:")
		for i, pr := range ps {
			fmt.Printf("  %s %s\n", colorize(cYellow, fmt.Sprintf("[%d/%d]", i+1, *prCount)), colorize(cYellow, pr.GetTitle()))
			fmt.Printf("    %s\n", colorize(cYellow, pr.GetHTMLURL()))
			if i == 0 {
				*firstPrKey = pr.GetHTMLURL()
			}
			prs = append(prs, pr)
		}
	}
	return prs
}

// promptActionForHash prompts the user for an action on a hash and updates maps in-place.
func promptActionForHash(h string, idx, total, prProgressIndex, totalPRs int, in *bufio.Reader, g *gh.GhClient, propagate bool, approved, declined, prSkipped map[string]bool, hashPrMap gh.HashPrMap, prMap map[string][]string) {
	for {
		fmt.Print(colorize(cOrange, fmt.Sprintf("pr %d/%d hash: %d/%d approve this hash? (y/n/s/q) ", prProgressIndex, totalPRs, idx+1, total)))
		input, _ := in.ReadString('\n')
		input = strings.TrimSpace(strings.ToLower(input))
		if input == "y" || input == "a" {
			approved[h] = true
			if propagate {
				autoApproveLinkedHashes(h, approved, declined, hashPrMap, prMap)
			}
			break
		} else if input == "n" || input == "d" {
			declined[h] = true
			autoDeclineLinkedHashes(h, declined, prSkipped, hashPrMap, prMap)
			break
		} else if input == "q" {
			fmt.Println("Quitting manual approval early.")
			os.Exit(0)
		} else if input == "s" {
			comment := ""
			prs := hashPrMap[h]
			for _, pr := range prs {
				c, err := g.GetPrComment(pr)
				if err != nil {
					fmt.Printf("%s\n", colorize(cRed, fmt.Sprintf("Error fetching comment for PR %s: %v", pr.GetHTMLURL(), err)))
					continue
				}
				if c != "" {
					comment = comment + colorize(cCyan, "\n\n--------"+strings.Repeat("-", len("From PR "+pr.GetHTMLURL()))+"\n")
					comment = comment + colorize(cCyan, "--- From PR "+pr.GetHTMLURL()+" ---\n")
					comment = comment + colorize(cCyan, "--------"+strings.Repeat("-", len("From PR "+pr.GetHTMLURL()))+"\n\n")
					comment = comment + colorize(cGreen, c)
				}
			}

			if comment != "" {
				fmt.Printf("%s\n", colorize(cGreen, "Review comment:"))
				fmt.Printf("%s\n", comment)
			} else {
				fmt.Printf("%s\n", colorize(cYellow, "No review comment found for this hash."))
			}
		} else {
			fmt.Println("Please enter y (approve), n (decline), s (show comment) or q (quit)")
		}
	}
}

// processApprovals walks prMap and approves PRs where all hashes are approved.
func processApprovals(prMap map[string][]string, approved, declined, prSkipped map[string]bool, hashPrMap gh.HashPrMap, g *gh.GhClient, dryRun bool) {
	for prKey, phashes := range prMap {
		if len(phashes) == 0 {
			continue
		}
		if prSkipped[prKey] {
			fmt.Printf("%s\n", colorize(cYellow, fmt.Sprintf("Not approving PR %s (skipped due to a declined hash)", prKey)))
			continue
		}
		allApproved := true
		for _, ph := range phashes {
			if declined[ph] {
				allApproved = false
				break
			}
			if len(approved) > 0 {
				if !approved[ph] {
					allApproved = false
					break
				}
			}
		}
		if allApproved {
			found := false
			for _, prs := range hashPrMap {
				for _, pr := range prs {
					if pr.GetHTMLURL() == prKey {
						if dryRun {
							fmt.Printf("%s\n", colorize(cYellow, fmt.Sprintf("[dry-run] Would approve PR %s", prKey)))
						} else {
							if err := g.ApprovePr(pr); err != nil {
								fmt.Printf("%s\n", colorize(cRed, fmt.Sprintf("Failed to approve PR %s: %v", prKey, err)))
							} else {
								fmt.Printf("%s\n", colorize(cGreen, fmt.Sprintf("Approved PR %s", prKey)))
							}
						}
						found = true
						break
					}
				}
				if found {
					break
				}
			}
			if !found {
				fmt.Printf("%s\n", colorize(cRed, fmt.Sprintf("Could not find PR object for %s to approve", prKey)))
			}
		}
	}
}

// autoApproveLinkedHashes auto-approves hashes that are linked in the same PR(s) as
// the provided hash `h`. It updates the provided `approved` map in-place and uses
// `declined` to avoid approving hashes that were already declined.
func autoApproveLinkedHashes(h string, approved, declined map[string]bool, hashPrMap gh.HashPrMap, prMap map[string][]string) {
	if prs, ok := hashPrMap[h]; ok {
		for _, pr := range prs {
			prKey := pr.GetHTMLURL()
			if linked, lok := prMap[prKey]; lok {
				for _, linkedHash := range linked {
					if linkedHash == h {
						continue
					}
					// don't auto-approve hashes that are already declined or already approved
					if !approved[linkedHash] && !declined[linkedHash] {
						approved[linkedHash] = true
						fmt.Printf("%s\n", colorize(cYellow, fmt.Sprintf("Auto-approved linked hash %s (from PR %s)", linkedHash, prKey)))
					}
				}
			}
		}
	}
}

// autoDeclineLinkedHashes marks PRs containing `h` as skipped and marks other hashes
// linked to those PRs as declined. It updates `declined` and `prSkipped` maps in-place.
func autoDeclineLinkedHashes(h string, declined, prSkipped map[string]bool, hashPrMap gh.HashPrMap, prMap map[string][]string) {
	if prs, ok := hashPrMap[h]; ok {
		for _, pr := range prs {
			prKey := pr.GetHTMLURL()
			if !prSkipped[prKey] {
				prSkipped[prKey] = true
				fmt.Printf("%s\n", colorize(cYellow, fmt.Sprintf("Skipping PR %s because hash %s was declined", prKey, h)))
			}
			if linked, lok := prMap[prKey]; lok {
				for _, linkedHash := range linked {
					if linkedHash == h {
						continue
					}
					if !declined[linkedHash] {
						declined[linkedHash] = true
						// ensure we won't prompt these later
						fmt.Printf("%s\n", colorize(cYellow, fmt.Sprintf("Marked linked hash %s as declined due to PR %s", linkedHash, prKey)))
					}
				}
			}
		}
	}
}
