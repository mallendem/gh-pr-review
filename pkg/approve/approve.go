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
	userHashPrMap, _, _, _, _, err := g.GetPrReviewRequested()
	if err != nil {
		fmt.Println(colorize(cYellow, fmt.Sprintf("Error fetching PR review requests: %v", err)))
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
}

func ApprovePrByHash(hashes []string) {
	g := gh.NewGhClient()
	_, changeMap, hMap, prMap, _, err := g.GetPrReviewRequested()
	if err != nil {
		fmt.Println(colorize(cYellow, fmt.Sprintf("Error fetching PR review requests: %v", err)))
		return
	}
	for _, h := range hashes {
		prs, ok := hMap[h]
		if !ok {
			fmt.Println(colorize(cYellow, fmt.Sprintf("No PRs found for hash: %s", h)))
			continue
		}
		for _, pr := range prs {
			fmt.Printf("%s %s\n", colorize(cYellow, "Found PR for hash"), colorize(cYellow, fmt.Sprintf("%s: %s", h, pr.GetHTMLURL())))
			prKey := pr.GetHTMLURL()
			linked, ok := prMap[prKey]
			if !ok {
				continue
			}
			var extras []string
			for _, ah := range linked {
				if ah != h {
					extras = append(extras, ah)
				}
			}
			if len(extras) > 0 {
				fmt.Println(colorize(cYellow, "  There are also other hashes linked to this PR:"))
				for _, ex := range extras {
					fmt.Println(colorize(cGreen, fmt.Sprintf("    %s", ex)))
					fmt.Println(colorize(cYellow, fmt.Sprintf("\t  Changes for hash %s:", ex)))
					if changes, ok := changeMap[ex]; ok {
						for _, line := range changes {
							fmt.Println(colorize(cRed, fmt.Sprintf("\t    %s", line)))
						}
					}
				}
			}
		}
	}
}

// ManualApproval interactively reviews hashes for the given user and approves PRs
// where all hashes are approved. propagate auto-approves linked hashes; dryRun
// skips actual GitHub API calls.
func ManualApproval(user string, propagate bool, dryRun bool) error {
	g := gh.NewGhClient()
	userHashPrMap, changeMap, hashPrMap, prMap, verifiedMap, err := g.GetPrReviewRequested()
	if err != nil {
		return fmt.Errorf("error fetching PR review requests: %w", err)
	}

	hashes := collectHashesForUsers(user, userHashPrMap)
	if len(hashes) == 0 {
		fmt.Println(colorize(cYellow, fmt.Sprintf("No hashes found for user %s", user)))
		return nil
	}

	approved := map[string]bool{}
	declined := map[string]bool{}
	prSkipped := map[string]bool{}

	in := bufio.NewReader(os.Stdin)
	firstSeen := map[string]string{}
	total := len(hashes)

	uniquePrKeys, prIndexMap := buildUniquePrKeys(hashes, hashPrMap)
	totalPRs := len(uniquePrKeys)

	for idx, h := range hashes {
		if approved[h] || declined[h] {
			continue
		}

		if isHashSkipped(h, hashPrMap, prSkipped) {
			fmt.Printf("Skipping hash %s because one of its PRs was previously skipped\n", h)
			continue
		}

		if allDup, originals := isAllDuplicateApproved(h, changeMap, firstSeen, approved); allDup {
			approved[h] = true
			fmt.Printf("All changes for hash %s are duplicates of %v and already approved — auto-approving.\n", h, originals)
			continue
		}

		if changes, ok := changeMap[h]; ok {
			fmt.Println("Changes:")
			printChangesAndMarkFirstSeen(h, changes, firstSeen)
		} else {
			fmt.Println("No changes recorded for this hash.")
		}

		prCount, firstPrKey := showAssociatedPRs(h, hashPrMap, verifiedMap)
		if prCount == 0 {
			fmt.Println("No PRs associated with this hash.")
		}

		prProgressIndex := 1
		if firstPrKey != "" {
			if v, ok := prIndexMap[firstPrKey]; ok {
				prProgressIndex = v
			}
		}

		promptActionForHash(h, idx, total, prProgressIndex, totalPRs, in, g, propagate, approved, declined, prSkipped, hashPrMap, prMap)
	}

	ProcessApprovals(prMap, approved, declined, prSkipped, hashPrMap, g, dryRun)
	return nil
}

func isHashSkipped(h string, hashPrMap gh.HashPrMap, prSkipped map[string]bool) bool {
	if prs, ok := hashPrMap[h]; ok {
		for _, pr := range prs {
			if prSkipped[pr.GetHTMLURL()] {
				return true
			}
		}
	}
	return false
}

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

