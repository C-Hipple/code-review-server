package jira

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
)

type JiraPullRequestIdentifier struct {
	URL    string `json:"url"`
	Status string `json:"status"` // Note this is the status in JIRA, not the github PR's status.
}

type DevDetails struct {
	PullRequests []JiraPullRequestIdentifier `json:"pullRequests"`
}

type DevStatusResponse struct {
	Detail []DevDetails `json:"detail"`
}

type JiraSearchResponse struct {
	Issues        []Issue `json:"issues"`
	NextPageToken string  `json:"nextPageToken"`
	IsLast        bool    `json:"isLast"`
}

type Issue struct {
	ID string `json:"id"`
}

func getDevURL(domain string, issueID string) string {
	return fmt.Sprintf("%s/rest/dev-status/1.0/issue/details?issueId=%s&applicationType=github&dataType=pullrequest", domain, issueID)
}

func getAuth() (string, string) {
	token := os.Getenv("JIRA_API_TOKEN")
	jiraEmail := os.Getenv("JIRA_API_EMAIL")
	return jiraEmail, token
}

// RepoRef identifies a GitHub repository by owner and short name. A project can
// span several repos, and two of them may share a short name under different
// owners, so PRs are matched and grouped on the pair rather than on the name.
type RepoRef struct {
	Owner string
	Repo  string
}

func (r RepoRef) FullName() string {
	return fmt.Sprintf("%s/%s", r.Owner, r.Repo)
}

// normalized lowercases the ref for comparison; GitHub owner and repo names are
// case-insensitive, and Jira reports whatever casing the PR was linked with.
func (r RepoRef) normalized() RepoRef {
	return RepoRef{Owner: strings.ToLower(r.Owner), Repo: strings.ToLower(r.Repo)}
}

// targetSet maps a normalized ref back to the ref as it was configured, so
// callers get keys they can hand straight to the GitHub API.
type targetSet map[RepoRef]RepoRef

func newTargetSet(refs []RepoRef) targetSet {
	targets := make(targetSet, len(refs))
	for _, ref := range refs {
		targets[ref.normalized()] = ref
	}
	return targets
}

func (t targetSet) lookup(ref RepoRef) (RepoRef, bool) {
	configured, ok := t[ref.normalized()]
	return configured, ok
}

// GetProjectPRKeys returns the PR numbers for every target repo under a JIRA epic,
// keyed by the repo they belong to.
//
// A non-nil error means the answer may be missing PRs: the epic search failed
// (the map is empty), or looking up the PR linked from some of its issues did.
// The map then holds what was found, and the caller can't tell a PR that left
// the epic from one whose lookup failed. An issue with no PR linked, or one
// linked to a repo outside repos, is not an error.
func GetProjectPRKeys(domain string, epicKey string, repos []RepoRef) (map[RepoRef][]int, error) {
	if !strings.HasSuffix(domain, "/") {
		domain += "/"
	}

	data, err := searchIssues(domain, fmt.Sprintf("Parent = %s", epicKey))
	if err != nil {
		return map[RepoRef][]int{}, fmt.Errorf("searching the issues of %s: %w", epicKey, err)
	}

	return processIssues(domain, data, repos)
}

// maxSearchPages caps how many pages of issues searchIssues follows, so a
// server that keeps handing back a next page can't hold a cycle forever.
const maxSearchPages = 50

// searchIssues runs a JQL search, following nextPageToken through every page.
func searchIssues(domain string, jql string) (JiraSearchResponse, error) {
	searchURL := fmt.Sprintf("%srest/api/3/search/jql", domain)
	jiraEmail, token := getAuth()
	client := &http.Client{}

	var all JiraSearchResponse
	pageToken := ""
	for page := 0; ; page++ {
		if page == maxSearchPages {
			return all, fmt.Errorf("more than %d pages of issues", maxSearchPages)
		}
		params := url.Values{}
		params.Add("jql", jql)
		params.Add("fields", "id")
		params.Add("maxResults", "100")
		if pageToken != "" {
			params.Add("nextPageToken", pageToken)
		}

		req, err := http.NewRequest("GET", searchURL, nil)
		if err != nil {
			return all, err
		}
		req.URL.RawQuery = params.Encode()
		req.SetBasicAuth(jiraEmail, token)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return all, err
		}
		var data JiraSearchResponse
		if resp.StatusCode != http.StatusOK {
			body := make([]byte, 1024)
			n, _ := resp.Body.Read(body)
			resp.Body.Close()
			return all, fmt.Errorf("search returned status %d: %s", resp.StatusCode, body[:n])
		}
		err = json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		if err != nil {
			return all, fmt.Errorf("decoding the search response: %w", err)
		}

		all.Issues = append(all.Issues, data.Issues...)
		if data.IsLast || data.NextPageToken == "" {
			return all, nil
		}
		pageToken = data.NextPageToken
	}
}

