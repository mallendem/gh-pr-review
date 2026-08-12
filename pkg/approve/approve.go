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

// Decisions tracks what the reviewer has decided so far. Approved and Declined
// are keyed by hash; Skipped and Committed are keyed by PR URL.
type Decisions struct {
	Approved  map[string]bool
	Declined  map[string]bool
	Skipped   map[string]bool
	Committed map[string]bool
}

func NewDecisions() Decisions {
	return Decisions{
		Approved:  map[string]bool{},
		Declined:  map[string]bool{},
		Skipped:   map[string]bool{},
		Committed: map[string]bool{},
	}
}

// Approve marks a hash approved, clearing any earlier decline.
func (d Decisions) Approve(h string) {
	delete(d.Declined, h)
	d.Approved[h] = true
}

// Decline marks a hash declined, clearing any earlier approval.
func (d Decisions) Decline(h string) {
	delete(d.Approved, h)
	d.Declined[h] = true
}

// PrApproved reports whether every hash in a PR has been approved and none
// declined. A PR with no hashes is never approvable.
func (d Decisions) PrApproved(hashes []string) bool {
	if len(hashes) == 0 {
		return false
	}
	for _, h := range hashes {
		if d.Declined[h] || !d.Approved[h] {
			return false
		}
	}
	return true
}

// PrCommitted reports whether every hash in a PR has already been committed.
func (d Decisions) PrCommitted(hashes []string) bool {
	if len(hashes) == 0 {
		return false
	}
	for _, h := range hashes {
		if !d.Committed[h] {
			return false
		}
	}
	return true
}

// PrDeclined reports whether any hash in a PR was declined.
func (d Decisions) PrDeclined(hashes []string) bool {
	for _, h := range hashes {
		if d.Declined[h] {
			return true
		}
	}
	return false
}

// StagedPRs returns the sorted URLs of PRs that would be approved right now:
// fully approved and not skipped. It also clears the skip flag from any PR that
// has since become fully approved, so the caller's view stays consistent.
func (d Decisions) StagedPRs(prHashes gh.PrHashMap) []string {
	var staged []string
	for prKey, hashes := range prHashes {
		if !d.PrApproved(hashes) {
			continue
		}
		delete(d.Skipped, prKey)
		staged = append(staged, prKey)
	}
	sort.Strings(staged)
	return staged
}

func ApprovePullRequest(users []string) error {
	c := gh.NewGhClient()
	c.PrintChangesPerUser(users)
	return nil
}

func PrintUsersWithPrs() {
	g := gh.NewGhClient()
	set, err := g.GetPrReviewRequested()
	if err != nil {
		fmt.Println(colorize(cYellow, fmt.Sprintf("Error fetching PR review requests: %v", err)))
		return
	}
	for _, user := range set.Users() {
		fmt.Println(colorize(cYellow, user))
	}
}

func ApprovePrByHash(hashes []string) {
	g := gh.NewGhClient()
	set, err := g.GetPrReviewRequested()
	if err != nil {
		fmt.Println(colorize(cYellow, fmt.Sprintf("Error fetching PR review requests: %v", err)))
		return
	}
	for _, h := range hashes {
		prs, ok := set.HashPRs[h]
		if !ok {
			fmt.Println(colorize(cYellow, fmt.Sprintf("No PRs found for hash: %s", h)))
			continue
		}
		for _, pr := range prs {
			prKey := pr.GetHTMLURL()
			fmt.Printf("%s %s\n", colorize(cYellow, "Found PR for hash"), colorize(cYellow, fmt.Sprintf("%s: %s", h, prKey)))
			extras := otherHashes(set.PRHashes[prKey], h)
			if len(extras) == 0 {
				continue
			}
			fmt.Println(colorize(cYellow, "  There are also other hashes linked to this PR:"))
			for _, ex := range extras {
				fmt.Println(colorize(cGreen, fmt.Sprintf("    %s", ex)))
				fmt.Println(colorize(cYellow, fmt.Sprintf("\t  Changes for hash %s:", ex)))
				for _, line := range set.Changes[ex] {
					fmt.Println(colorize(cRed, fmt.Sprintf("\t    %s", line)))
				}
			}
		}
	}
}

// otherHashes returns every hash in the list except the given one.
func otherHashes(hashes []string, except string) []string {
	var extras []string
	for _, h := range hashes {
		if h != except {
			extras = append(extras, h)
		}
	}
	return extras
}