func isAllDuplicateApproved(h string, changeMap gh.HashChangeMap, firstSeen map[string]string, approved map[string]bool) (bool, []string) {
	changes, ok := changeMap[h]
	if !ok {
		return false, nil
	}
	originalsSet := map[string]struct{}{}
	for _, line := range changes {
		first, seen := firstSeen[line]
		if !seen || first == h || !approved[first] {
			return false, nil
		}
		originalsSet[first] = struct{}{}
	}
	if len(originalsSet) == 0 {
		return false, nil
	}
	var originals []string
	for o := range originalsSet {
		originals = append(originals, o)
	}
	sort.Strings(originals)
	return true, originals
}

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

// showAssociatedPRs prints associated PRs for a given hash with verification status
// and returns the count and the first PR's URL.
func showAssociatedPRs(h string, hashPrMap gh.HashPrMap, verifiedMap gh.PrVerifiedMap) (int, string) {
	prs, ok := hashPrMap[h]
	if !ok {
		return 0, ""
	}
	fmt.Println("Associated PRs:")
	firstPrKey := ""
	for i, pr := range prs {
		prKey := pr.GetHTMLURL()
		verifiedIcon := VerifiedIcon(verifiedMap[prKey])
		fmt.Printf("  %s %s %s\n", colorize(cYellow, fmt.Sprintf("[%d/%d]", i+1, len(prs))), verifiedIcon, colorize(cYellow, pr.GetTitle()))
		fmt.Printf("    %s\n", colorize(cYellow, prKey))
		if i == 0 {
			firstPrKey = prKey
		}
	}
	return len(prs), firstPrKey
}

// VerifiedIcon returns a checkmark or X emoji based on verification status.
func VerifiedIcon(verified bool) string {
	if verified {
		return "✅"
	}
	return "❌"
}

func promptActionForHash(h string, idx, total, prProgressIndex, totalPRs int, in *bufio.Reader, g *gh.GhClient, propagate bool, approved, declined, prSkipped map[string]bool, hashPrMap gh.HashPrMap, prMap map[string][]string) {
	for {
		fmt.Print(colorize(cOrange, fmt.Sprintf("pr %d/%d hash: %d/%d approve this hash? (y/n/s/q) ", prProgressIndex, totalPRs, idx+1, total)))
		input, _ := in.ReadString('\n')
		input = strings.TrimSpace(strings.ToLower(input))
		switch input {
		case "y", "a":
			approved[h] = true
			if propagate {
				ApproveLinkedHashes(h, approved, declined, hashPrMap, prMap, false)
			}
			return
		case "n", "d":
			declined[h] = true
			DeclineLinkedHashes(h, declined, prSkipped, hashPrMap, prMap, false)
			return
		case "q":
			fmt.Println("Quitting manual approval early.")
			os.Exit(0)
		case "s":
			showPrComments(h, hashPrMap, g)
		default:
			fmt.Println("Please enter y (approve), n (decline), s (show comment) or q (quit)")
		}
	}
}

func showPrComments(h string, hashPrMap gh.HashPrMap, g *gh.GhClient) {
	var comment string
	for _, pr := range hashPrMap[h] {
		c, err := g.GetPrComment(pr)
		if err != nil {
			fmt.Println(colorize(cRed, fmt.Sprintf("Error fetching comment for PR %s: %v", pr.GetHTMLURL(), err)))
			continue
		}
		if c != "" {
			sep := strings.Repeat("-", len("From PR "+pr.GetHTMLURL()))
			comment += colorize(cCyan, "\n\n--------"+sep+"\n")
			comment += colorize(cCyan, "--- From PR "+pr.GetHTMLURL()+" ---\n")
			comment += colorize(cCyan, "--------"+sep+"\n\n")
			comment += colorize(cGreen, c)
		}
	}
	if comment != "" {
		fmt.Println(colorize(cGreen, "Review comment:"))
		fmt.Println(comment)
	} else {
		fmt.Println(colorize(cYellow, "No review comment found for this hash."))
	}
}

// ProcessApprovals walks prMap and approves PRs where all hashes are approved.
func ProcessApprovals(prMap map[string][]string, approved, declined, prSkipped map[string]bool, hashPrMap gh.HashPrMap, g *gh.GhClient, dryRun bool) {
	for prKey, phashes := range prMap {
		if len(phashes) == 0 || prSkipped[prKey] {
			if prSkipped[prKey] {
				fmt.Println(colorize(cYellow, fmt.Sprintf("Not approving PR %s (skipped due to a declined hash)", prKey)))
			}
			continue
		}
		if !allHashesApproved(phashes, approved, declined) {
			continue
		}
		pr := findPrByURL(prKey, hashPrMap)
		if pr == nil {
			fmt.Println(colorize(cRed, fmt.Sprintf("Could not find PR object for %s to approve", prKey)))
			continue
		}
		if dryRun {
			fmt.Println(colorize(cYellow, fmt.Sprintf("[dry-run] Would approve PR %s", prKey)))
		} else if err := g.ApprovePr(pr); err != nil {
			fmt.Println(colorize(cRed, fmt.Sprintf("Failed to approve PR %s: %v", prKey, err)))
		} else {
			fmt.Println(colorize(cGreen, fmt.Sprintf("Approved PR %s", prKey)))
		}
	}
}