type issuePR struct {
	ref RepoRef
	num int
}

// parsePRURL pulls the repo and PR number out of a GitHub pull request URL of
// the form https://<host>/<owner>/<repo>/pull/<number>.
func parsePRURL(prURL string) (RepoRef, int, error) {
	parts := strings.Split(strings.TrimSuffix(prURL, "/"), "/")
	if len(parts) < 4 {
		return RepoRef{}, 0, fmt.Errorf("unexpected PR URL %q", prURL)
	}
	num, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return RepoRef{}, 0, fmt.Errorf("failed to convert PR number %q to int", parts[len(parts)-1])
	}
	return RepoRef{Owner: parts[len(parts)-4], Repo: parts[len(parts)-3]}, num, nil
}

func processIssues(domain string, data JiraSearchResponse, target_repos []RepoRef) (map[RepoRef][]int, error) {
	// Returns a map of repo -> list of PR numbers for that repo. PRs linked to
	// repos outside the target list are dropped, so a project spanning several
	// repos gets each repo's PRs grouped independently. The error joins every
	// issue whose linked PR couldn't be looked up.

	targets := newTargetSet(target_repos)

	var wg sync.WaitGroup

	errChan := make(chan error, len(data.Issues))
	resultsChan := make(chan issuePR, len(data.Issues))

	for _, issue := range data.Issues {
		wg.Add(1)
		go func(issue Issue) {
			defer wg.Done()

			pr, err := getPRLinkForIssue(domain, issue.ID)
			if err != nil {
				errChan <- fmt.Errorf("getting the PR linked from issue %s: %w", issue.ID, err)
				return
			}
			if pr == nil || pr.URL == "" {
				// No PR linked from this issue.
				return
			}

			ref, num, err := parsePRURL(pr.URL)
			if err != nil {
				errChan <- fmt.Errorf("issue %s: %w", issue.ID, err)
				return
			}
			// Match on owner and repo together: a project may span repos that
			// share a short name under different owners.
			configured, ok := targets.lookup(ref)
			if !ok {
				slog.Debug("Issue's PR is in a repo the project doesn't list", "issue", issue.ID, "repo", ref.FullName())
				return
			}
			resultsChan <- issuePR{ref: configured, num: num}
		}(issue)
	}

	wg.Wait()
	close(errChan)
	close(resultsChan)

	out := make(map[RepoRef][]int)
	for r := range resultsChan {
		out[r.ref] = append(out[r.ref], r.num)
	}
	var errs []error
	for err := range errChan {
		errs = append(errs, err)
	}
	return out, errors.Join(errs...)
}

func getPRLinkForIssue(domain string, issueID string) (*JiraPullRequestIdentifier, error) {
	/// Get first the PRs (Jira calls them dev-status) for an issue
	jiraEmail, token := getAuth()
	devURL := getDevURL(domain, issueID)

	req, err := http.NewRequest("GET", devURL, nil)
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(jiraEmail, token)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status code: %d", resp.StatusCode)
	}

	var data DevStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	if len(data.Detail) == 0 || len(data.Detail[0].PullRequests) == 0 {
		// fmt.Printf("No PR for issue: %s\n", issueID)
		return nil, nil // Indicate no PR found without an error
	}

	pr := data.Detail[0].PullRequests[0]
	// fmt.Printf("URL: %s\n", pr.URL)
	// fmt.Printf("Status: %s\n", pr.Status)

	return &pr, nil
}
