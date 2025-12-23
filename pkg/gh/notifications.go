package gh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/mallendem/gh-pr-review/pkg/utils"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v72/github"
	"golang.org/x/sync/errgroup"
)

const CONCURRENCY_LIMIT = 10

// GhPrHashMap maps GitHub usernames to a map of hash strings to slices of Pull Requests
type GhPrHashMap map[string]map[string][]*github.PullRequest

// HashChangeMap maps hash strings to slices of change lines
type HashChangeMap map[string][]string

// HashPrMap maps hash strings to slices of Pull Requests
type HashPrMap map[string][]*github.PullRequest

// PrHashMap maps a PR identifier (HTML URL) to hashes associated with that PR
type PrHashMap map[string][]string

func (g *GhClient) getNotifications() ([]*github.Notification, error) {
	var allNotifications []*github.Notification
	opt := &github.NotificationListOptions{
		All:         true,
		ListOptions: github.ListOptions{PerPage: 50},
	}

	totalFetched := 0
	page := 1

	for {
		opt.Page = page

		// 3 days ago
		opt.Since = time.Now().AddDate(0, 0, -3)
		notifications, resp, err := g.c.Activity.ListNotifications(context.Background(), opt)
		if err != nil {
			return nil, err
		}

		allNotifications = append(allNotifications, notifications...)
		totalFetched += len(notifications)

		if resp.NextPage == 0 {
			break
		}
		page++
	}
	return allNotifications, nil
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
	updateURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/update-branch", owner, repo, number)
	req, err := http.NewRequest("POST", updateURL, nil)
	if err != nil {
		// Non-fatal: return error
		return fmt.Errorf("failed to create update-branch request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	// This endpoint requires authentication
	req.Header.Set("Authorization", "Bearer "+g.t)
	resp, err := g.c.Client().Do(req)
	if err != nil {
		// Log and continue to approval step
		fmt.Printf("warning: update-branch request failed for %s: %v\n", pr.GetHTMLURL(), err)
	} else {
		// drain and close
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		// we can have a valid 200OK response witha 404 Not Found status in the body
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			fmt.Printf("updated branch for PR %s (status %d)\n", pr.GetHTMLURL(), resp.StatusCode)
		} else {
			fmt.Printf("warning: update-branch returned status %d for PR %s\n", resp.StatusCode, pr.GetHTMLURL())
		}
	}

	// 2) Approve the PR (create a review with APPROVE)
	approveEvent := "APPROVE"
	review := &github.PullRequestReviewRequest{
		Event: &approveEvent,
	}
	_, _, revErr := g.c.PullRequests.CreateReview(context.Background(), owner, repo, number, review)
	if revErr != nil {
		fmt.Printf("warning: failed to create approval review for PR %s: %v\n", pr.GetHTMLURL(), revErr)
	} else {
		fmt.Printf("created approval review for PR %s\n", pr.GetHTMLURL())
	}

	// 3) Enable auto-merge for the PR using GraphQL mutation
	// Use the enablePullRequestAutoMerge mutation (requires PR node ID)
	nodeID := pr.GetNodeID()
	if nodeID == "" {
		fmt.Printf("warning: PR %s missing node ID; cannot enable auto-merge\n", pr.GetHTMLURL())
	} else {
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
			fmt.Printf("warning: failed to create GraphQL request for PR %s: %v\n", pr.GetHTMLURL(), err)
		} else {
			reqGQL.Header.Set("Accept", "application/vnd.github+json")
			reqGQL.Header.Set("Content-Type", "application/json")
			reqGQL.Header.Set("Authorization", "Bearer "+g.t)
			respGQL, err := g.c.Client().Do(reqGQL)
			if err != nil {
				fmt.Printf("warning: GraphQL request failed for PR %s: %v\n", pr.GetHTMLURL(), err)
			} else {
				defer func() { _ = respGQL.Body.Close() }()
				body, _ := io.ReadAll(respGQL.Body)
				if respGQL.StatusCode < 200 || respGQL.StatusCode > 299 {
					fmt.Printf("warning: GraphQL enablePullRequestAutoMerge returned status %d for PR %s: %s\n", respGQL.StatusCode, pr.GetHTMLURL(), string(body))
				} else {
					// inspect response for GraphQL errors
					var gqlResp struct {
						Data   interface{}              `json:"data"`
						Errors []map[string]interface{} `json:"errors"`
					}
					if err := json.Unmarshal(body, &gqlResp); err != nil {
						fmt.Printf("warning: failed to decode GraphQL response for PR %s: %v\n", pr.GetHTMLURL(), err)
					} else if len(gqlResp.Errors) > 0 {
						fmt.Printf("warning: GraphQL returned errors for PR %s: %v\n", pr.GetHTMLURL(), gqlResp.Errors)
					} else {
						fmt.Printf("enabled auto-merge (GraphQL) for PR %s\n", pr.GetHTMLURL())
					}
				}
			}
		}
	}
	//
	//// After enabling auto-merge (or attempting to), attempt to merge the PR immediately via REST.
	//// This uses the "Merge a pull request" endpoint: PUT /repos/{owner}/{repo}/pulls/{pull_number}/merge
	//mergeURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/merge", owner, repo, number)
	//mergePayload := strings.NewReader(`{"commit_title":"","commit_message":"Merged by pr-approver","merge_method":"merge"}`)
	//mergeReq, err := http.NewRequest("PUT", mergeURL, mergePayload)
	//if err != nil {
	//	fmt.Printf("warning: failed to create merge request for PR %s: %v\n", pr.GetHTMLURL(), err)
	//	return nil
	//}
	//mergeReq.Header.Set("Accept", "application/vnd.github+json")
	//mergeReq.Header.Set("Content-Type", "application/json")
	//mergeReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	//// ensure authentication header is present
	//mergeReq.Header.Set("Authorization", "Bearer "+g.t)
	//
	//mergeResp, err := g.c.Client().Do(mergeReq)
	//if err != nil {
	//	fmt.Printf("warning: merge request failed for PR %s: %v\n", pr.GetHTMLURL(), err)
	//	return nil
	//}
	//defer func() { _ = mergeResp.Body.Close() }()
	//mergeBody, _ := io.ReadAll(mergeResp.Body)
	//if mergeResp.StatusCode >= 200 && mergeResp.StatusCode <= 299 {
	//	fmt.Printf("merged PR %s (status %d): %s\n", pr.GetHTMLURL(), mergeResp.StatusCode, string(mergeBody))
	//} else {
	//	fmt.Printf("warning: merge returned status %d for PR %s: %s\n", mergeResp.StatusCode, pr.GetHTMLURL(), string(mergeBody))
	//}

	return nil
}