func allHashesApproved(phashes []string, approved, declined map[string]bool) bool {
	for _, ph := range phashes {
		if declined[ph] || !approved[ph] {
			return false
		}
	}
	return len(approved) > 0
}

func findPrByURL(url string, hashPrMap gh.HashPrMap) *github.PullRequest {
	for _, prs := range hashPrMap {
		for _, pr := range prs {
			if pr.GetHTMLURL() == url {
				return pr
			}
		}
	}
	return nil
}

// ApproveLinkedHashes auto-approves hashes linked in the same PR(s) as h.
// When quiet is true, no output is printed.
func ApproveLinkedHashes(h string, approved, declined map[string]bool, hashPrMap gh.HashPrMap, prMap map[string][]string, quiet bool) {
	prs, ok := hashPrMap[h]
	if !ok {
		return
	}
	for _, pr := range prs {
		prKey := pr.GetHTMLURL()
		linked, ok := prMap[prKey]
		if !ok {
			continue
		}
		for _, lh := range linked {
			if lh == h || approved[lh] || declined[lh] {
				continue
			}
			approved[lh] = true
			if !quiet {
				fmt.Println(colorize(cYellow, fmt.Sprintf("Auto-approved linked hash %s (from PR %s)", lh, prKey)))
			}
		}
	}
}

// DeclineLinkedHashes marks PRs containing h as skipped and declines linked hashes.
// When quiet is true, no output is printed.
func DeclineLinkedHashes(h string, declined, prSkipped map[string]bool, hashPrMap gh.HashPrMap, prMap map[string][]string, quiet bool) {
	prs, ok := hashPrMap[h]
	if !ok {
		return
	}
	for _, pr := range prs {
		prKey := pr.GetHTMLURL()
		if !prSkipped[prKey] {
			prSkipped[prKey] = true
			if !quiet {
				fmt.Println(colorize(cYellow, fmt.Sprintf("Skipping PR %s because hash %s was declined", prKey, h)))
			}
		}
		linked, ok := prMap[prKey]
		if !ok {
			continue
		}
		for _, lh := range linked {
			if lh == h || declined[lh] {
				continue
			}
			declined[lh] = true
			if !quiet {
				fmt.Println(colorize(cYellow, fmt.Sprintf("Marked linked hash %s as declined due to PR %s", lh, prKey)))
			}
		}
	}
}

// PrepareGUI fetches data and, if user is empty, returns the list of available
// usernames so a selection panel can be shown. When user is non-empty it behaves
// like PrepareManualApproval and pre-filters hashes for that user.
func PrepareGUI(user string) (hashes []string, availableUsers []string, userHashPrMap gh.GhPrHashMap, changeMap gh.HashChangeMap, hashPrMap gh.HashPrMap, prMap map[string][]string, verifiedMap gh.PrVerifiedMap, client *gh.GhClient, err error) {
	client = gh.NewGhClient()
	userHashPrMap, changeMap, hashPrMap, prMap, verifiedMap, err = client.GetPrReviewRequested()
	if err != nil {
		return nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("error fetching PR review requests: %w", err)
	}
	// build sorted list of available users
	for u := range userHashPrMap {
		availableUsers = append(availableUsers, u)
	}
	sort.Strings(availableUsers)

	if user != "" {
		hashes = collectHashesForUsers(user, userHashPrMap)
	}
	return
}

// CollectHashesForUsers is an exported wrapper around collectHashesForUsers for
// use by the GUI after user selection.
func CollectHashesForUsers(user string, userHashPrMap gh.GhPrHashMap) []string {
	return collectHashesForUsers(user, userHashPrMap)
}

// PrepareManualApproval fetches data required for manual approval (used by both CLI and GUI).
func PrepareManualApproval(user string) ([]string, gh.HashChangeMap, gh.HashPrMap, map[string][]string, gh.PrVerifiedMap, *gh.GhClient, error) {
	g := gh.NewGhClient()
	userHashPrMap, changeMap, hashPrMap, prMap, verifiedMap, err := g.GetPrReviewRequested()
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("error fetching PR review requests: %w", err)
	}
	hashes := collectHashesForUsers(user, userHashPrMap)
	return hashes, changeMap, hashPrMap, prMap, verifiedMap, g, nil
}
