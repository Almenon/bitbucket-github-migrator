package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v72/github"
	"github.com/joho/godotenv"
	"github.com/ktrysmt/go-bitbucket"
)

type settings struct {
	bbWorkspace         string
	bbUsername          string
	bbPassword          string
	revokeOldPerms      bool
	cloneVia            string
	ghOrg               string
	ghUser              string
	ghOwner             string
	ghToken             string
	dryRun              bool
	overwrite           bool
	visibility          string
	runProgram          string
	repoFile            string
	migrateRepoContents bool
	migrateRepoSettings bool
	migrateOpenPrs      bool
	migrateClosedPrs    bool
}

func main() {
	err := godotenv.Load(".env")
	if err != nil {
		log.Fatalf("Error loading .env file")
	}

	config := settings{
		bbWorkspace:         os.Getenv("BITBUCKET_WORKSPACE"),
		bbUsername:          os.Getenv("BITBUCKET_USER"),
		bbPassword:          os.Getenv("BITBUCKET_TOKEN"),
		revokeOldPerms:      getEnvVarAsBool("BITBUCKET_REVOKEOLDPERMS"),
		cloneVia:            os.Getenv("CLONE_VIA"),
		ghUser:              os.Getenv("GITHUB_USER"),
		ghOrg:               os.Getenv("GITHUB_ORG"),
		ghOwner:             "",
		ghToken:             os.Getenv("GITHUB_TOKEN"),
		dryRun:              getEnvVarAsBool("GITHUB_DRYRUN"),
		overwrite:           getEnvVarAsBool("GITHUB_OVERWRITE"),
		visibility:          getEnvOrDefault("GITHUB_PRIVATE_VISIBILITY", "internal"),
		runProgram:          getEnvOrDefault("GITHUB_RUN_PROGRAM", "noop"),
		repoFile:            os.Getenv("REPO_FILE"),
		migrateRepoContents: getEnvVarAsBool("MIGRATE_REPO_CONTENTS"),
		migrateRepoSettings: getEnvVarAsBool("MIGRATE_REPO_SETTINGS"),
		migrateOpenPrs:      getEnvVarAsBool("MIGRATE_OPEN_PRS"),
		migrateClosedPrs:    getEnvVarAsBool("MIGRATE_CLOSED_PRS"),
	}

	if config.bbWorkspace == "" || config.bbUsername == "" || config.bbPassword == "" {
		fmt.Println("BITBUCKET_WORKSPACE or BITBUCKET_USER or BITBUCKET_TOKEN not set in .env file or env vars")
		os.Exit(2)
	}

	if config.ghToken == "" {
		fmt.Println("GITHUB_TOKEN not set in .env file or env vars")
		os.Exit(2)
	}

	if (config.ghUser == "" && config.ghOrg == "") || (config.ghUser != "" && config.ghOrg != "") {
		fmt.Println("You must set either org or user but not both")
		os.Exit(2)
	}

	config.ghOwner = strings.Join([]string{config.ghOrg, config.ghUser}, "")

	repos := parseRepos(config.repoFile)

	bitbucketClient := bitbucket.NewBasicAuth(config.bbUsername, config.bbPassword)
	githubClient := github.NewClient(nil).WithAuthToken(config.ghToken)

	migrateRepos(githubClient, bitbucketClient, repos, config)
}

// returns defaultVal if envVar is not present or empty
func getEnvOrDefault(envVar string, defaultVal string) string {
	result := os.Getenv(envVar)
	if result == "" {
		return defaultVal
	} else {
		return result
	}
}

func getEnvVarAsBool(envVar string) bool {
	result, err := strconv.ParseBool(os.Getenv(envVar))
	if err != nil {
		fmt.Println("could not parse bool env var ", envVar)
		os.Exit(2)
	}
	return result
}

func parseRepos(repoFile string) []string {
	var repos []string
	if repoFile == "" {
		fmt.Println("You must supply a list of names of repos to migrate in REPO_FILE")
		os.Exit(2)
	}
	data, err := os.ReadFile(strings.TrimSpace(repoFile))
	if err != nil {
		log.Fatalf("could not read file %s", repoFile)
	}
	repos = strings.Split(string(data), "\n")

	cleaned_repos := []string{}
	for _, repo := range repos {
		repo = strings.TrimSpace(repo)
		if repo != "" {
			// ignore commented out repos
			if repo[0] == "#"[0] {
				continue
			}
			// bitbucket replaces invalid chars with -
			// see https://support.atlassian.com/bitbucket-cloud/kb/what-is-a-repository-slug/
			repo = strings.ReplaceAll(repo, " ", "-")
			repo = strings.ReplaceAll(repo, "/", "-")
			repo = strings.ReplaceAll(repo, "+", "-")
			repo = strings.ReplaceAll(repo, "&", "-")
			repo = strings.ReplaceAll(repo, "(", "-")
			repo = strings.ReplaceAll(repo, ")", "-")
			// - is not allowed at start or end of string
			repo = strings.Trim(repo, "-")
			cleaned_repos = append(cleaned_repos, repo)
		}
	}
	return cleaned_repos
}

func migrateRepos(gh *github.Client, bb *bitbucket.Client, repoList []string, config settings) {
	if config.dryRun {
		fmt.Println("Dry Run - not actually migrating anything")
	}

	for _, repo := range repoList {
		migrateRepo(gh, bb, repo, config)
	}
}

func migrateRepo(gh *github.Client, bb *bitbucket.Client, repoName string, config settings) {
	fmt.Println("Getting bitbucket settings for", repoName)
	bbRepo := getRepo(bb, config.bbWorkspace, repoName)

	if config.revokeOldPerms {
		fmt.Println("revoking old bitbucket permissions to prevent accidental writes")
		updatePermissionsToReadOnly(bb, config.bbWorkspace, repoName, config.dryRun)
	} else {
		fmt.Println("skipping revoking old bitbucket permissions")
	}

	var repoFolder string
	if config.migrateRepoContents {
		repoFolder = cloneRepo(repoName, config)
	}
	var prs *PullRequests
	if config.migrateOpenPrs || config.migrateClosedPrs {
		prs = getPrs(bb, config.bbWorkspace, repoName, bbRepo.Mainbranch.Name)
	}

	fmt.Println("Migrating to Github")
	ghRepo := createRepo(gh, bbRepo, config)
	if config.migrateRepoContents {
		pushRepoToGithub(repoFolder, repoName, config)
	} else {
		fmt.Println("Skipping repo contents")
	}
	if config.migrateRepoSettings {
		updateRepo(gh, config.ghOwner, ghRepo, config.dryRun)
		updateRepoTopics(gh, config.ghOwner, ghRepo, config.dryRun)
		updateCustomProperties(gh, config.ghOwner, ghRepo, config.dryRun, bbRepo.Project.Name)
	} else {
		fmt.Println("Skipping repo settings")
	}
	if config.migrateOpenPrs {
		migrateOpenPrs(gh, config.ghOwner, ghRepo, prs, config.dryRun)
	} else {
		fmt.Println("Skipping open PR's")
	}
	if config.migrateClosedPrs {
		createClosedPrs(gh, config.ghOwner, ghRepo, prs, config.dryRun)
	} else {
		fmt.Println("Skipping closed PR's")
	}
	fmt.Println("done migrating repo")
	fmt.Print("-----------------------\n\n")

	// sleep for .5s to help avoid github rate limit
	time.Sleep(time.Millisecond * 500)
}
