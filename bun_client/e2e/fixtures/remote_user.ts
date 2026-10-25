// The local codereviewserver.toml of the remote-mode bridge (start_server.ts
// writes it into the bridge's own HOME), and what registering should send
// from it: the workflows under their TOML keys, the fallback repos and the
// section settings — and none of the settings only a server reads.

export const REMOTE_LOGIN = 'e2e-remote';

export const REMOTE_USER_TOML = `GithubUsername = "${REMOTE_LOGIN}"
Repos = ["acme/widgets", "acme/gadgets"]
SleepDuration = 5
DesktopNotifications = true

[SectionPriority]
"Needs Review" = 1
"My PRs" = 2

[SectionSorting]
"Needs Review" = "oldest_first"

[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "Review requests"
SectionTitle = "Needs Review"
Filters = ["FilterNotDraft"]

[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "My PRs"
SectionTitle = "My PRs"
Repos = ["acme/widgets"]
Filters = ["FilterMyPRs"]
IncludeDiff = true
`;

export const REMOTE_USER_REGISTRATION = {
    Workflows: [
        {
            WorkflowType: 'SyncReviewRequestsWorkflow',
            Name: 'Review requests',
            SectionTitle: 'Needs Review',
            Filters: ['FilterNotDraft'],
        },
        {
            WorkflowType: 'SyncReviewRequestsWorkflow',
            Name: 'My PRs',
            SectionTitle: 'My PRs',
            Repos: ['acme/widgets'],
            Filters: ['FilterMyPRs'],
            IncludeDiff: true,
        },
    ],
    Repos: ['acme/widgets', 'acme/gadgets'],
    SectionPriority: { 'Needs Review': 1, 'My PRs': 2 },
    SectionSorting: { 'Needs Review': 'oldest_first' },
};
