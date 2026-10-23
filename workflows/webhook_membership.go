package workflows

import (
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/go-github/v74/github"
)

// A delivery can change which sections a PR belongs in, not only what its rows
// say: a review I submit clears my review request, which takes the PR out of
// "Review Requested"; a reply to my thread puts it into "Waiting on me". A
// cycle would catch up when it next lists PRs, but with webhooks on there is
// no need to wait: after a delivery is applied, the PR is checked against each
// workflow again, from the caches the delivery just updated, and its section
// rows are added, updated or dropped to match.
//
// That check runs only while webhooks are trusted for the PR (webhookHandles)
// and its caches hold its current head (cacheServes), which is what lets the
// filters and the row's details be worked out without asking GitHub. It waits
// for any cycle in progress to finish writing sections first (membershipMu),
// so a cycle working from a listing older than the delivery can't undo it.

// membershipMu is held by a cycle from the start of its run until its section
// changes are written and pruned, and by each round of re-evaluation.
var membershipMu sync.Mutex

// membership is a workflow's verdict on whether a PR belongs in its section.
type membership int

const (
	// memberUnknown: the PR alone can't tell — a Jira epic, or a team lookup
	// that failed. The section is left as it is.
	memberUnknown membership = iota
	memberYes
	memberNo
)

// webhookMember is a workflow that can judge, from a PR alone, whether the PR
// belongs in its section — the way its Run would if its listing turned up just
// that PR.
type webhookMember interface {
	Workflow
	webhookMembership(pr *github.PullRequest) membership
	includesDiff() bool
	ShouldNotify() bool
}

func (w SyncReviewRequestsWorkflow) webhookMembership(pr *github.PullRequest) membership {
	repo := pr.GetBase().GetRepo()
	for _, req := range w.GetPRRequirements() {
		if strings.EqualFold(req.Owner, repo.GetOwner().GetLogin()) && strings.EqualFold(req.Repo, repo.GetName()) &&
			prStateMatches(req.State, pr) {
			return filtersMembership(pr, w.Filters)
		}
	}
	// Not a repository and state this workflow lists.
	return memberNo
}

func (w SyncReviewRequestsWorkflow) includesDiff() bool { return w.IncludeDiff }

func (w SearchWorkflow) webhookMembership(pr *github.PullRequest) membership {
	login := strings.TrimSpace(w.Login)
	if login == "" {
		return memberUnknown
	}
	// Both searches are for open PRs.
	if pr.GetState() != "open" {
		return memberNo
	}
	switch w.Mode {
	case ModeReviewRequested:
		// The search is what admits a PR here; ask what it asks.
		if verdict := reviewRequestedOf(pr, login); verdict != memberYes {
			return verdict
		}
	case ModeWaitingOnMe:
		// Its filter decides on its own: every PR it lets through is one
		// the searches would have found (see candidateRefs).
	default:
		return memberUnknown
	}
	return filtersMembership(pr, w.filters(login))
}

func (w SearchWorkflow) includesDiff() bool { return w.IncludeDiff }

// A project list's PRs are the ones its Jira epic links, which a PR can't
// say; only its filters can be applied again, to drop one that fails them.
func (w ProjectListWorkflow) webhookMembership(pr *github.PullRequest) membership {
	if filtersMembership(pr, w.Filters) == memberNo {
		return memberNo
	}
	return memberUnknown
}

func (w ProjectListWorkflow) includesDiff() bool { return w.IncludeDiff }

// prStateMatches reports whether pr is in the state a workflow lists PRs in.
func prStateMatches(state string, pr *github.PullRequest) bool {
	return state == "all" || pr.GetState() == state
}

func filtersMembership(pr *github.PullRequest, filters []git_tools.PRFilter) membership {
	if len(git_tools.ApplyPRFilters([]*github.PullRequest{pr}, filters)) == 0 {
		return memberNo
	}
	return memberYes
}

