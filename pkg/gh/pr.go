package gh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/google/go-github/v72/github"
	"golang.org/x/sync/errgroup"
)

func (g *GhClient) getPrHash(pr *github.PullRequest) ([]string, map[string][]string, map[string]string, map[string][]string, error) {
	diffURL := pr.GetURL()
	req, err := http.NewRequest("GET", diffURL, nil)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.diff")

	resp, err := g.c.Client().Do(req)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	diffBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	diff := string(diffBytes)

	var hashes []string
	var hunkLines []string
	var rawHunkLines []string
	inHunk := false
	hunkMap := make(map[string][]string)
	rawHunkMap := make(map[string][]string)
	hashFileMap := make(map[string]string)
	currentFile := ""

	flushHunk := func() {
		if len(hunkLines) == 0 {
			return
		}
		hash := sha256.Sum256([]byte(strings.Join(hunkLines, "\n")))
		h := hex.EncodeToString(hash[:])
		hashes = append(hashes, h)
		hunkMap[h] = append([]string(nil), hunkLines...)
		rawHunkMap[h] = append([]string(nil), rawHunkLines...)
		hashFileMap[h] = currentFile
		hunkLines = nil
		rawHunkLines = nil
	}

	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			flushHunk()
			inHunk = false
			if idx := strings.LastIndex(line, " b/"); idx >= 0 {
				currentFile = line[idx+3:]
			}
			continue
		}
		if strings.HasPrefix(line, "@@") {
			flushHunk()
			inHunk = true
			continue
		}
		if inHunk {
			if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
				continue
			}
			// Capture all lines (context, additions, deletions) into rawHunkLines
			rawHunkLines = append(rawHunkLines, line)
			if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
				hunkLines = append(hunkLines, normalizeHunkLine(line, currentFile))
			}
		}
	}
	flushHunk()

	return hashes, hunkMap, hashFileMap, rawHunkMap, nil
}

