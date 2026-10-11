package workflows

// The workflow sets a cycle works with.
//
// A hosted server keeps a dashboard for each registered user as well as its
// own: the config file's workflows fill the server's (user ""), and each user's
// stored workflows fill theirs. Which of those workflows a cycle runs, and which
// of them may hold items, are deliberately different questions:
//
//   - The run set is the config's workflows plus those of every user seen
//     within UserActiveHours. An idle user's workflows would spend the GitHub
//     budget on a dashboard nobody is looking at.
//   - The owned set is the config's workflows plus those of every registered
//     user, idle or not. Release and prune go by it: were they to go by the run
//     set, a user away for a day would come back to a dashboard emptied by the
//     cycles that skipped them, and wait a cycle to see it refill. A user who is
//     deleted — swept by retention, or by an administrator — drops out of it,
//     which releases and prunes their items with no cleanup code of its own.
//
// Both are rebuilt every cycle, after the config is reloaded, so a user who
// registers, changes their workflows or comes back is picked up by the next one.

import (
	"crs/config"
	"crs/database"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
)

// RunSet builds the workflows a cycle runs and ReprocessPR consults: the config
// file's, on the server's own dashboard, then those of every user seen within
// cfg.UserActiveHours of now, each on that user's. A workflow that fails to
// decode or build is skipped with a log, as MatchWorkflows skips a bad config
// workflow; when the users cannot be loaded at all, the set is the config's
// alone.
func RunSet(cfg config.Config, now time.Time) []Workflow {
	run := buildDashboard(cfg.RawWorkflows, cfg.Repos, cfg.JiraDomain, cfg.SectionPriority)

	dashboards, err := loadUserDashboards(cfg.DB)
	if err != nil {
		slog.Error("Failed to load users; running only the config's workflows", "error", err)
		return run
	}
	for _, d := range dashboards {
		if !userActive(d.user, cfg.UserActiveHours, now) {
			slog.Debug("Skipping an idle user's workflows", "user", d.user.Login, "last_seen", d.user.LastSeen)
			continue
		}
		// A user's own Repos stand in for the server's, which are what a
		// workflow naming none falls back to otherwise.
		repos := cfg.Repos
		if len(d.user.Repos) > 0 {
			repos = d.user.Repos
		}
		run = append(run, buildDashboard(d.runnable(), repos, cfg.JiraDomain, d.user.SectionPriority)...)
	}
	return run
}

// buildDashboard builds one dashboard's workflows. repos is the fallback for a
// workflow that names none, and sectionPriority where each section sorts.
func buildDashboard(raws []config.RawWorkflow, repos []string, jiraDomain string, sectionPriority map[string]int) []Workflow {
	built := MatchWorkflows(raws, &repos, jiraDomain)
	for i, wf := range built {
		built[i] = withSectionPriority(wf, sectionPriority[wf.GetOrgSectionName()])
	}
	return built
}

// withSectionPriority returns wf with its section sorting at priority.
// MatchWorkflows builds a workflow from its own entry alone, and where its
// section sorts is a setting of the dashboard it lands on rather than of the
// entry, so it is applied afterwards.
func withSectionPriority(wf Workflow, priority int) Workflow {
	switch w := wf.(type) {
	case SyncReviewRequestsWorkflow:
		w.SectionPriority = priority
		return w
	case ProjectListWorkflow:
		w.SectionPriority = priority
		return w
	case SearchWorkflow:
		w.SectionPriority = priority
		return w
	}
	return wf
}

// ownedSections maps the ownership key of every workflow that may hold items —
// the config file's, and every registered user's, idle or not — to the section
// it writes into. Like the config file it reads stored workflows rather than
// built ones: one that fails to build (an unknown type, a filter this server
// does not have) keeps its items until it is actually removed, so a typo never
// costs a dashboard its contents.
//
// ok is false when the set cannot be told completely, and the caller must then
// release and prune nothing this cycle: the users failed to load, or a stored
// workflow is too broken to say which section it writes into. Reading either as
// "no such workflow" would empty the dashboards it backs.
func ownedSections(cfg config.Config) (owned map[string]database.SectionRef, ok bool) {
	owned = make(map[string]database.SectionRef, len(cfg.RawWorkflows))
	// The config file's workflows are the server's own, so their sections are
	// on the server's dashboard, user "".
	for _, wf := range cfg.RawWorkflows {
		owned[config.OwnershipKey("", wf.Name)] = database.SectionRef{User: "", Name: wf.SectionTitle}
	}

	dashboards, err := loadUserDashboards(cfg.DB)
	if err != nil {
		slog.Error("Failed to load users; skipping ownership release and section pruning this cycle", "error", err)
		return nil, false
	}
	for _, d := range dashboards {
		for _, wf := range d.workflows {
			if d.undecodable[wf.Name] && wf.SectionTitle == "" {
				slog.Error("Cannot tell which section a stored user workflow writes into; skipping ownership release and section pruning this cycle",
					"user", d.user.Login, "workflow", wf.Name)
				return nil, false
			}
			owned[config.OwnershipKey(d.user.Login, wf.Name)] = database.SectionRef{User: d.user.Login, Name: wf.SectionTitle}
		}
	}
	return owned, true
}

