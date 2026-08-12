package gh

import (
	"context"
	"sort"
	"time"

	"github.com/google/go-github/v72/github"
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

// PrVerifiedMap maps a PR identifier (HTML URL) to whether all its commits are verified (signed)
type PrVerifiedMap map[string]bool

// Occurrence is one place a hash's change block shows up: a specific spot in a
// specific file of a specific PR. The same hash usually has many occurrences —
// that is the whole point of hashing changes by content.
type Occurrence struct {
	PrURL string
	File  string
	// Raw holds the change block together with the surrounding context lines
	// from its hunk, so the GUI can render the diff in situ.
	Raw []string
}

// HashOccurrences maps a hash to every place it appears, sorted by file then PR.
type HashOccurrences map[string][]Occurrence

// ReviewSet is everything fetched for a review session. It is built once by
// GetPrReviewRequested and then read by the CLI and the GUI.
type ReviewSet struct {
	// UsersToHashes maps a PR author to the hashes contributed by their PRs.
	UsersToHashes GhPrHashMap
	// Changes maps a hash to its normalized change lines (the hashed content).
	Changes HashChangeMap
	// HashPRs maps a hash to the PRs containing it.
	HashPRs HashPrMap
	// PRHashes maps a PR URL to every hash it contains.
	PRHashes PrHashMap
	// Verified maps a PR URL to whether all of its commits are signed.
	Verified PrVerifiedMap
	// Occurrences maps a hash to every file/PR location it appears in.
	Occurrences HashOccurrences
	// Warnings collects per-PR failures that were skipped rather than aborting
	// the whole fetch.
	Warnings []string
}

func newReviewSet() *ReviewSet {
	return &ReviewSet{
		UsersToHashes: make(GhPrHashMap),
		Changes:       make(HashChangeMap),
		HashPRs:       make(HashPrMap),
		PRHashes:      make(PrHashMap),
		Verified:      make(PrVerifiedMap),
		Occurrences:   make(HashOccurrences),
	}
}

// Users returns the sorted list of PR authors in the set.
func (r *ReviewSet) Users() []string {
	users := make([]string, 0, len(r.UsersToHashes))
	for u := range r.UsersToHashes {
		users = append(users, u)
	}
	sort.Strings(users)
	return users
}

func (g *GhClient) getNotifications() ([]*github.Notification, error) {
	var allNotifications []*github.Notification
	opt := &github.NotificationListOptions{
		All:         true,
		Since:       time.Now().AddDate(0, 0, -3),
		ListOptions: github.ListOptions{PerPage: 50},
	}

	for page := 1; ; page++ {
		opt.Page = page
		notifications, resp, err := g.c.Activity.ListNotifications(context.Background(), opt)
		if err != nil {
			return nil, err
		}
		allNotifications = append(allNotifications, notifications...)
		if resp.NextPage == 0 {
			break
		}
	}
	return allNotifications, nil
}
