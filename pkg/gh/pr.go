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
	"github.com/mallendem/gh-pr-review/pkg/utils"
	"golang.org/x/sync/errgroup"
)

func (g *GhClient) getPrHash(pr *github.PullRequest) ([]string, map[string][]string, error) {
	diffURL := pr.GetURL()
	req, err := http.NewRequest("GET", diffURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.diff")

	resp, err := g.c.Client().Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	diffBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	diff := string(diffBytes)

	var hashes []string
	hunkLines := make([]string, 0)
	inHunk := false
	hunkMap := make(map[string][]string)

	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@") {
			if len(hunkLines) > 0 {
				hash := sha256.Sum256([]byte(strings.Join(hunkLines, "\n")))
				h := hex.EncodeToString(hash[:])
				hashes = append(hashes, h)
				hunkMap[h] = append([]string(nil), hunkLines...) // copy
				hunkLines = nil
			}
			inHunk = true
			continue
		}
		if inHunk {
			// Only consider added/removed lines, not file headers
			if (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) &&
				!strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") {
				hunkLines = append(hunkLines, line)
			}
		}
	}
	// Hash the last hunk if present
	if len(hunkLines) > 0 {
		hash := sha256.Sum256([]byte(strings.Join(hunkLines, "\n")))
		h := hex.EncodeToString(hash[:])
		hashes = append(hashes, h)
		hunkMap[h] = append([]string(nil), hunkLines...)
	}

	return hashes, hunkMap, nil
}

func (g *GhClient) GetPrReviewRequested() (GhPrHashMap, HashChangeMap, HashPrMap, PrHashMap, error) {
	userHashPrMap := make(GhPrHashMap)
	n, err := g.getNotifications()
	if err != nil {
		return nil, nil, nil, nil, err
	}

	hashChangeMap := make(map[string][]string)
	hashPrMap := make(HashPrMap)
	prHashMap := make(PrHashMap)

	m := sync.Mutex{}
	eg := new(errgroup.Group)
	eg.SetLimit(CONCURRENCY_LIMIT)

	for _, notification := range n {
		notification := notification // capture loop variable
		eg.Go(func() error {
			if notification.GetReason() == "review_requested" {
				url := notification.GetSubject().GetURL()
				pr, _, err := g.c.PullRequests.Get(
					context.Background(),
					notification.GetRepository().GetOwner().GetLogin(),
					notification.GetRepository().GetName(),
					utils.Must(strconv.Atoi(strings.Split(url, "/pulls/")[1])),
				)
				if err != nil {
					return err
				}
				if pr == nil || pr.GetState() != "open" {
					return nil
				}
				prUser := pr.GetUser().GetLogin()

				// get hashes and local map (no shared map mutation here)
				prHash, prMap, err := g.getPrHash(pr)
				if err != nil {
					return err
				}

				// merge under lock
				m.Lock()
				if userHashPrMap[prUser] == nil {
					userHashPrMap[prUser] = make(map[string][]*github.PullRequest)
				}
				prKey := pr.GetHTMLURL()
				for _, h := range prHash {
					// append pr to userHashPrMap[prUser][h] if not present
					present := false
					for _, exist := range userHashPrMap[prUser][h] {
						if exist.GetHTMLURL() == prKey {
							present = true
							break
						}
					}
					if !present {
						userHashPrMap[prUser][h] = append(userHashPrMap[prUser][h], pr)
					}

					// also populate global hash->PR map with dedupe by URL
					prList := hashPrMap[h]
					found := false
					for _, exist := range prList {
						if exist.GetHTMLURL() == prKey {
							found = true
							break
						}
					}
					if !found {
						hashPrMap[h] = append(hashPrMap[h], pr)
					}

					// populate PR->hash map with dedupe
					hashes := prHashMap[prKey]
					have := false
					for _, existH := range hashes {
						if existH == h {
							have = true
							break
						}
					}
					if !have {
						prHashMap[prKey] = append(prHashMap[prKey], h)
					}
				}
				for k, v := range prMap {
					if _, ok := hashChangeMap[k]; !ok {
						hashChangeMap[k] = v
					}
				}
				m.Unlock()
			}
			return nil
		})
	}

	if err := eg.Wait(); err == nil {
		return userHashPrMap, hashChangeMap, hashPrMap, prHashMap, nil
	}
	return nil, nil, nil, nil, nil
}

func (g *GhClient) ApprovePr(pr *github.PullRequest) error {
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
			if err := g.tryUpdateBranch(owner, repo, number, pr); err != nil {
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
	vars := map[string]interface{}{
		"pullId":      nodeID,
		"mergeMethod": "SQUASH",
	}
	payload := map[string]interface{}{
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
	reqGQL.Header.Set("Authorization", "Bearer "+g.t)
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
		Data   interface{}              `json:"data"`
		Errors []map[string]interface{} `json:"errors"`
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
	userHashPrMap, hashChangeMap, _, prHashMap, err := g.GetPrReviewRequested()
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
