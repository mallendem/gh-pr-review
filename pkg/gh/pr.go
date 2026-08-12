package gh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/go-github/v72/github"
	"golang.org/x/sync/errgroup"
)

// fetchDiff downloads the unified diff for a PR.
func (g *GhClient) fetchDiff(pr *github.PullRequest) (string, error) {
	req, err := http.NewRequest("GET", pr.GetURL(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.diff")

	resp, err := g.c.Client().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Without this check an error page would be parsed as a diff and
		// silently yield zero hashes.
		return "", fmt.Errorf("diff request for %s returned status %d: %s",
			pr.GetHTMLURL(), resp.StatusCode, truncate(string(body), 200))
	}
	return string(body), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// GetPrReviewRequested fetches every open PR that requested your review and
// groups their changes by content hash. A PR that cannot be fetched or parsed
// is recorded in ReviewSet.Warnings rather than failing the whole run.
func (g *GhClient) GetPrReviewRequested() (*ReviewSet, error) {
	notifications, err := g.getNotifications()
	if err != nil {
		return nil, err
	}

	set := newReviewSet()
	var mu sync.Mutex
	eg := new(errgroup.Group)
	eg.SetLimit(CONCURRENCY_LIMIT)

	for _, notification := range notifications {
		if notification.GetReason() != "review_requested" {
			continue
		}
		eg.Go(func() error {
			pr, err := g.pullRequestFor(notification)
			if err != nil {
				mu.Lock()
				set.Warnings = append(set.Warnings, err.Error())
				mu.Unlock()
				return nil
			}
			if pr == nil {
				return nil
			}

			diff, err := g.fetchDiff(pr)
			if err != nil {
				mu.Lock()
				set.Warnings = append(set.Warnings, err.Error())
				mu.Unlock()
				return nil
			}
			blocks := parseDiff(diff)
			verified := g.areCommitsVerified(pr)

			mu.Lock()
			set.add(pr, blocks, verified)
			mu.Unlock()
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}
	set.sortOccurrences()
	return set, nil
}

// pullRequestFor resolves the open PR a notification refers to. It returns
// (nil, nil) when the PR exists but is not open.
func (g *GhClient) pullRequestFor(n *github.Notification) (*github.PullRequest, error) {
	url := n.GetSubject().GetURL()
	_, numPart, ok := strings.Cut(url, "/pulls/")
	if !ok {
		return nil, fmt.Errorf("notification subject %q is not a pull request", url)
	}
	number, err := strconv.Atoi(numPart)
	if err != nil {
		return nil, fmt.Errorf("failed to parse PR number from %s: %w", url, err)
	}
	owner := n.GetRepository().GetOwner().GetLogin()
	repo := n.GetRepository().GetName()
	pr, _, err := g.c.PullRequests.Get(context.Background(), owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s/%s#%d: %w", owner, repo, number, err)
	}
	if pr == nil || pr.GetState() != "open" {
		return nil, nil
	}
	return pr, nil
}

// add records one PR's change blocks. Callers must hold the set's lock.
func (r *ReviewSet) add(pr *github.PullRequest, blocks []diffBlock, verified bool) {
	prKey := pr.GetHTMLURL()
	author := pr.GetUser().GetLogin()
	if r.UsersToHashes[author] == nil {
		r.UsersToHashes[author] = make(map[string][]*github.PullRequest)
	}
	r.Verified[prKey] = verified

	for _, b := range blocks {
		if !containsPR(r.UsersToHashes[author][b.hash], prKey) {
			r.UsersToHashes[author][b.hash] = append(r.UsersToHashes[author][b.hash], pr)
		}
		if !containsPR(r.HashPRs[b.hash], prKey) {
			r.HashPRs[b.hash] = append(r.HashPRs[b.hash], pr)
		}
		if !containsString(r.PRHashes[prKey], b.hash) {
			r.PRHashes[prKey] = append(r.PRHashes[prKey], b.hash)
		}
		if _, ok := r.Changes[b.hash]; !ok {
			r.Changes[b.hash] = b.lines
		}
		r.Occurrences[b.hash] = append(r.Occurrences[b.hash], Occurrence{
			PrURL: prKey,
			File:  b.file,
			Raw:   b.raw,
		})
	}
}

// sortOccurrences gives each hash a stable presentation order, since PRs are
// fetched concurrently.
func (r *ReviewSet) sortOccurrences() {
	for _, occs := range r.Occurrences {
		sort.SliceStable(occs, func(i, j int) bool {
			if occs[i].File != occs[j].File {
				return occs[i].File < occs[j].File
			}
			return occs[i].PrURL < occs[j].PrURL
		})
	}
}

func containsPR(prs []*github.PullRequest, url string) bool {
	for _, pr := range prs {
		if pr.GetHTMLURL() == url {
			return true
		}
	}
	return false
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// areCommitsVerified reports whether every commit in a PR is signed.
func (g *GhClient) areCommitsVerified(pr *github.PullRequest) bool {
	base := pr.GetBase()
	owner := base.GetRepo().GetOwner().GetLogin()
	repo := base.GetRepo().GetName()
	if owner == "" || repo == "" {
		return false
	}

	opt := &github.ListOptions{PerPage: 100}
	total := 0
	for {
		commits, resp, err := g.c.PullRequests.ListCommits(context.Background(), owner, repo, pr.GetNumber(), opt)
		if err != nil {
			return false
		}
		for _, c := range commits {
			if !c.GetCommit().GetVerification().GetVerified() {
				return false
			}
		}
		total += len(commits)
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return total > 0
}

// ApprovePr updates the PR's branch if needed, approves it and enables
// auto-merge. Progress is reported through emit as each step finishes rather
// than returned in a batch, so a caller driving a UI can show the work as it
// happens: every step here is a network round trip. emit may be nil. Nothing
// here may write to stdout — the GUI owns the terminal.
func (g *GhClient) ApprovePr(pr *github.PullRequest, reviewBody string, emit func(string)) error {
	logf := func(format string, args ...any) {
		if emit != nil {
			emit(fmt.Sprintf(format, args...))
		}
	}

	if pr == nil {
		return fmt.Errorf("nil PR")
	}

	base := pr.GetBase()
	if base == nil || base.GetRepo() == nil || base.GetRepo().GetOwner() == nil {
		return fmt.Errorf("unable to determine owner/repo for PR %s", pr.GetHTMLURL())
	}
	owner := base.GetRepo().GetOwner().GetLogin()
	repo := base.GetRepo().GetName()
	number := pr.GetNumber()

	// 1) Rebase the branch, but only if it is actually behind the base.
	baseRef := base.GetRef()
	headRef := pr.GetHead().GetRef()
	switch {
	case baseRef == "" || headRef == "":
		logf("warning: unable to determine refs for PR %s, skipping update-branch", pr.GetHTMLURL())
	default:
		behind, err := g.isBranchBehind(owner, repo, baseRef, headRef)
		switch {
		case err != nil:
			logf("warning: failed to check branch status for PR %s: %v", pr.GetHTMLURL(), err)
		case behind:
			if err := g.tryUpdateBranch(owner, repo, number); err != nil {
				logf("warning: failed to update branch for PR %s: %v", pr.GetHTMLURL(), err)
			}
		default:
			logf("branch for PR %s is up-to-date with base (%s), skipping update-branch", pr.GetHTMLURL(), baseRef)
		}
	}

	// 2) Approve the PR.
	approveEvent := "APPROVE"
	review := &github.PullRequestReviewRequest{Event: &approveEvent}
	if reviewBody != "" {
		review.Body = &reviewBody
	}
	if _, _, err := g.c.PullRequests.CreateReview(context.Background(), owner, repo, number, review); err != nil {
		return fmt.Errorf("failed to create approval for PR %s: %w", pr.GetHTMLURL(), err)
	}

	// 3) Enable auto-merge, falling back to an immediate squash merge.
	nodeID := pr.GetNodeID()
	if nodeID == "" {
		return fmt.Errorf("PR %s has no node ID, cant enable auto-merge", pr.GetHTMLURL())
	}
	if err := g.tryEnableAutoMerge(nodeID, pr); err != nil {
		logf("warning: enabling auto-merge failed for PR %s: %v; attempting squash merge", pr.GetHTMLURL(), err)
		if mergeErr := g.trySquashMerge(owner, repo, number, pr); mergeErr != nil {
			return fmt.Errorf("squash merge failed for PR %s: %v; original auto-merge error: %w", pr.GetHTMLURL(), mergeErr, err)
		}
		logf("squash merged PR %s", pr.GetHTMLURL())
	} else {
		logf("enabled auto-merge (GraphQL) for PR %s", pr.GetHTMLURL())
	}

	return nil
}

// tryEnableAutoMerge attempts to enable auto-merge for the given PR using GraphQL.
// It returns nil on success or an error describing the failure so callers can
// decide on fallback behavior.
func (g *GhClient) tryEnableAutoMerge(nodeID string, pr *github.PullRequest) error {
	graphqlURL := "https://api.github.com/graphql"
	mutation := `mutation EnableAutoMerge($pullId:ID!, $mergeMethod:PullRequestMergeMethod!) { enablePullRequestAutoMerge(input:{pullRequestId:$pullId, mergeMethod:$mergeMethod}) { pullRequest { id } } }`
	payload := map[string]any{
		"query": mutation,
		"variables": map[string]any{
			"pullId":      nodeID,
			"mergeMethod": "SQUASH",
		},
	}
	bodyBytes, _ := json.Marshal(payload)
	reqGQL, err := http.NewRequest("POST", graphqlURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create GraphQL request for PR %s: %w", pr.GetHTMLURL(), err)
	}
	reqGQL.Header.Set("Accept", "application/vnd.github+json")
	reqGQL.Header.Set("Content-Type", "application/json")
	reqGQL.Header.Set("Authorization", "Bearer "+g.token)
	respGQL, err := g.c.Client().Do(reqGQL)
	if err != nil {
		return fmt.Errorf("GraphQL request failed for PR %s: %w", pr.GetHTMLURL(), err)
	}
	defer func() { _ = respGQL.Body.Close() }()
	body, _ := io.ReadAll(respGQL.Body)
	if respGQL.StatusCode < 200 || respGQL.StatusCode > 299 {
		return fmt.Errorf("GraphQL enablePullRequestAutoMerge returned status %d for PR %s: %s", respGQL.StatusCode, pr.GetHTMLURL(), string(body))
	}
	var gqlResp struct {
		Data   any              `json:"data"`
		Errors []map[string]any `json:"errors"`
	}
	if err := json.Unmarshal(body, &gqlResp); err != nil {
		return fmt.Errorf("failed to decode GraphQL response for PR %s: %w", pr.GetHTMLURL(), err)
	}
	if len(gqlResp.Errors) > 0 {
		return fmt.Errorf("GraphQL returned errors for PR %s: %v", pr.GetHTMLURL(), gqlResp.Errors)
	}
	return nil
}

// trySquashMerge attempts to immediately squash-merge the given PR.
func (g *GhClient) trySquashMerge(owner, repo string, number int, pr *github.PullRequest) error {
	commitMessage := fmt.Sprintf("Squash merge PR #%d: %s", number, pr.GetTitle())
	opt := &github.PullRequestOptions{MergeMethod: "squash"}
	if _, _, err := g.c.PullRequests.Merge(context.Background(), owner, repo, number, commitMessage, opt); err != nil {
		return fmt.Errorf("merge failed for PR %s: %w", pr.GetHTMLURL(), err)
	}
	return nil
}

// PrBody returns the PR description with Dependabot's command footer stripped.
func PrBody(pr *github.PullRequest) (string, error) {
	if pr == nil {
		return "", fmt.Errorf("nil PR")
	}
	if body := strings.TrimSpace(pr.GetBody()); body != "" {
		return cleanDependabotMessage(body), nil
	}
	return "", fmt.Errorf("no comment/body found for PR %s", pr.GetHTMLURL())
}

func (g *GhClient) PrintChangesPerUser(users []string) {
	set, err := g.GetPrReviewRequested()
	if err != nil {
		fmt.Printf("Error fetching PR review requests: %v\n", err)
		return
	}
	for _, w := range set.Warnings {
		fmt.Printf("warning: %s\n", w)
	}

	// normalize and dedupe requested users into a lookup map (lowercase)
	filter := map[string]struct{}{}
	for _, u := range users {
		// cobra's StringSlice may allow comma-separated entries; split further if needed
		for _, token := range strings.Split(u, ",") {
			if n := strings.TrimSpace(token); n != "" {
				filter[strings.ToLower(n)] = struct{}{}
			}
		}
	}

	for _, user := range set.Users() {
		if len(filter) > 0 {
			if _, ok := filter[strings.ToLower(user)]; !ok {
				continue
			}
		}

		fmt.Printf("User: %s\n", user)
		for hash, prs := range set.UsersToHashes[user] {
			fmt.Printf("  Hash: %s\n", hash)
			printChanges(set.Changes[hash], "    ")

			// For each PR tied to this hash, show additional hashes associated with that PR
			for _, pr := range prs {
				extras := otherHashes(set.PRHashes[pr.GetHTMLURL()], hash)
				if len(extras) == 0 {
					continue
				}
				fmt.Printf("    Additional hashes linked in PR %s:\n", pr.GetHTMLURL())
				for _, ah := range extras {
					fmt.Printf("      %s\n", ah)
					printChanges(set.Changes[ah], "        ")
				}
			}
		}
	}
}

func printChanges(changes []string, indent string) {
	if len(changes) == 0 {
		fmt.Printf("%sNo changes found for this hash.\n", indent)
		return
	}
	fmt.Printf("%sChanges:\n", indent)
	for _, line := range changes {
		fmt.Printf("%s  %s\n", indent, line)
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
