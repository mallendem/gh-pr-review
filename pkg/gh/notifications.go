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