// userDashboard is one registered user and their stored workflows.
type userDashboard struct {
	user database.User
	// workflows holds every stored row, decoded as far as it would go and made
	// the user's (AsUserWorkflow), in the user's order.
	workflows []config.RawWorkflow
	// undecodable names the rows whose JSON did not decode. They are not run,
	// but still own what they can be seen to (ownedSections).
	undecodable map[string]bool
}

// runnable is the user's workflows less the ones that did not decode.
func (d userDashboard) runnable() []config.RawWorkflow {
	runnable := make([]config.RawWorkflow, 0, len(d.workflows))
	for _, wf := range d.workflows {
		if !d.undecodable[wf.Name] {
			runnable = append(runnable, wf)
		}
	}
	return runnable
}

// loadUserDashboards reads every registered user and their stored workflows,
// ordered by login. A database that is not open has no users.
func loadUserDashboards(db *database.DB) ([]userDashboard, error) {
	if db == nil {
		return nil, nil
	}
	users, err := db.ListUsers()
	if err != nil {
		return nil, err
	}
	stored, err := db.GetAllUserWorkflows()
	if err != nil {
		return nil, err
	}

	dashboards := make([]userDashboard, 0, len(users))
	for _, u := range users {
		d := userDashboard{user: u}
		// Rows are keyed by the login as the user registered it, which is the
		// case ListUsers returns.
		for _, row := range stored[u.Login] {
			raw, err := DecodeUserWorkflow(row)
			if err != nil {
				slog.Error("Skipping a stored user workflow that does not decode",
					"user", u.Login, "workflow", row.Name, "error", err)
				if d.undecodable == nil {
					d.undecodable = map[string]bool{}
				}
				d.undecodable[row.Name] = true
			}
			d.workflows = append(d.workflows, AsUserWorkflow(u.Login, raw))
		}
		dashboards = append(dashboards, d)
	}
	return dashboards, nil
}

// DecodeUserWorkflow decodes a stored user workflow into the RawWorkflow it was
// saved from: the JSON of a config.RawWorkflow, keyed by its field names, which
// are also its TOML keys. The row's Name wins over the JSON's, as the name the
// row is stored — and unique — under.
//
// A value of the wrong type fails the decode but, as with encoding/json
// generally, leaves the other fields filled, so a caller that only needs to
// know where the workflow writes can still read its SectionTitle.
func DecodeUserWorkflow(row database.UserWorkflow) (config.RawWorkflow, error) {
	var raw config.RawWorkflow
	err := json.Unmarshal([]byte(row.JSON), &raw)
	raw.Name = row.Name
	return raw, err
}

// AsUserWorkflow makes raw one of login's workflows: it fills login's dashboard
// and, unless it names another GithubUsername, is about login. The username is
// filled in here, before any filter is built, because the identity filters and
// the search workflows read it; a config workflow inherits the root-level
// GithubUsername instead (config.parseConfig), which is the server owner's and
// never a user's. What the user stored is left as they sent it.
func AsUserWorkflow(login string, raw config.RawWorkflow) config.RawWorkflow {
	raw.User = login
	if strings.TrimSpace(raw.GithubUsername) == "" {
		raw.GithubUsername = login
	}
	return raw
}

// userActive reports whether u was seen within activeHours of now. 0 counts
// everyone as active, and so does a negative count, which validation rejects.
func userActive(u database.User, activeHours int, now time.Time) bool {
	if activeHours <= 0 {
		return true
	}
	return !u.LastSeen.Before(now.Add(-time.Duration(activeHours) * time.Hour))
}

// sweepIdleUsers deletes the users idle for longer than UserRetentionDays,
// along with their workflows, drafts and feedback. It runs ahead of the cycle's
// release and prune, which then find the swept users gone from the owned set:
// their items are released and pruned, and their emptied sections deleted, in
// the same cycle.
func sweepIdleUsers(cfg config.Config, now time.Time) {
	if cfg.UserRetentionDays <= 0 || cfg.DB == nil {
		return
	}
	cutoff := now.Add(-time.Duration(cfg.UserRetentionDays) * 24 * time.Hour)
	swept, err := cfg.DB.DeleteUsersIdleSince(cutoff)
	if err != nil {
		slog.Error("Failed to delete idle users", "retention_days", cfg.UserRetentionDays, "error", err)
		return
	}
	if len(swept) > 0 {
		slog.Info("Deleted users idle past the retention window", "users", swept, "retention_days", cfg.UserRetentionDays)
	}
}
