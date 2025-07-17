package main

import (
	"cmp"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/ktrysmt/go-bitbucket"
	"github.com/mitchellh/mapstructure"
)

func getRepo(bb *bitbucket.Client, owner string, repoName string) *bitbucket.Repository {
	ro := &bitbucket.RepositoryOptions{
		Owner:    owner,
		RepoSlug: repoName,
	}
	repo, err := bb.Repositories.Repository.Get(ro)
	if err != nil {
		log.Fatalf("Failed to get repo from bitbucket: %v", err)
	}
	return repo
}

// clones repo to a temp folder
func cloneRepo(repo string, config settings) (tempfolderpath string) {
	tempDir, err := os.MkdirTemp("", fmt.Sprintf("%s-%s-*", config.bbWorkspace, repo))
	if err != nil {
		log.Fatalf("Failed to create temp directory: %s", err)
	}

	var cloneURL string
	if strings.ToLower(config.cloneVia) == "ssh" {
		cloneURL = fmt.Sprintf("git@bitbucket.org:%s/%s.git", config.bbWorkspace, repo)
	} else {
		cloneURL = fmt.Sprintf("https://bitbucket.org/%s/%s.git", config.bbWorkspace, repo)
	}
	fmt.Printf("Cloning repository %s to %s\n", repo, tempDir)

	cmd := exec.Command("git", "clone", "--mirror", cloneURL, tempDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("Failed to clone repository: %s\nOutput: %s", err, string(output))
	}
	fmt.Println(string(output))

	return tempDir
}

func updatePermissionsToReadOnly(bb *bitbucket.Client, owner string, repoName string, dryRun bool) {
	// number is arbitrary, just want to be nice to their API
	const apiWaitTime = time.Millisecond * 16

	ro := &bitbucket.RepositoryOptions{
		Owner:    owner,
		RepoSlug: repoName,
	}
	user_perms, err := bb.Repositories.Repository.ListUserPermissions(ro)
	if err != nil {
		log.Fatalf("Failed to get user permissions: %v", err)
	}
	group_perms, err := bb.Repositories.Repository.ListGroupPermissions(ro)
	if err != nil {
		log.Fatalf("Failed to get group permissions: %v", err)
	}

	if dryRun {
		return
	}

	for _, userPerm := range user_perms.UserPermissions {
		user := userPerm.User
		permOpts := &bitbucket.RepositoryUserPermissionsOptions{
			Owner:      owner,
			RepoSlug:   repoName,
			User:       user.AccountId,
			Permission: "read",
		}
		_, err := bb.Repositories.Repository.SetUserPermissions(permOpts)
		if err != nil {
			log.Fatalf("Failed to update user permission for %s: %v", user.Username, err)
		}
		time.Sleep(apiWaitTime)
	}

	for _, groupPerm := range group_perms.GroupPermissions {
		groupSlug := groupPerm.Group.Slug
		permOpts := &bitbucket.RepositoryGroupPermissionsOptions{
			Owner:      owner,
			RepoSlug:   repoName,
			Group:      groupSlug,
			Permission: "read",
		}
		_, err := bb.Repositories.Repository.SetGroupPermissions(permOpts)
		if err != nil {
			log.Fatalf("Failed to update group permission for %s: %v", groupSlug, err)
		}
		time.Sleep(apiWaitTime)
	}
}

func getPrs(bb *bitbucket.Client, owner string, repo string, destinationBranch string) *PullRequests {
	opt := &bitbucket.PullRequestsOptions{
		Owner:             owner,
		RepoSlug:          repo,
		DestinationBranch: destinationBranch,
		Query:             "state IN (\"MERGED\", \"OPEN\")",
	}
	fmt.Println("getting prs for", repo)
	response, err := bb.Repositories.PullRequests.Gets(opt)
	if err != nil {
		log.Fatalf("Failed to get PRs: %v", err)
	}
	prs, err := decodePullRequests(response)
	if err != nil {
		log.Fatalf("Error decoding PRs: %v", err)
	}
	slices.SortFunc(prs.Values, func(i PullRequest, j PullRequest) int {
		return cmp.Compare(i.ID, j.ID)
	})
	return prs
}

/////////////////////////////////

var stringToTimeHookFunc = mapstructure.StringToTimeHookFunc("2006-01-02T15:04:05.000000+00:00")

func DecodeError(e map[string]interface{}) error {
	var bitbucketError bitbucket.BitbucketError
	err := mapstructure.Decode(e["error"], &bitbucketError)
	if err != nil {
		return err
	}

	return errors.New(bitbucketError.Message)
}

type PullRequests struct {
	Size     int
	Page     int
	Pagelen  int
	Next     string
	Previous string
	Values   []PullRequest
}

type PullRequest struct {
	c *bitbucket.Client

	Type              string
	ID                int
	Title             string
	Rendered          PRRendered
	Summary           PRSummary
	State             string
	Author            map[string]any
	Source            map[string]any
	Destination       map[string]any
	MergeCommit       PRMergeCommit  `mapstructure:"merge_commit"`
	CommentCount      int            `mapstructure:"comment_count"`
	TaskCount         int            `mapstructure:"task_count"`
	CloseSourceBranch bool           `mapstructure:"close_source_branch"`
	ClosedBy          map[string]any `mapstructure:"closed_by"`
	Reason            string
	CreatedOn         time.Time `mapstructure:"created_on"`
	UpdatedOn         time.Time `mapstructure:"updated_on"`
	Reviewers         []map[string]any
	Participants      []map[string]any
	Draft             bool
	Queued            bool
}

type PRRendered struct {
	Title       PRText
	Description PRText
	Reason      PRText
}

type PRText struct {
	Raw    string
	Markup string
	HTML   string
}

type PRSummary struct {
	Raw    string
	Markup string
	HTML   string
}

type PRMergeCommit struct {
	Hash string
}

func decodePullRequests(prsResponse interface{}) (*PullRequests, error) {
	prResponseMap, ok := prsResponse.(map[string]interface{})
	if !ok {
		return nil, errors.New("not a valid format")
	}

	prArray := prResponseMap["values"].([]interface{})
	var prs []PullRequest
	for _, prEntry := range prArray {
		pr, err := decodePullRequest(prEntry)
		if err == nil {
			prs = append(prs, *pr)
		} else {
			return nil, err
		}
	}

	page, ok := prResponseMap["page"].(float64)
	if !ok {
		page = 0
	}

	pagelen, ok := prResponseMap["pagelen"].(float64)
	if !ok {
		pagelen = 0
	}
	size, ok := prResponseMap["size"].(float64)
	if !ok {
		size = 0
	}

	pullRequests := PullRequests{
		Page:    int(page),
		Pagelen: int(pagelen),
		Size:    int(size),
		Values:  prs,
	}
	return &pullRequests, nil
}

func decodePullRequest(response interface{}) (*PullRequest, error) {
	prMap := response.(map[string]interface{})

	if prMap["type"] == "error" {
		return nil, DecodeError(prMap)
	}

	var pr = new(PullRequest)
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Metadata:   nil,
		Result:     pr,
		DecodeHook: stringToTimeHookFunc,
	})
	if err != nil {
		return nil, err
	}
	err = decoder.Decode(prMap)
	if err != nil {
		return nil, err
	}

	return pr, nil
}
