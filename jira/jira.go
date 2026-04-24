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

// GetProjectPRKeys returns the PR numbers for every target repo under a JIRA epic,
// keyed by the repo short name (e.g. "code-review-server").
func GetProjectPRKeys(domain string, epicKey string, repo_names []string) map[string][]int {
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
		return map[string][]int{}
	}
	req.URL.RawQuery = params.Encode()

	req.SetBasicAuth(jiraEmail, token)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("Error making request", "error", err)
		return map[string][]int{}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("Error getting JIRA search")
		// Read the response body to get the error message.
		body := make([]byte, 1024)
		n, _ := resp.Body.Read(body) // We ignore the error here.
		slog.Error("JIRA response body", "content", string(body[:n]))
		return map[string][]int{}
	}

	var data JiraSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		slog.Error("Error decoding JSON", "error", err)
		return map[string][]int{}
	}

	return processIssues(domain, data, repo_names)

}

type issuePR struct {
	repo string
	num  int
}

func processIssues(domain string, data JiraSearchResponse, target_repos []string) map[string][]int {
	// Returns a map of repo short name -> list of PR numbers for that repo.

	targets := make(map[string]struct{}, len(target_repos))
	for _, r := range target_repos {
		targets[r] = struct{}{}
	}

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

			urlParts := strings.Split(pr.URL, "/")
			repo := urlParts[len(urlParts)-3]
			if _, ok := targets[repo]; !ok {
				errChan <- fmt.Errorf("Issue PR is for a separate repo: %s", repo)
				return
			}
			prNum := urlParts[len(urlParts)-1]
			num, err := strconv.Atoi(prNum)
			if err != nil {
				errChan <- fmt.Errorf("Failed to convert prNum %s to int", prNum)
				return
			}
			resultsChan <- issuePR{repo: repo, num: num}
		}(issue)
	}

	wg.Wait()
	close(errChan)
	close(resultsChan)

	out := make(map[string][]int)
	for r := range resultsChan {
		out[r.repo] = append(out[r.repo], r.num)
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
