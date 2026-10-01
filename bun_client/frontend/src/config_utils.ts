/**
 * Types and pure helpers for the server configuration editor.
 *
 * The shapes mirror the `RPCHandler.GetConfig` / `RPCHandler.UpdateConfig`
 * replies. Validation here is a client-side convenience so the user gets
 * feedback before a round trip; the server validates again before it writes
 * anything, and its errors are reported in the same shape.
 *
 * Problems with a plugin or an AI setting are root-level (`workflow: -1`) and
 * name what they are about in `field`, as the server does: `Plugins[0].Model`,
 * `AI.DefaultCommand`, `AIFeatures[2].Mode`. `groupProblems` sorts them out.
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
    /** The LLM backend the plugin calls: '', 'gemini' or 'openrouter'. */
    Provider?: string;
    /** The OpenRouter model to ask for; required with Provider 'openrouter'. */
    Model?: string;
}

/** The `[AI]` table: defaults every `[[AIFeatures]]` entry inherits. */
export interface AISettings {
    DefaultProvider?: string;
    DefaultCommand?: string;
    DefaultModel?: string;
}

/** One `[[AIFeatures]]` entry. */
export interface AIFeatureEntry {
    ID: string;
    Enabled?: boolean;
    /** '', 'oneshot' or 'agent'; empty runs the feature's default mode. */
    Mode?: string;
    Automatic?: boolean;
    Provider?: string;
    Command?: string;
    Model?: string;
}

/** An AI feature the server has registered, as `GetConfig` describes it. */
export interface AIFeatureTypeInfo {
    id: string;
    name: string;
    description: string;
    /** Supported execution modes, default first. */
    modes: string[];
    /** Applied to what the server serves (diff order, review ease) rather than a report. */
    applied: boolean;
    /** The root-level key (e.g. ExperimentalLLMReviewEase) that switches it on, if any. */
    legacy_key?: string;
    /** The entry that legacy key stands for while the file has none of its own. */
    legacy?: AIFeatureEntry;
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
    AI?: AISettings;
    AIFeatures?: AIFeatureEntry[] | null;
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
    ai_features?: AIFeatureTypeInfo[];
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
    Plugins: PluginEntry[];
    AI: Required<AISettings>;
    AIFeatures: AIFeatureEntry[];
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

export const AI_PROVIDER_GEMINI = 'gemini';
export const AI_PROVIDER_OPENROUTER = 'openrouter';
export const AI_PROVIDER_COMMAND = 'command';

const AI_PROVIDERS = [AI_PROVIDER_GEMINI, AI_PROVIDER_OPENROUTER, AI_PROVIDER_COMMAND];
/** A plugin is itself a command, so it calls one of the two APIs or neither. */
const PLUGIN_PROVIDERS = [AI_PROVIDER_GEMINI, AI_PROVIDER_OPENROUTER];
const AI_MODES = ['oneshot', 'agent'];

export const PLUGIN_PROVIDER_OPTIONS = [
    { value: '', label: 'Not set (CRS_LLM_PROVIDER, else Gemini)' },
    { value: AI_PROVIDER_GEMINI, label: 'Gemini (GEMINI_API_KEY)' },
    { value: AI_PROVIDER_OPENROUTER, label: 'OpenRouter (OPENROUTER_API_KEY)' },
];

export const AI_DEFAULT_PROVIDER_OPTIONS = [
    { value: '', label: 'Not set (a default command if one is set, else Gemini)' },
    { value: AI_PROVIDER_GEMINI, label: 'Gemini API (GEMINI_API_KEY)' },
    { value: AI_PROVIDER_OPENROUTER, label: 'OpenRouter (OPENROUTER_API_KEY)' },
    { value: AI_PROVIDER_COMMAND, label: 'Command on this machine' },
];

export const AI_FEATURE_PROVIDER_OPTIONS = [
    { value: '', label: 'Inherit the [AI] defaults' },
    { value: AI_PROVIDER_GEMINI, label: 'Gemini API' },
    { value: AI_PROVIDER_OPENROUTER, label: 'OpenRouter' },
    { value: AI_PROVIDER_COMMAND, label: 'Command on this machine' },
];

export const AI_MODE_LABELS: Record<string, string> = {
    oneshot: 'One-shot (a single model call)',
    agent: 'Agent (a multi-turn tool loop)',
};

/** A plugin with every field present, so drafts compare equal to what the server sends back. */
function fullPlugin(plugin: Partial<PluginEntry>): PluginEntry {
    return {
        Name: plugin.Name ?? '',
        Command: plugin.Command ?? '',
        IncludeDiff: !!plugin.IncludeDiff,
        IncludeHeaders: !!plugin.IncludeHeaders,
        IncludeComments: !!plugin.IncludeComments,
        IncludeBranch: !!plugin.IncludeBranch,
        OnlyOnDemand: !!plugin.OnlyOnDemand,
        Provider: plugin.Provider ?? '',
        Model: plugin.Model ?? '',
    };
}

/** An AI feature entry with every field present, like `fullPlugin`. */
export function fullAIFeature(entry: Partial<AIFeatureEntry> & { ID: string }): AIFeatureEntry {
    return {
        ID: entry.ID,
        Enabled: !!entry.Enabled,
        Mode: entry.Mode ?? '',
        Automatic: !!entry.Automatic,
        Provider: entry.Provider ?? '',
        Command: entry.Command ?? '',
        Model: entry.Model ?? '',
    };
}

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
        Plugins: (config.Plugins ?? []).map(fullPlugin),
        AI: {
            DefaultProvider: config.AI?.DefaultProvider ?? '',
            DefaultCommand: config.AI?.DefaultCommand ?? '',
            DefaultModel: config.AI?.DefaultModel ?? '',
        },
        AIFeatures: (config.AIFeatures ?? []).map(fullAIFeature),
    };
}

