/**
 * Types and pure helpers for the server configuration editor.
 *
 * The shapes mirror the `RPCHandler.GetConfig` / `RPCHandler.UpdateConfig`
 * replies. Validation here is a client-side convenience so the user gets
 * feedback before a round trip; the server validates again before it writes
 * anything, and its errors are reported in the same shape.
 */

export interface WorkflowEntry {
    WorkflowType: string;
    Name: string;
    Owner?: string;
    Repo?: string;
    Repos?: string[] | null;
    JiraEpic?: string;
    Filters?: string[] | null;
    SectionTitle: string;
    PRState?: string;
    GithubUsername?: string;
    IncludeDiff?: boolean;
    Teams?: string[] | null;
    /** null / undefined means "inherit the global setting". */
    DesktopNotifications?: boolean | null;
}

export interface PluginEntry {
    Name: string;
    Command: string;
    IncludeDiff?: boolean;
    IncludeHeaders?: boolean;
    IncludeComments?: boolean;
    IncludeBranch?: boolean;
    OnlyOnDemand?: boolean;
}

export interface ServerConfig {
    Repos: string[];
    /** Minutes between workflow syncs. */
    SleepDuration: number;
    JiraDomain: string;
    GithubUsername: string;
    RepoLocation: string;
    AutoWorktree: boolean;
    DesktopNotifications: boolean;
    SectionPriority: Record<string, number>;
    SectionSorting: Record<string, string>;
    Workflows: WorkflowEntry[];
    Plugins: PluginEntry[];
    ExperimentalLLMFileOrdering: boolean;
    ExperimentalLLMReviewEase: boolean;
}

export interface WorkflowTypeInfo {
    name: string;
    description: string;
    deprecated: boolean;
    deprecated_by?: string;
    required_fields: string[];
    optional_fields: string[];
}

export interface FilterInfo {
    name: string;
    description: string;
    requires_arg: boolean;
    arg_label?: string;
}

/** A single problem with a configuration. `workflow` is -1 for global settings. */
export interface ConfigValidationError {
    workflow: number;
    field: string;
    message: string;
}

export interface ConfigReply {
    okay: boolean;
    message: string;
    path: string;
    config: ServerConfig;
    /** True when no config file exists yet and the server is running its defaults. */
    using_defaults?: boolean;
    workflow_types: WorkflowTypeInfo[];
    filters: FilterInfo[];
    /** Only present on UpdateConfig replies. */
    errors?: ConfigValidationError[];
}

/** Fields the config editor can change. Everything else is left to the server. */
export interface ConfigDraft {
    Repos: string[];
    SleepDuration: number;
    GithubUsername: string;
    RepoLocation: string;
    JiraDomain: string;
    AutoWorktree: boolean;
    DesktopNotifications: boolean;
    Workflows: WorkflowEntry[];
}

export const PR_STATE_OPTIONS = [
    { value: '', label: 'Default (open)' },
    { value: 'open', label: 'Open' },
    { value: 'closed', label: 'Closed' },
    { value: 'all', label: 'All' },
];

export const NOTIFICATION_OPTIONS = [
    { value: 'inherit', label: 'Inherit global setting' },
    { value: 'on', label: 'Always notify' },
    { value: 'off', label: 'Never notify' },
];

/** Builds the editable draft from a config fetched from the server. */
export function draftFromConfig(config: ServerConfig): ConfigDraft {
    return {
        Repos: [...(config.Repos ?? [])],
        SleepDuration: config.SleepDuration,
        GithubUsername: config.GithubUsername ?? '',
        RepoLocation: config.RepoLocation ?? '',
        JiraDomain: config.JiraDomain ?? '',
        AutoWorktree: !!config.AutoWorktree,
        DesktopNotifications: !!config.DesktopNotifications,
        Workflows: (config.Workflows ?? []).map(w => ({ ...w })),
    };
}

/** A blank workflow, pre-filled with the first non-deprecated type. */
export function emptyWorkflow(workflowTypes: WorkflowTypeInfo[]): WorkflowEntry {
    const preferred = workflowTypes.find(t => !t.deprecated) ?? workflowTypes[0];
    return {
        WorkflowType: preferred?.name ?? 'SyncReviewRequestsWorkflow',
        Name: '',
        SectionTitle: '',
        Filters: [],
        Repos: [],
        Teams: [],
        IncludeDiff: false,
        PRState: '',
    };
}

/**
 * Splits an edited text field into list entries.
 *
 * Nothing is trimmed or dropped here: the field is rendered straight back from
 * what this returns, so tidying up mid-edit would delete the separator or space
 * the moment the user typed it. `cleanDraft` does the tidying at save time.
 */
export function splitList(text: string, separator: string): string[] {
    return text.split(separator);
}

/** Renders list entries into a text field. Must use splitList's separator. */
export function joinList(values: string[] | null | undefined, separator: string): string {
    return (values ?? []).join(separator);
}

/** Trims list entries and drops the blank ones. */
export function cleanList(values: string[] | null | undefined): string[] {
    return (values ?? []).map(entry => entry.trim()).filter(entry => entry.length > 0);
}

/** Splits a configured filter into its name and optional argument. */
export function splitFilter(entry: string): { name: string; arg: string } {
    const idx = entry.indexOf(':');
    if (idx === -1) return { name: entry, arg: '' };
    return { name: entry.slice(0, idx), arg: entry.slice(idx + 1) };
}

/** Joins a filter name and argument into its config form. */
export function joinFilter(name: string, arg: string): string {
    return arg ? `${name}:${arg}` : name;
}

/**
 * Normalizes a draft into what actually gets validated and sent: trimmed
 * strings, no blank list entries, no stray spaces around a filter's argument.
 */
