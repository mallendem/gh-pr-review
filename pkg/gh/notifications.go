package gh

import (
	"context"
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

// HashFileMap maps hash strings to the filename the hunk belongs to
type HashFileMap map[string]string

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