/** A blank plugin. Most plugins read the diff, so that starts on. */
export function emptyPlugin(): PluginEntry {
    return fullPlugin({ IncludeDiff: true });
}

/**
 * The entry an AI feature starts from when the editor first changes it: what
 * the legacy key stands for when one switches it on, so the feature keeps
 * running as it did, and switched off otherwise.
 */
export function seedAIFeature(info: AIFeatureTypeInfo | undefined, id: string): AIFeatureEntry {
    return fullAIFeature({ ...(info?.legacy ?? {}), ID: id });
}

/** Reports whether an entry sets nothing, so removing it changes nothing either. */
export function isBlankAIFeature(entry: AIFeatureEntry): boolean {
    return (
        !entry.Enabled &&
        !entry.Automatic &&
        !entry.Mode?.trim() &&
        !entry.Provider?.trim() &&
        !entry.Command?.trim() &&
        !entry.Model?.trim()
    );
}

/** How an AI feature reaches a model, as the server resolves it from the config. */
export interface AIProviderChoice {
    provider: string;
    /** The command line the command provider runs; only it reads this. */
    command: string;
    /** The model the openrouter provider asks for; only it reads this. */
    model: string;
}

/**
 * Resolves the provider an AI feature runs with, mirroring the server's
 * `AIProviderFor`: the first of the feature's Provider, the feature's Command
 * (which picks "command"), [AI] DefaultProvider, [AI] DefaultCommand (which
 * picks "command") that is set wins, and "gemini" when none is.
 */