func (g *GhClient) GetPrReviewRequested() (GhPrHashMap, HashChangeMap, HashPrMap, PrHashMap, PrVerifiedMap, HashFileMap, HashRawChangeMap, error) {
	userHashPrMap := make(GhPrHashMap)
	n, err := g.getNotifications()
	if err != nil {
		return nil, nil, nil, nil, nil, nil, nil, err
	}

	hashChangeMap := make(map[string][]string)
	hashRawChangeMap := make(HashRawChangeMap)
	hashPrMap := make(HashPrMap)
	prHashMap := make(PrHashMap)
	prVerifiedMap := make(PrVerifiedMap)
	hashFileMap := make(HashFileMap)

	mu := sync.Mutex{}
	eg := new(errgroup.Group)
	eg.SetLimit(CONCURRENCY_LIMIT)

	for _, notification := range n {
		eg.Go(func() error {
			if notification.GetReason() != "review_requested" {
				return nil
			}
			url := notification.GetSubject().GetURL()
			owner := notification.GetRepository().GetOwner().GetLogin()
			repo := notification.GetRepository().GetName()
			prNumber, err := strconv.Atoi(strings.Split(url, "/pulls/")[1])
			if err != nil {
				return fmt.Errorf("failed to parse PR number from %s: %w", url, err)
			}
			pr, _, err := g.c.PullRequests.Get(context.Background(), owner, repo, prNumber)
			if err != nil {
				return err
			}
			if pr == nil || pr.GetState() != "open" {
				return nil
			}
			prUser := pr.GetUser().GetLogin()

			prHash, localChangeMap, localFileMap, localRawChangeMap, err := g.getPrHash(pr)
			if err != nil {
				return err
			}

			verified := g.areCommitsVerified(owner, repo, pr.GetNumber())

			mu.Lock()
			if userHashPrMap[prUser] == nil {
				userHashPrMap[prUser] = make(map[string][]*github.PullRequest)
			}
			prKey := pr.GetHTMLURL()
			prVerifiedMap[prKey] = verified
			for _, h := range prHash {
				if !containsPR(userHashPrMap[prUser][h], prKey) {
					userHashPrMap[prUser][h] = append(userHashPrMap[prUser][h], pr)
				}
				if !containsPR(hashPrMap[h], prKey) {
					hashPrMap[h] = append(hashPrMap[h], pr)
				}
				if !containsString(prHashMap[prKey], h) {
					prHashMap[prKey] = append(prHashMap[prKey], h)
				}
			}
			for k, v := range localChangeMap {
				if _, ok := hashChangeMap[k]; !ok {
					hashChangeMap[k] = v
				}
			}
			for h, file := range localFileMap {
				if hashFileMap[h] == nil {
					hashFileMap[h] = make(map[string]string)
				}
				hashFileMap[h][prKey] = file
			}
			for k, v := range localRawChangeMap {
				if _, ok := hashRawChangeMap[k]; !ok {
					hashRawChangeMap[k] = v
				}
			}
			mu.Unlock()
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, nil, nil, nil, nil, nil, nil, err
	}
	return userHashPrMap, hashChangeMap, hashPrMap, prHashMap, prVerifiedMap, hashFileMap, hashRawChangeMap, nil
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

// areCommitsVerified checks whether all commits in a PR are verified (signed).
func (g *GhClient) areCommitsVerified(owner, repo string, number int) bool {
	commits, _, err := g.c.PullRequests.ListCommits(context.Background(), owner, repo, number, nil)
	if err != nil {
		return false
	}
	for _, c := range commits {
		if c.GetCommit().GetVerification().GetVerified() != true {
			return false
		}
	}
	return len(commits) > 0
}

func (g *GhClient) ApprovePr(pr *github.PullRequest, reviewBody string) error {
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

	// 1) Try to update the branch (rebase) using the REST endpoint
	// Only attempt to update the branch if the head is behind the base branch.
	baseRef := base.GetRef()
	headRef := pr.GetHead().GetRef()
	if baseRef == "" || headRef == "" {
		fmt.Printf("warning: unable to determine refs for PR %s, skipping update-branch\n", pr.GetHTMLURL())
	} else {
		behind, err := g.isBranchBehind(owner, repo, baseRef, headRef)
		if err != nil {
			fmt.Printf("warning: failed to check branch status for PR %s: %v\n", pr.GetHTMLURL(), err)
		} else if behind {
			if err := g.tryUpdateBranch(owner, repo, number); err != nil {
				// TODO: this doesn't work, no idea why rebasing via API is so broken,
				// but we should detect if we _need_ to rebase first before trying, and
				// if it fails, we should return errors properly.
				fmt.Printf("warning: failed to update branch for PR %s: %v\n", pr.GetHTMLURL(), err)
				//return err
			}
		} else {
			fmt.Printf("branch for PR %s is up-to-date with base (%s), skipping update-branch\n", pr.GetHTMLURL(), baseRef)
		}
	}

	// 2) Approve the PR (create a review with APPROVE)
	approveEvent := "APPROVE"
	review := &github.PullRequestReviewRequest{
		Event: &approveEvent,
	}
	if reviewBody != "" {
		review.Body = &reviewBody
	}
	_, _, revErr := g.c.PullRequests.CreateReview(context.Background(), owner, repo, number, review)
	if revErr != nil {
		return fmt.Errorf("failed to create approval for PR %s: %w", pr.GetHTMLURL(), revErr)
	}

	// 3) Enable auto-merge for the PR using GraphQL mutation
	// Use the enablePullRequestAutoMerge mutation (requires PR node ID)
	nodeID := pr.GetNodeID()
	if nodeID == "" {
		return fmt.Errorf("PR %s has no node ID, cant enable auto-merge", pr.GetHTMLURL())
	} else {
		if err := g.tryEnableAutoMerge(nodeID, pr); err != nil {
			fmt.Printf("warning: enabling auto-merge failed for PR %s: %v; attempting squash merge\n", pr.GetHTMLURL(), err)
			if mergeErr := g.trySquashMerge(owner, repo, number, pr); mergeErr != nil {
				return fmt.Errorf("squash merge failed for PR %s: %v; original auto-merge error: %w", pr.GetHTMLURL(), mergeErr, err)
			}
		}
	}

	return nil
}

// tryEnableAutoMerge attempts to enable auto-merge for the given PR using GraphQL.
// It returns nil on success or an error describing the failure so callers can
// decide on fallback behavior.
func (g *GhClient) tryEnableAutoMerge(nodeID string, pr *github.PullRequest) error {
	graphqlURL := "https://api.github.com/graphql"
	mutation := `mutation EnableAutoMerge($pullId:ID!, $mergeMethod:PullRequestMergeMethod!) { enablePullRequestAutoMerge(input:{pullRequestId:$pullId, mergeMethod:$mergeMethod}) { pullRequest { id } } }`
	vars := map[string]any{
		"pullId":      nodeID,
		"mergeMethod": "SQUASH",
	}
	payload := map[string]any{
		"query":     mutation,
		"variables": vars,
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
	// inspect response for GraphQL errors
	var gqlResp struct {
		Data   any              `json:"data"`
		Errors []map[string]any `json:"errors"`
	}
	if err := json.Unmarshal(body, &gqlResp); err != nil {
		return fmt.Errorf("failed to decode GraphQL response for PR %s: %w", pr.GetHTMLURL(), err)
	} else if len(gqlResp.Errors) > 0 {
		return fmt.Errorf("GraphQL returned errors for PR %s: %v", pr.GetHTMLURL(), gqlResp.Errors)
	}
	fmt.Printf("enabled auto-merge (GraphQL) for PR %s\n", pr.GetHTMLURL())
	return nil
}

func (g *GhClient) GetPrComment(pr *github.PullRequest) (string, error) {
	if pr == nil {
		return "", fmt.Errorf("nil PR")
	}

	// Prefer the PR description/body if it's present
	if body := strings.TrimSpace(pr.GetBody()); body != "" {
		return cleanDependabotMessage(body), nil
	}
	// No comment found
	return "", fmt.Errorf("no comment/body found for PR %s", pr.GetHTMLURL())
}

func (g *GhClient) PrintChangesPerUser(users []string) {
	userHashPrMap, hashChangeMap, _, prHashMap, _, _, _, err := g.GetPrReviewRequested()
	if err != nil {
		fmt.Printf("Error fetching PR review requests: %v\n", err)
		return
	}

	// normalize and dedupe requested users into a lookup map (lowercase)
	filter := map[string]struct{}{}
	if len(users) > 0 {
		for _, u := range users {
			// cobra's StringSlice may allow comma-separated entries; split further if needed
			for _, token := range strings.Split(u, ",") {
				n := strings.TrimSpace(token)
				if n == "" {
					continue
				}
				filter[strings.ToLower(n)] = struct{}{}
			}
		}
	}

	for user, hashMap := range userHashPrMap {
		lowerUser := strings.ToLower(user)
		// if filter provided, skip users not in the filter
		if len(filter) > 0 {
			if _, ok := filter[lowerUser]; !ok {
				continue
			}
		}

		fmt.Printf("User: %s\n", user)
		for hash, prs := range hashMap {
			fmt.Printf("  Hash: %s\n", hash)
			if changes, ok := hashChangeMap[hash]; ok {
				fmt.Println("    Changes:")
				for _, line := range changes {
					fmt.Printf("      %s\n", line)
				}
			} else {
				fmt.Println("    No changes found for this hash.")
			}

			// For each PR tied to this hash, show additional hashes associated with that PR
			for _, pr := range prs {
				prKey := pr.GetHTMLURL()
				if linked, ok := prHashMap[prKey]; ok {
					var extras []string
					for _, ah := range linked {
						if ah == hash {
							continue
						}
						extras = append(extras, ah)
					}
					if len(extras) > 0 {
						fmt.Printf("    Additional hashes linked in PR %s:\n", prKey)
						for _, ah := range extras {
							fmt.Printf("      %s\n", ah)
							if changes2, ok2 := hashChangeMap[ah]; ok2 {
								fmt.Println("        Changes:")
								for _, line := range changes2 {
									fmt.Printf("          %s\n", line)
								}
							} else {
								fmt.Println("        No changes found for this hash.")
							}
						}
					}
				}
			}
		}
	}
}

// trySquashMerge attempts to immediately squash-merge the given PR.
// Returns nil on success or an error describing the failure.
func (g *GhClient) trySquashMerge(owner, repo string, number int, pr *github.PullRequest) error {
	commitMessage := fmt.Sprintf("Squash merge PR #%d: %s", number, pr.GetTitle())
	opt := &github.PullRequestOptions{
		MergeMethod: "squash",
		CommitTitle: "",
	}
	_, _, err := g.c.PullRequests.Merge(
		context.Background(),
		owner,
		repo,
		number,
		commitMessage,
		opt)

	if err != nil {
		return fmt.Errorf("merge failed for PR %s: %w", pr.GetHTMLURL(), err)
	}
	return nil
}
