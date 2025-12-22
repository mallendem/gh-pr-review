package gh

import (
	"context"
	"os"

	"github.com/google/go-github/v72/github"
	"golang.org/x/oauth2"
)

type GhClient struct {
	c *github.Client
	t string
}

func NewGhClient() *GhClient {
	ghToken := ""
	if ghToken = os.Getenv("GITHUB_TOKEN"); ghToken == "" {
		panic("GITHUB_TOKEN environment variable is not set")
	}

	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: ghToken},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	return &GhClient{
		c: client,
		t: ghToken,
	}
}