// reviewRequestedOf reports whether pr's review is requested of login,
// personally or through one of their teams — what ReviewRequestedRefs'
// searches find.
func reviewRequestedOf(pr *github.PullRequest, login string) membership {
	if userInRequestedReviewers(pr, login) {
		return memberYes
	}
	if len(pr.RequestedTeams) == 0 {
		return memberNo
	}
	mine := git_tools.GetMyTeamRefs()
	if mine == nil {
		// The lookup failed; nil is never what it returns for no teams.
		return memberUnknown
	}
	owner := pr.GetBase().GetRepo().GetOwner().GetLogin()
	for _, team := range pr.RequestedTeams {
		org := team.GetOrganization().GetLogin()
		if org == "" {
			org = owner
		}
		key := org + "/" + team.GetSlug()
		if slices.ContainsFunc(mine, func(ref string) bool { return strings.EqualFold(ref, key) }) {
			return memberYes
		}
	}
	return memberNo
}

// activeWorkflows is the manager's current workflows, as of its last config
// load.
var activeWorkflows atomic.Pointer[[]Workflow]

func setActiveWorkflows(workflows []Workflow) {
	snapshot := slices.Clone(workflows)
	activeWorkflows.Store(&snapshot)
}

func activeWorkflowsSnapshot() []Workflow {
	if p := activeWorkflows.Load(); p != nil {
		return *p
	}
	return nil
}

// rememberedPRs holds the latest PR object seen for each PR, from a cycle's
// listing or a delivery, for re-evaluating PRs after a delivery that carries
// none — a conversation comment, a check run, a commit status.
var rememberedPRs sync.Map // rememberedKey -> rememberedPR

type rememberedPR struct {
	pr *github.PullRequest
	at time.Time
}

// rememberedKey ignores case: a cycle keys PRs by the repository as the config
// spells it, a delivery as GitHub does.
func rememberedKey(key PRKey) PRKey {
	return PRKey{Owner: strings.ToLower(key.Owner), Repo: strings.ToLower(key.Repo), Number: key.Number}
}

// rememberPR records pr as the latest seen for key, unless what is held
// already is newer — a listing taken at the start of a cycle can arrive after
// a delivery that came in while it ran.
func rememberPR(key PRKey, pr *github.PullRequest) {
	if pr == nil {
		return
	}
	k := rememberedKey(key)
	if v, ok := rememberedPRs.Load(k); ok {
		held := v.(rememberedPR).pr
		if held.UpdatedAt != nil && pr.UpdatedAt != nil && held.UpdatedAt.After(pr.UpdatedAt.Time) {
			pr = held
		}
	}
	rememberedPRs.Store(k, rememberedPR{pr: pr, at: time.Now()})
}

func rememberedPRFor(key PRKey) *github.PullRequest {
	if v, ok := rememberedPRs.Load(rememberedKey(key)); ok {
		return v.(rememberedPR).pr
	}
	return nil
}

// pruneRememberedPRs forgets the PRs no listing or delivery has mentioned
// since before.
func pruneRememberedPRs(before time.Time) {
	rememberedPRs.Range(func(k, v any) bool {
		if v.(rememberedPR).at.Before(before) {
			rememberedPRs.Delete(k)
		}
		return true
	})
}

// reevaluations is the PRs waiting to be re-evaluated, each with the PR object
// to judge it by (nil to use the remembered one). Deliveries arrive in bursts —
// a push sets off a check run delivery per check — so a PR waiting already is
// re-evaluated once.
var reevaluations struct {
	mu      sync.Mutex
	pending map[PRKey]*github.PullRequest
}

// reevaluationWake tells runReevaluations there is work.
var reevaluationWake = make(chan struct{}, 1)

// scheduleReevaluation queues key to be checked against the workflows again,
// if webhooks are trusted for it.
func scheduleReevaluation(key PRKey, pr *github.PullRequest) {
	if !webhookHandles(key) {
		return
	}
	reevaluations.mu.Lock()
	if reevaluations.pending == nil {
		reevaluations.pending = map[PRKey]*github.PullRequest{}
	}
	if pr != nil || reevaluations.pending[key] == nil {
		reevaluations.pending[key] = pr
	}
	reevaluations.mu.Unlock()
	select {
	case reevaluationWake <- struct{}{}:
	default:
	}
}

