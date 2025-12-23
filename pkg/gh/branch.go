package gh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/google/go-github/v72/github"
)

// tryUpdateBranch tries to rebase the branch for the given PR.
func (g *GhClient) tryUpdateBranch(owner, repo string, number int, pr *github.PullRequest) error {
	_, _, err := g.c.PullRequests.UpdateBranch(
		context.Background(),
		owner,
		repo,
		number,
		&github.PullRequestBranchUpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update branch for PR #%d in %s/%s: %w", number, owner, repo, err)
	}
	return nil
}

// isBranchBehind checks whether headRef is behind baseRef using the GitHub compare API.
// It returns true if the head is behind the base (i.e., the branch is out-of-date and
// should be updated/rebased).
func (g *GhClient) isBranchBehind(owner, repo, baseRef, headRef string) (bool, error) {
	if owner == "" || repo == "" || baseRef == "" || headRef == "" {
		return false, fmt.Errorf("invalid parameters to isBranchBehind")
	}

	// Use the REST compare endpoint to avoid dependency on go-github method signatures.
	compareURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/compare/%s...%s", owner, repo, baseRef, headRef)
	req, err := http.NewRequest("GET", compareURL, nil)
	if err != nil {
		return false, fmt.Errorf("failed to build compare request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+g.t)

	resp, err := g.c.Client().Do(req)
	if err != nil {
		return false, fmt.Errorf("compare request failed for %s/%s %s...%s: %w", owner, repo, baseRef, headRef, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false, fmt.Errorf("compare returned status %d for %s/%s: %s", resp.StatusCode, owner, repo, string(body))
	}
	var cmp struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &cmp); err != nil {
		return false, fmt.Errorf("failed to decode compare response for %s/%s: %w", owner, repo, err)
	}
	// status values are: "identical", "ahead", "behind", "diverged"
	return cmp.Status == "behind", nil
}