export function cleanDraft(draft: ConfigDraft): ConfigDraft {
    return {
        ...draft,
        Repos: cleanList(draft.Repos),
        GithubUsername: draft.GithubUsername.trim(),
        RepoLocation: draft.RepoLocation.trim(),
        JiraDomain: draft.JiraDomain.trim(),
        Workflows: draft.Workflows.map(workflow => ({
            ...workflow,
            Name: workflow.Name?.trim() ?? '',
            SectionTitle: workflow.SectionTitle?.trim() ?? '',
            Repo: workflow.Repo?.trim(),
            JiraEpic: workflow.JiraEpic?.trim(),
            Repos: cleanList(workflow.Repos),
            Teams: cleanList(workflow.Teams),
            Filters: cleanList(workflow.Filters).map(entry => {
                const { name, arg } = splitFilter(entry);
                return joinFilter(name.trim(), arg.trim());
            }),
        })),
    };
}

/** Reports whether a repository entry is in "owner/repo" form. */
export function isValidRepo(entry: string): boolean {
    const parts = entry.trim().split('/');
    return parts.length === 2 && parts[0].length > 0 && parts[1].length > 0;
}

function globalError(field: string, message: string): ConfigValidationError {
    return { workflow: -1, field, message };
}

function workflowError(workflow: number, field: string, message: string): ConfigValidationError {
    return { workflow, field, message };
}

/**
 * Validates a draft the same way the server does, so obvious mistakes are
 * caught before saving. Returns an empty array when the draft looks good.
 *
 * The draft is cleaned first, so a half-typed "owner/repo, " isn't reported as
 * an empty repository — the server would drop that blank entry too.
 */
export function validateDraft(
    rawDraft: ConfigDraft,
    workflowTypes: WorkflowTypeInfo[],
    filters: FilterInfo[]
): ConfigValidationError[] {
    const draft = cleanDraft(rawDraft);
    const problems: ConfigValidationError[] = [];

    if (!Number.isFinite(draft.SleepDuration) || draft.SleepDuration <= 0) {
        problems.push(globalError('SleepDuration', 'must be a positive number of minutes'));
    } else if (draft.SleepDuration > 1440) {
        problems.push(globalError('SleepDuration', 'must be at most 1440 minutes (24 hours)'));
    }

    draft.Repos.forEach(repo => {
        if (!isValidRepo(repo)) {
            problems.push(globalError('Repos', `"${repo}" is not in "owner/repo" form`));
        }
    });

    const knownTypes = new Set(workflowTypes.map(t => t.name));
    const filtersByName = new Map(filters.map(f => [f.name, f]));
    const seenNames = new Map<string, number>();

    draft.Workflows.forEach((workflow, index) => {
        const name = workflow.Name?.trim() ?? '';
        if (!name) {
            problems.push(workflowError(index, 'Name', 'is required'));
        } else if (seenNames.has(name)) {
            problems.push(
                workflowError(
                    index,
                    'Name',
                    `duplicates the name of workflow ${(seenNames.get(name) as number) + 1}; names must be unique`
                )
            );
        } else {
            seenNames.set(name, index);
        }

        if (!workflow.SectionTitle?.trim()) {
            problems.push(workflowError(index, 'SectionTitle', 'is required'));
        }

        if (!workflow.WorkflowType) {
            problems.push(workflowError(index, 'WorkflowType', 'is required'));
        } else if (knownTypes.size > 0 && !knownTypes.has(workflow.WorkflowType)) {
            problems.push(
                workflowError(
                    index,
                    'WorkflowType',
                    `unknown workflow type "${workflow.WorkflowType}"`
                )
            );
        }

        (workflow.Repos ?? []).forEach(repo => {
            if (!isValidRepo(repo)) {
                problems.push(
                    workflowError(index, 'Repos', `"${repo}" is not in "owner/repo" form`)
                );
            }
        });

        if (
            (workflow.Repos ?? []).length === 0 &&
            draft.Repos.length === 0 &&
            workflow.WorkflowType !== 'SingleRepoSyncReviewRequestsWorkflow'
        ) {
            problems.push(
                workflowError(index, 'Repos', 'is required when no global repository list is set')
            );
        }

        (workflow.Filters ?? []).forEach(entry => {
            const { name: filterName, arg } = splitFilter(entry);
            const info = filtersByName.get(filterName);
            if (!info) {
                if (filtersByName.size > 0) {
                    problems.push(
                        workflowError(index, 'Filters', `unknown filter "${filterName}"`)
                    );
                }
                return;
            }
            if (info.requires_arg && !arg.trim()) {
                problems.push(
                    workflowError(
                        index,
                        'Filters',
                        `${filterName} requires a ${info.arg_label || 'value'}`
                    )
                );
            }
        });

        if (workflow.WorkflowType === 'ProjectListWorkflow' && !workflow.JiraEpic?.trim()) {
            problems.push(
                workflowError(index, 'JiraEpic', 'is required (the Jira epic key, e.g. BOARD-123)')
            );
        }

        if (
            workflow.WorkflowType === 'SingleRepoSyncReviewRequestsWorkflow' &&
            !isValidRepo(workflow.Repo ?? '')
        ) {
            problems.push(
                workflowError(index, 'Repo', 'is required and must be in "owner/repo" form')
            );
        }
    });

    return problems;
}

/** Groups problems by workflow index so each editor card can show its own. */
export function problemsByWorkflow(
    problems: ConfigValidationError[]
): Map<number, ConfigValidationError[]> {
    const grouped = new Map<number, ConfigValidationError[]>();
    problems.forEach(problem => {
        const existing = grouped.get(problem.workflow);
        if (existing) {
            existing.push(problem);
        } else {
            grouped.set(problem.workflow, [problem]);
        }
    });
    return grouped;
}
