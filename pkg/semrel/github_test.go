package semrel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"

	"github.com/Masterminds/semver"
	"github.com/google/go-github/v30/github"
	"github.com/stretchr/testify/require"
)

func TestNewGithubRepository(t *testing.T) {
	require := require.New(t)

	repo, err := NewGitHubRepository(context.TODO(), "", "", "")
	require.Nil(repo)
	require.EqualError(err, "invalid slug")

	repo, err = NewGitHubRepository(context.TODO(), "", "owner/test-repo", "token")
	require.NotNil(repo)
	require.NoError(err)
	require.Equal("owner", repo.Owner())
	require.Equal("test-repo", repo.Repo())

	repo, err = NewGitHubRepository(context.TODO(), "github.enterprise", "owner/test-repo", "token")
	require.NotNil(repo)
	require.NoError(err)
	require.Equal("github.enterprise", repo.Client.BaseURL.Host)
}

func createGithubCommit(sha, message string) *github.RepositoryCommit {
	return &github.RepositoryCommit{SHA: &sha, Commit: &github.Commit{Message: &message}}
}

var commitType = "commit"

func createGithubRef(ref, sha string) *github.Reference {
	return &github.Reference{Ref: &ref, Object: &github.GitObject{SHA: &sha, Type: &commitType}}
}

var (
	GITHUB_REPO_PRIVATE  = true
	GITHUB_DEFAULTBRANCH = "master"
	GITHUB_REPO          = github.Repository{DefaultBranch: &GITHUB_DEFAULTBRANCH, Private: &GITHUB_REPO_PRIVATE}
	GITHUB_COMMITS       = []*github.RepositoryCommit{
		createGithubCommit("abcd", "feat(app): new feature"),
		createGithubCommit("dcba", "Fix: bug"),
		createGithubCommit("cdba", "Initial commit"),
		createGithubCommit("efcd", "chore: break\nBREAKING CHANGE: breaks everything"),
	}
	GITHUB_TAGS = []*github.Reference{
		createGithubRef("refs/tags/test-tag", "deadbeef"),
		createGithubRef("refs/tags/v1.0.0", "deadbeef"),
		createGithubRef("refs/tags/v2.0.0", "deadbeef"),
		createGithubRef("refs/tags/v2.1.0-beta", "deadbeef"),
		createGithubRef("refs/tags/v3.0.0-beta.2", "deadbeef"),
		createGithubRef("refs/tags/v3.0.0-beta.1", "deadbeef"),
		createGithubRef("refs/tags/2020.04.19", "deadbeef"),
	}
)

//nolint:errcheck
func githubHandler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer token" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/repos/owner/test-repo" {
		json.NewEncoder(w).Encode(GITHUB_REPO)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/repos/owner/test-repo/commits" {
		json.NewEncoder(w).Encode(GITHUB_COMMITS)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/repos/owner/test-repo/git/refs/tags" {
		json.NewEncoder(w).Encode(GITHUB_TAGS)
		return
	}
	if r.Method == "POST" && r.URL.Path == "/repos/owner/test-repo/git/refs" {
		var data map[string]string
		json.NewDecoder(r.Body).Decode(&data)
		r.Body.Close()
		if data["sha"] != "deadbeef" || data["ref"] != "refs/tags/v2.0.0" {
			http.Error(w, "invalid sha or ref", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, "{}")
		return
	}
	if r.Method == "POST" && r.URL.Path == "/repos/owner/test-repo/releases" {
		var data map[string]string
		json.NewDecoder(r.Body).Decode(&data)
		r.Body.Close()
		if data["tag_name"] != "v2.0.0" {
			http.Error(w, "invalid tag name", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, "{}")
		return
	}
	http.Error(w, "invalid route", http.StatusNotImplemented)
}

func getNewGithubTestRepo(t *testing.T) (*GitHubRepository, *httptest.Server) {
	repo, err := NewGitHubRepository(context.TODO(), "", "owner/test-repo", "token")
	require.NoError(t, err)
	ts := httptest.NewServer(http.HandlerFunc(githubHandler))
	repo.Client.BaseURL, _ = url.Parse(ts.URL + "/")
	return repo, ts
}

func TestGithubGetInfo(t *testing.T) {
	repo, ts := getNewGithubTestRepo(t)
	defer ts.Close()
	defaultBranch, isPrivate, err := repo.GetInfo()
	require.NoError(t, err)
	require.Equal(t, GITLAB_DEFAULTBRANCH, defaultBranch)
	require.True(t, isPrivate)
}

func TestGithubGetCommits(t *testing.T) {
	repo, ts := getNewGithubTestRepo(t)
	defer ts.Close()
	commits, err := repo.GetCommits("")
	require.NoError(t, err)
	require.Len(t, commits, 4)

	if !compareCommit(commits[0], "feat", "app", Change{false, true, false}) ||
		!compareCommit(commits[1], "fix", "", Change{false, false, true}) ||
		!compareCommit(commits[2], "", "", Change{false, false, false}) ||
		!compareCommit(commits[3], "chore", "", Change{true, false, false}) {
		t.Fatal("invalid commits")
	}
}

func TestGithubGetLatestRelease(t *testing.T) {
	repo, ts := getNewGithubTestRepo(t)
	defer ts.Close()

	testCases := []struct {
		vrange          string
		re              *regexp.Regexp
		expectedSHA     string
		expectedVersion string
	}{
		{"", nil, "deadbeef", "2020.4.19"},
		{"", regexp.MustCompile("^v[0-9]*"), "deadbeef", "2.0.0"},
		{"2-beta", nil, "deadbeef", "2.1.0-beta"},
		{"3-beta", nil, "deadbeef", "3.0.0-beta.2"},
		{"4-beta", nil, "deadbeef", "4.0.0-beta"},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("VersionRange: %s, RE: %s", tc.vrange, tc.re), func(t *testing.T) {
			release, err := repo.GetLatestRelease(tc.vrange, tc.re)
			require.NoError(t, err)
			require.Equal(t, tc.expectedSHA, release.SHA)
			require.Equal(t, tc.expectedVersion, release.Version.String())
		})
	}
}

func TestGithubCreateRelease(t *testing.T) {
	repo, ts := getNewGithubTestRepo(t)
	defer ts.Close()
	newVersion := semver.MustParse("2.0.0")
	err := repo.CreateRelease("", newVersion, false, "", "deadbeef")
	require.NoError(t, err)
}