// runReevaluations works through the queue whenever something is added to it.
func runReevaluations() {
	for range reevaluationWake {
		drainReevaluations()
	}
}

// drainReevaluations re-evaluates every PR waiting, once no cycle is writing
// sections.
func drainReevaluations() {
	membershipMu.Lock()
	defer membershipMu.Unlock()
	reevaluations.mu.Lock()
	pending := reevaluations.pending
	reevaluations.pending = nil
	reevaluations.mu.Unlock()

	db := config.C().DB
	for key, pr := range pending {
		if pr == nil {
			pr = rememberedPRFor(key)
		}
		if pr == nil {
			continue
		}
		reevaluateMembershipSafely(db, key, pr)
	}
}

// reevaluateMembershipSafely keeps a panic on one PR from taking the worker
// down with it.
func reevaluateMembershipSafely(db *database.DB, key PRKey, pr *github.PullRequest) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("Panic re-evaluating a PR's sections after a webhook", "pr", key.Number, "repo", key.Repo, "panic", p)
		}
	}()
	reevaluateMembership(db, key, pr)
}

// reevaluateMembership checks pr against every workflow and brings its section
// rows in line: added where it now belongs, rewritten — details included, from
// the caches — where it still does, and dropped where it no longer does. The
// caller holds membershipMu.
func reevaluateMembership(db *database.DB, key PRKey, pr *github.PullRequest) {
	if db == nil || !webhookHandles(key) || !cacheServes(db, key, pr) || pr.Number == nil {
		return
	}
	workflows := activeWorkflowsSnapshot()
	if len(workflows) == 0 {
		return
	}
	fullName := pr.GetBase().GetRepo().GetFullName()
	if fullName == "" {
		return
	}
	identifier := fmt.Sprintf("%s-%d", fullName, pr.GetNumber())
	prName := fmt.Sprintf("%s#%d", fullName, pr.GetNumber())

	// The filters and Details() read the aux store, as they do in a cycle.
	store := NewAuxDataStore()
	store.Set(key, loadCachedAuxData(db, key, pr, AuxDataRequirement{Diff: true}))
	SetCurrentAuxDataStore(store)
	defer SetCurrentAuxDataStore(nil)

	ttl := time.Now().Add(2 * time.Hour).Unix()
	dropped := false
	for _, wf := range workflows {
		member, ok := wf.(webhookMember)
		if !ok {
			continue
		}
		verdict := member.webhookMembership(pr)
		if verdict == memberUnknown {
			continue
		}
		section, err := db.GetSection(wf.GetOrgSectionName())
		if err != nil || section == nil {
			continue
		}
		item, _ := db.GetItem(section.ID, identifier)
		listed := item != nil && slices.Contains(item.GetWorkflows(), wf.GetName())

		switch {
		case verdict == memberNo && listed:
			applyChange(SerializedFileChange{FileChange: &FileChanges{
				ChangeType:   "Delete",
				Identifier:   item.Identifier,
				Status:       item.Status,
				Title:        item.Title,
				SectionID:    section.ID,
				SectionName:  section.SectionName,
				WorkflowName: wf.GetName(),
				HeadSHA:      pr.GetHead().GetSHA(),
			}}, "webhook: no longer matches the workflow")
			webhookActivity.sectionDrops.Add(1)
			dropped = true
			slog.Info("Webhook took a PR out of a section", "pr", prName, "section", section.SectionName, "workflow", wf.GetName())

		case verdict == memberYes:
			change := SyncTODOToSectionDB(db, wf.GetName(), pr, section, member.includesDiff())
			if change.ChangeType == "No Change" {
				continue
			}
			change.TTL = ttl
			change.NotifyOnAdd = member.ShouldNotify()
			applyChange(SerializedFileChange{FileChange: &change, Lines: change.GetLines(2)}, "webhook: matches the workflow")
			if !listed {
				webhookActivity.sectionAdds.Add(1)
				slog.Info("Webhook put a PR into a section", "pr", prName, "section", section.SectionName, "workflow", wf.GetName())
			}
		}
	}
	if dropped {
		pruneOrphanedItems()
	}
}
