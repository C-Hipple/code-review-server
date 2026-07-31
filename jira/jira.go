package jira

import (
	"encoding/json"
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
	Issues []Issue `json:"issues"`
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
func GetProjectPRKeys(domain string, epicKey string, repos []RepoRef) map[RepoRef][]int {
	if !strings.HasSuffix(domain, "/") {
		domain += "/"
	}

	searchURL := fmt.Sprintf("%srest/api/3/search/jql", domain)

	jiraEmail, token := getAuth()

	params := url.Values{}
	params.Add("jql", fmt.Sprintf("Parent = %s", epicKey))

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		slog.Error("Error creating request", "error", err)
		return map[RepoRef][]int{}
	}
	req.URL.RawQuery = params.Encode()

	req.SetBasicAuth(jiraEmail, token)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("Error making request", "error", err)
		return map[RepoRef][]int{}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("Error getting JIRA search")
		// Read the response body to get the error message.
		body := make([]byte, 1024)
		n, _ := resp.Body.Read(body) // We ignore the error here.
		slog.Error("JIRA response body", "content", string(body[:n]))
		return map[RepoRef][]int{}
	}

	var data JiraSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		slog.Error("Error decoding JSON", "error", err)
		return map[RepoRef][]int{}
	}

	return processIssues(domain, data, repos)

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

func processIssues(domain string, data JiraSearchResponse, target_repos []RepoRef) map[RepoRef][]int {
	// Returns a map of repo -> list of PR numbers for that repo. PRs linked to
	// repos outside the target list are dropped, so a project spanning several
	// repos gets each repo's PRs grouped independently.

	targets := newTargetSet(target_repos)

	var wg sync.WaitGroup

	errChan := make(chan error, len(data.Issues))
	resultsChan := make(chan issuePR, len(data.Issues))

	for _, issue := range data.Issues {
		wg.Add(1)
		go func(issue Issue) {
			defer wg.Done()

			pr, err := getPRLinkForIssue(domain, issue.ID)
			if pr == nil {
				errChan <- fmt.Errorf("pr is nil %s", issue.ID)
				return
			}

			if err != nil {
				errChan <- fmt.Errorf("error getting PR link for issue %s: %w", issue.ID, err)
				return
			}

			if pr.URL == "" {
				errChan <- fmt.Errorf("Err getting PR link for issue %s: URL is empty", issue.ID)
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
				errChan <- fmt.Errorf("Issue PR is for a separate repo: %s", ref.FullName())
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
	return out
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