// ManualApproval interactively reviews hashes for the given user and approves PRs
// where all hashes are approved. propagate auto-approves linked hashes; dryRun
// skips actual GitHub API calls.
func ManualApproval(user string, propagate bool, dryRun bool) error {
	g := gh.NewGhClient()
	set, err := g.GetPrReviewRequested()
	if err != nil {
		return fmt.Errorf("error fetching PR review requests: %w", err)
	}
	for _, w := range set.Warnings {
		fmt.Println(colorize(cYellow, "warning: "+w))
	}

	hashes := CollectHashesForUsers(user, set.UsersToHashes)
	if len(hashes) == 0 {
		fmt.Println(colorize(cYellow, fmt.Sprintf("No hashes found for user %s", user)))
		return nil
	}

	dec := NewDecisions()
	in := bufio.NewReader(os.Stdin)
	firstSeen := map[string]string{}
	total := len(hashes)

	uniquePrKeys, prIndexMap := buildUniquePrKeys(hashes, set.HashPRs)
	totalPRs := len(uniquePrKeys)

	for idx, h := range hashes {
		if dec.Approved[h] || dec.Declined[h] {
			continue
		}

		if isHashSkipped(h, set.HashPRs, dec.Skipped) {
			fmt.Printf("Skipping hash %s because one of its PRs was previously skipped\n", h)
			continue
		}

		if allDup, originals := isAllDuplicateApproved(h, set.Changes, firstSeen, dec.Approved); allDup {
			dec.Approved[h] = true
			fmt.Printf("All changes for hash %s are duplicates of %v and already approved — auto-approving.\n", h, originals)
			continue
		}

		if changes, ok := set.Changes[h]; ok {
			fmt.Println("Changes:")
			printChangesAndMarkFirstSeen(h, changes, firstSeen)
		} else {
			fmt.Println("No changes recorded for this hash.")
		}

		prCount, firstPrKey := showAssociatedPRs(h, set.HashPRs, set.Verified)
		if prCount == 0 {
			fmt.Println("No PRs associated with this hash.")
		}

		prProgressIndex := 1
		if v, ok := prIndexMap[firstPrKey]; ok {
			prProgressIndex = v
		}

		promptActionForHash(h, idx, total, prProgressIndex, totalPRs, in, propagate, dec, set)
	}

	for _, line := range ProcessApprovals(set, dec, g, dryRun, "") {
		fmt.Println(line)
	}
	return nil
}

func isHashSkipped(h string, hashPRs gh.HashPrMap, skipped map[string]bool) bool {
	for _, pr := range hashPRs[h] {
		if skipped[pr.GetHTMLURL()] {
			return true
		}
	}
	return false
}

// CollectHashesForUsers returns the sorted hashes contributed by the PRs of the
// given comma-separated users. User matching is case-insensitive.
func CollectHashesForUsers(users string, userHashPrMap gh.GhPrHashMap) []string {
	hashesMap := map[string]struct{}{}
	for _, u := range strings.Split(users, ",") {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		for uname, userMap := range userHashPrMap {
			if !strings.EqualFold(uname, u) {
				continue
			}
			for h := range userMap {
				hashesMap[h] = struct{}{}
			}
		}
	}
	hashes := make([]string, 0, len(hashesMap))
	for h := range hashesMap {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)
	return hashes
}