export function resolveAIProvider(ai: AISettings, entry: AIFeatureEntry): AIProviderChoice {
    const feature = {
        provider: entry.Provider?.trim() ?? '',
        command: entry.Command?.trim() ?? '',
        model: entry.Model?.trim() ?? '',
    };
    const defaults = {
        provider: ai.DefaultProvider?.trim() ?? '',
        command: ai.DefaultCommand?.trim() ?? '',
        model: ai.DefaultModel?.trim() ?? '',
    };
    const command = feature.command || defaults.command;
    const model = feature.model || defaults.model;
    let provider = AI_PROVIDER_GEMINI;
    if (feature.provider) provider = feature.provider;
    else if (feature.command) provider = AI_PROVIDER_COMMAND;
    else if (defaults.provider) provider = defaults.provider;
    else if (defaults.command) provider = AI_PROVIDER_COMMAND;
    return { provider, command, model };
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
        Plugins: draft.Plugins.map(plugin => ({
            ...plugin,
            Name: plugin.Name?.trim() ?? '',
            Command: plugin.Command?.trim() ?? '',
            Provider: plugin.Provider?.trim() ?? '',
            Model: plugin.Model?.trim() ?? '',
        })),
        AI: {
            DefaultProvider: draft.AI.DefaultProvider.trim(),
            DefaultCommand: draft.AI.DefaultCommand.trim(),
            DefaultModel: draft.AI.DefaultModel.trim(),
        },
        // Entries keep their order: the server reports problems by index.
        AIFeatures: draft.AIFeatures.map(entry => ({
            ...entry,
            ID: entry.ID?.trim() ?? '',
            Mode: entry.Mode?.trim() ?? '',
            Provider: entry.Provider?.trim() ?? '',
            Command: entry.Command?.trim() ?? '',
            Model: entry.Model?.trim() ?? '',
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
    filters: FilterInfo[],
    aiFeatures: AIFeatureTypeInfo[] = []
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

    problems.push(...validatePlugins(draft.Plugins));
    problems.push(...validateAI(draft.AI, draft.AIFeatures, aiFeatures));
    return problems;
}

/** Checks the [[Plugins]] entries the way the server does. */
function validatePlugins(plugins: PluginEntry[]): ConfigValidationError[] {
    const problems: ConfigValidationError[] = [];
    const seenNames = new Map<string, number>();
    plugins.forEach((plugin, index) => {
        const field = (name: string) => `Plugins[${index}].${name}`;
        const name = plugin.Name ?? '';
        if (!name) {
            problems.push(globalError(field('Name'), 'is required'));
        } else if (seenNames.has(name)) {
            problems.push(
                globalError(
                    field('Name'),
                    `duplicates the name of plugin ${(seenNames.get(name) as number) + 1}; plugin names must be unique`
                )
            );
        } else {
            seenNames.set(name, index);
        }
        if (!plugin.Command) {
            problems.push(globalError(field('Command'), 'is required'));
        }
        const provider = plugin.Provider ?? '';
        if (provider && !PLUGIN_PROVIDERS.includes(provider)) {
            problems.push(
                globalError(
                    field('Provider'),
                    `unknown provider "${provider}" (expected "gemini" or "openrouter")`
                )
            );
        }
        if (provider === AI_PROVIDER_OPENROUTER && !plugin.Model) {
            problems.push(
                globalError(
                    field('Model'),
                    'the openrouter provider needs a model, e.g. "anthropic/claude-sonnet-4.5"'
                )
            );
        }
    });
    return problems;
}

function unknownAIProvider(provider: string): string {
    return `unknown provider "${provider}" (expected "gemini", "openrouter" or "command")`;
}

/** Checks [AI] and the [[AIFeatures]] entries the way the server does. */
function validateAI(
    ai: AISettings,
    entries: AIFeatureEntry[],
    registry: AIFeatureTypeInfo[]
): ConfigValidationError[] {
    const problems: ConfigValidationError[] = [];
    const defaultProvider = ai.DefaultProvider ?? '';
    if (defaultProvider && !AI_PROVIDERS.includes(defaultProvider)) {
        problems.push(globalError('AI.DefaultProvider', unknownAIProvider(defaultProvider)));
    }

    const known = new Map(registry.map(info => [info.id, info]));
    const seenIDs = new Map<string, number>();
    entries.forEach((entry, index) => {
        const field = (name: string) => `AIFeatures[${index}].${name}`;
        const id = entry.ID ?? '';
        const info = known.get(id);
        if (!id) {
            problems.push(globalError(field('ID'), 'is required'));
        } else if (seenIDs.has(id)) {
            problems.push(globalError(field('ID'), `"${id}" already has an entry`));
        } else {
            seenIDs.set(id, index);
            if (known.size > 0 && !info) {
                problems.push(globalError(field('ID'), `unknown AI feature "${id}"`));
            }
        }

        const mode = entry.Mode ?? '';
        if (mode && !AI_MODES.includes(mode)) {
            problems.push(
                globalError(field('Mode'), `unknown mode "${mode}" (expected "oneshot" or "agent")`)
            );
        } else if (mode && info && !info.modes.includes(mode)) {
            problems.push(
                globalError(
                    field('Mode'),
                    `${id} does not run in mode "${mode}" (supported: ${info.modes.join(', ')})`
                )
            );
        }

        const provider = entry.Provider ?? '';
        if (provider && !AI_PROVIDERS.includes(provider)) {
            problems.push(globalError(field('Provider'), unknownAIProvider(provider)));
        }

        // Only an enabled feature ever builds its provider, so a disabled entry
        // left half-configured isn't worth blocking a save over.
        if (entry.Enabled) {
            const choice = resolveAIProvider(ai, entry);
            if (choice.provider === AI_PROVIDER_COMMAND && !choice.command) {
                problems.push(
                    globalError(
                        field('Command'),
                        'the command provider needs a command: set one here or a default command'
                    )
                );
            }
            if (choice.provider === AI_PROVIDER_OPENROUTER && !choice.model) {
                problems.push(
                    globalError(
                        field('Model'),
                        'the openrouter provider needs a model: set one here or a default model'
                    )
                );
            }
        }
    });
    return problems;
}

/** Problems sorted by the part of the editor that shows them. */
export interface GroupedProblems {
    /** Root-level settings, and root keys the editor doesn't model. */
    global: ConfigValidationError[];
    workflows: Map<number, ConfigValidationError[]>;
    /** Keyed by index into Plugins; `field` is the plugin's own field name. */
    plugins: Map<number, ConfigValidationError[]>;
    /** The [AI] table; `field` is the setting's name. */
    ai: ConfigValidationError[];
    /** Keyed by index into AIFeatures; `field` is the entry's own field name. */
    aiFeatures: Map<number, ConfigValidationError[]>;
}

const INDEXED_FIELD = /^(Plugins|AIFeatures)\[(\d+)\]\.(.+)$/;

function addTo(
    map: Map<number, ConfigValidationError[]>,
    key: number,
    problem: ConfigValidationError
) {
    const existing = map.get(key);
    if (existing) {
        existing.push(problem);
    } else {
        map.set(key, [problem]);
    }
}

/**
 * Groups problems so each part of the editor can show its own: workflow
 * problems by workflow index, and the root-level ones by the field they name —
 * `Plugins[1].Model` goes to plugin 1 as `Model`, `AI.DefaultCommand` to the
 * AI defaults as `DefaultCommand`.
 */
export function groupProblems(problems: ConfigValidationError[]): GroupedProblems {
    const grouped: GroupedProblems = {
        global: [],
        workflows: new Map(),
        plugins: new Map(),
        ai: [],
        aiFeatures: new Map(),
    };
    problems.forEach(problem => {
        if (problem.workflow >= 0) {
            addTo(grouped.workflows, problem.workflow, problem);
            return;
        }
        const indexed = INDEXED_FIELD.exec(problem.field);
        if (indexed) {
            const [, list, index, field] = indexed;
            const target = list === 'Plugins' ? grouped.plugins : grouped.aiFeatures;
            addTo(target, Number(index), { ...problem, field });
            return;
        }
        if (problem.field.startsWith('AI.')) {
            grouped.ai.push({ ...problem, field: problem.field.slice('AI.'.length) });
            return;
        }
        grouped.global.push(problem);
    });
    return grouped;
}