func buildUniquePrKeys(hashes []string, hashPRs gh.HashPrMap) ([]string, map[string]int) {
	prKeySet := map[string]struct{}{}
	var uniquePrKeys []string
	for _, h := range hashes {
		for _, pr := range hashPRs[h] {
			k := pr.GetHTMLURL()
			if _, seen := prKeySet[k]; !seen {
				prKeySet[k] = struct{}{}
				uniquePrKeys = append(uniquePrKeys, k)
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
func showAssociatedPRs(h string, hashPRs gh.HashPrMap, verified gh.PrVerifiedMap) (int, string) {
	prs, ok := hashPRs[h]
	if !ok {
		return 0, ""
	}
	fmt.Println("Associated PRs:")
	firstPrKey := ""
	for i, pr := range prs {
		prKey := pr.GetHTMLURL()
		fmt.Printf("  %s %s %s\n", colorize(cYellow, fmt.Sprintf("[%d/%d]", i+1, len(prs))), VerifiedIcon(verified[prKey]), colorize(cYellow, pr.GetTitle()))
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

func promptActionForHash(h string, idx, total, prProgressIndex, totalPRs int, in *bufio.Reader, propagate bool, dec Decisions, set *gh.ReviewSet) {
	for {
		fmt.Print(colorize(cOrange, fmt.Sprintf("pr %d/%d hash: %d/%d approve this hash? (y/n/s/q) ", prProgressIndex, totalPRs, idx+1, total)))
		input, _ := in.ReadString('\n')
		switch strings.TrimSpace(strings.ToLower(input)) {
		case "y", "a":
			dec.Approve(h)
			if propagate {
				for _, line := range ApproveLinkedHashes(h, dec, set) {
					fmt.Println(colorize(cYellow, line))
				}
			}
			return
		case "n", "d":
			dec.Decline(h)
			for _, line := range DeclineLinkedHashes(h, dec, set) {
				fmt.Println(colorize(cYellow, line))
			}
			return
		case "q":
			fmt.Println("Quitting manual approval early.")
			os.Exit(0)
		case "s":
			showPrComments(h, set.HashPRs)
		default:
			fmt.Println("Please enter y (approve), n (decline), s (show comment) or q (quit)")
		}
	}
}

func showPrComments(h string, hashPRs gh.HashPrMap) {
	var comment string
	for _, pr := range hashPRs[h] {
		c, err := gh.PrBody(pr)
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

// ProcessApprovals approves every PR whose hashes are all approved. It returns
// colorized log lines so callers can print them (CLI) or show them in a popup
// (GUI) — nothing here writes to stdout.
func ProcessApprovals(set *gh.ReviewSet, dec Decisions, g *gh.GhClient, dryRun bool, reviewBody string) []string {
	var logs []string
	var prKeys []string
	for k := range set.PRHashes {
		prKeys = append(prKeys, k)
	}
	sort.Strings(prKeys)

	for _, prKey := range prKeys {
		hashes := set.PRHashes[prKey]
		if dec.Skipped[prKey] {
			logs = append(logs, colorize(cYellow, fmt.Sprintf("Not approving PR %s (skipped due to a declined hash)", prKey)))
			continue
		}
		if !dec.PrApproved(hashes) {
			continue
		}
		pr := findPrByURL(prKey, set.HashPRs)
		if pr == nil {
			logs = append(logs, colorize(cRed, fmt.Sprintf("Could not find PR object for %s to approve", prKey)))
			continue
		}
		if dryRun {
			logs = append(logs, colorize(cYellow, fmt.Sprintf("[dry-run] Would approve PR %s", prKey)))
			continue
		}
		steps, err := g.ApprovePr(pr, reviewBody)
		for _, s := range steps {
			logs = append(logs, colorize(cCyan, "  "+s))
		}
		if err != nil {
			logs = append(logs, colorize(cRed, fmt.Sprintf("Failed to approve PR %s: %v", prKey, err)))
		} else {
			logs = append(logs, colorize(cGreen, fmt.Sprintf("Approved PR %s", prKey)))
		}
	}
	return logs
}

func findPrByURL(url string, hashPRs gh.HashPrMap) *github.PullRequest {
	for _, prs := range hashPRs {
		for _, pr := range prs {
			if pr.GetHTMLURL() == url {
				return pr
			}
		}
	}
	return nil
}

// ApproveLinkedHashes auto-approves the other hashes of every PR containing h,
// returning a log line per hash it touched.
func ApproveLinkedHashes(h string, dec Decisions, set *gh.ReviewSet) []string {
	var logs []string
	for _, pr := range set.HashPRs[h] {
		prKey := pr.GetHTMLURL()
		for _, lh := range set.PRHashes[prKey] {
			if lh == h || dec.Approved[lh] || dec.Declined[lh] {
				continue
			}
			dec.Approved[lh] = true
			logs = append(logs, fmt.Sprintf("Auto-approved linked hash %s (from PR %s)", lh, prKey))
		}
	}
	return logs
}

// DeclineLinkedHashes marks every PR containing h as skipped and declines the
// other hashes in those PRs, returning a log line per change it made.
func DeclineLinkedHashes(h string, dec Decisions, set *gh.ReviewSet) []string {
	var logs []string
	for _, pr := range set.HashPRs[h] {
		prKey := pr.GetHTMLURL()
		if !dec.Skipped[prKey] {
			dec.Skipped[prKey] = true
			logs = append(logs, fmt.Sprintf("Skipping PR %s because hash %s was declined", prKey, h))
		}
		for _, lh := range set.PRHashes[prKey] {
			if lh == h || dec.Declined[lh] {
				continue
			}
			dec.Decline(lh)
			logs = append(logs, fmt.Sprintf("Marked linked hash %s as declined due to PR %s", lh, prKey))
		}
	}
	return logs
}

// Session is everything the GUI needs to start: the fetched review data, the
// authors available to pick from, and the hashes for any pre-selected user.
type Session struct {
	Set            *gh.ReviewSet
	AvailableUsers []string
	Hashes         []string
	Client         *gh.GhClient
}

// PrepareGUI fetches review data. When user is empty the caller is expected to
// show a selection panel built from AvailableUsers; otherwise Hashes is
// pre-filtered for that user.
func PrepareGUI(user string) (*Session, error) {
	client := gh.NewGhClient()
	set, err := client.GetPrReviewRequested()
	if err != nil {
		return nil, fmt.Errorf("error fetching PR review requests: %w", err)
	}
	s := &Session{
		Set:            set,
		AvailableUsers: set.Users(),
		Client:         client,
	}
	if user != "" {
		s.Hashes = CollectHashesForUsers(user, set.UsersToHashes)
	}
	return s, nil
}
