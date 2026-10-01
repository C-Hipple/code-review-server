import { expect, test, describe } from 'bun:test';
import {
    cleanDraft,
    cleanList,
    draftFromConfig,
    emptyPlugin,
    emptyWorkflow,
    fullAIFeature,
    groupProblems,
    isBlankAIFeature,
    isValidRepo,
    joinFilter,
    joinList,
    resolveAIProvider,
    seedAIFeature,
    splitFilter,
    splitList,
    validateDraft,
} from './config_utils';
import type {
    AIFeatureTypeInfo,
    ConfigDraft,
    FilterInfo,
    ServerConfig,
    WorkflowTypeInfo,
} from './config_utils';

const workflowTypes: WorkflowTypeInfo[] = [
    {
        name: 'SyncReviewRequestsWorkflow',
        description: '',
        deprecated: false,
        required_fields: [],
        optional_fields: [],
    },
    {
        name: 'SingleRepoSyncReviewRequestsWorkflow',
        description: '',
        deprecated: true,
        required_fields: ['Repo'],
        optional_fields: [],
    },
    {
        name: 'ProjectListWorkflow',
        description: '',
        deprecated: false,
        required_fields: ['JiraEpic'],
        optional_fields: [],
    },
];

const filters: FilterInfo[] = [
    { name: 'FilterNotDraft', description: '', requires_arg: false },
    { name: 'FilterByLabel', description: '', requires_arg: true, arg_label: 'label' },
];

const aiFeatures: AIFeatureTypeInfo[] = [
    {
        id: 'comments-addressed',
        name: 'Comments addressed',
        description: '',
        modes: ['oneshot', 'agent'],
        applied: false,
    },
    {
        id: 'review-ease',
        name: 'Review ease',
        description: '',
        modes: ['oneshot'],
        applied: true,
        legacy_key: 'ExperimentalLLMReviewEase',
        legacy: {
            ID: 'review-ease',
            Enabled: true,
            Automatic: true,
            Provider: 'gemini',
            Mode: '',
            Command: '',
            Model: '',
        },
    },
];

const baseDraft = (overrides: Partial<ConfigDraft> = {}): ConfigDraft => ({
    Repos: ['owner/repo'],
    SleepDuration: 10,
    GithubUsername: 'me',
    RepoLocation: '~/',
    JiraDomain: '',
    AutoWorktree: false,
    DesktopNotifications: false,
    Workflows: [
        {
            WorkflowType: 'SyncReviewRequestsWorkflow',
            Name: 'My Open PRs',
            SectionTitle: 'My PRs',
            Filters: ['FilterNotDraft'],
        },
    ],
    Plugins: [],
    AI: { DefaultProvider: '', DefaultCommand: '', DefaultModel: '' },
    AIFeatures: [],
    ...overrides,
});

const validate = (draft: ConfigDraft) => validateDraft(draft, workflowTypes, filters, aiFeatures);

/** The fields of the problems validate finds, e.g. ['Plugins[0].Model']. */
const problemFields = (draft: ConfigDraft) => validate(draft).map(p => p.field);

describe('list helpers', () => {
    test('splitList keeps what the user typed, separators and all', () => {
        // Trimming or dropping here would eat the newline as it's typed.
        expect(splitList('owner/one\n', '\n')).toEqual(['owner/one', '']);
        expect(splitList('a, b', ',')).toEqual(['a', ' b']);
    });

    test('joinList round-trips splitList exactly', () => {
        const typed = 'owner/one\n\nowner/two ';
        expect(joinList(splitList(typed, '\n'), '\n')).toBe(typed);
    });

    test('joinList handles null', () => {
        expect(joinList(null, '\n')).toBe('');
    });

    test('cleanList trims entries and drops the blank ones', () => {
        expect(cleanList([' owner/one ', '', '  ', 'owner/two'])).toEqual([
            'owner/one',
            'owner/two',
        ]);
    });
});

describe('cleanDraft', () => {
    test('tidies the values that get sent to the server', () => {
        const draft = baseDraft({ Repos: ['owner/repo ', ''], GithubUsername: ' me ' });
        draft.Workflows[0] = {
            ...draft.Workflows[0],
            Name: ' My Open PRs ',
            SectionTitle: ' My PRs ',
            Repos: ['owner/other ', ''],
            Teams: [' team-a', ''],
            Filters: ['FilterByLabel: needs review '],
        };

        const cleaned = cleanDraft(draft);
        expect(cleaned.Repos).toEqual(['owner/repo']);
        expect(cleaned.GithubUsername).toBe('me');
        expect(cleaned.Workflows[0].Name).toBe('My Open PRs');
        expect(cleaned.Workflows[0].SectionTitle).toBe('My PRs');
        expect(cleaned.Workflows[0].Repos).toEqual(['owner/other']);
        expect(cleaned.Workflows[0].Teams).toEqual(['team-a']);
        expect(cleaned.Workflows[0].Filters).toEqual(['FilterByLabel:needs review']);
    });

    test('tidies plugins and AI settings, keeping AI entries in order', () => {
        const draft = baseDraft({
            Plugins: [{ ...emptyPlugin(), Name: ' Summarize ', Command: 'summarize_diff ' }],
            AI: { DefaultProvider: '', DefaultCommand: ' claude -p ', DefaultModel: '' },
            AIFeatures: [
                fullAIFeature({ ID: ' review-ease', Model: ' openai/gpt-5 ' }),
                fullAIFeature({ ID: 'comments-addressed' }),
            ],
        });

        const cleaned = cleanDraft(draft);
        expect(cleaned.Plugins[0].Name).toBe('Summarize');
        expect(cleaned.Plugins[0].Command).toBe('summarize_diff');
        expect(cleaned.AI.DefaultCommand).toBe('claude -p');
        expect(cleaned.AIFeatures.map(f => f.ID)).toEqual(['review-ease', 'comments-addressed']);
        expect(cleaned.AIFeatures[0].Model).toBe('openai/gpt-5');
    });

    test('leaves the original draft alone', () => {
        const draft = baseDraft({ Repos: ['owner/repo '] });
        cleanDraft(draft);
        expect(draft.Repos).toEqual(['owner/repo ']);
    });
});

describe('filter helpers', () => {
    test('splitFilter separates the argument', () => {
        expect(splitFilter('FilterByLabel:needs review')).toEqual({
            name: 'FilterByLabel',
            arg: 'needs review',
        });
    });

    test('splitFilter leaves argument-free filters alone', () => {
        expect(splitFilter('FilterNotDraft')).toEqual({ name: 'FilterNotDraft', arg: '' });
    });

    test('joinFilter omits an empty argument', () => {
        expect(joinFilter('FilterNotDraft', '')).toBe('FilterNotDraft');
        expect(joinFilter('FilterByLabel', 'bug')).toBe('FilterByLabel:bug');
    });

    test('joinFilter keeps an argument mid-edit intact', () => {
        // "needs " must survive so the user can go on to type "review".
        expect(joinFilter('FilterByLabel', 'needs ')).toBe('FilterByLabel:needs ');
    });
});

describe('isValidRepo', () => {
    test('accepts owner/repo', () => {
        expect(isValidRepo('C-Hipple/code-review-server')).toBe(true);
    });

    test('rejects anything else', () => {
        expect(isValidRepo('code-review-server')).toBe(false);
        expect(isValidRepo('a/b/c')).toBe(false);
        expect(isValidRepo('/repo')).toBe(false);
        expect(isValidRepo('')).toBe(false);
    });
});

describe('draftFromConfig', () => {
    test('copies workflows so edits do not mutate the fetched config', () => {
        const config = {
            Repos: ['owner/repo'],
            SleepDuration: 5,
            JiraDomain: '',
            GithubUsername: 'me',
            RepoLocation: '~/',
            AutoWorktree: false,
            DesktopNotifications: true,
            SectionPriority: {},
            SectionSorting: {},
            Workflows: [
                { WorkflowType: 'SyncReviewRequestsWorkflow', Name: 'A', SectionTitle: 'Sec' },
            ],
            Plugins: [],
        } as ServerConfig;

        const draft = draftFromConfig(config);
        draft.Workflows[0].Name = 'B';
        draft.Repos.push('other/repo');

        expect(config.Workflows[0].Name).toBe('A');
        expect(config.Repos).toEqual(['owner/repo']);
    });

    test('fills in plugins and AI settings a server may leave out', () => {
        const config = {
            Repos: [],
            SleepDuration: 10,
            Workflows: [],
            Plugins: [{ Name: 'Summarize', Command: 'summarize_diff', IncludeDiff: true }],
        } as unknown as ServerConfig;

        const draft = draftFromConfig(config);
        expect(draft.Plugins[0]).toEqual({
            Name: 'Summarize',
            Command: 'summarize_diff',
            IncludeDiff: true,
            IncludeHeaders: false,
            IncludeComments: false,
            IncludeBranch: false,
            OnlyOnDemand: false,
            Provider: '',
            Model: '',
        });
        expect(draft.AI).toEqual({ DefaultProvider: '', DefaultCommand: '', DefaultModel: '' });
        expect(draft.AIFeatures).toEqual([]);
    });

    test('drafts of the same config compare equal, so a fresh load is not dirty', () => {
        const config = {
            Repos: [],
            SleepDuration: 10,
            Workflows: [],
            Plugins: [],
            AI: { DefaultCommand: 'claude -p' },
            AIFeatures: [{ ID: 'review-ease', Enabled: true }],
        } as unknown as ServerConfig;
        expect(JSON.stringify(draftFromConfig(config))).toBe(
            JSON.stringify(draftFromConfig(config))
        );
        expect(draftFromConfig(config).AIFeatures[0]).toEqual(
            fullAIFeature({ ID: 'review-ease', Enabled: true })
        );
    });
});

describe('emptyWorkflow', () => {
    test('defaults to the first non-deprecated type', () => {
        expect(emptyWorkflow(workflowTypes).WorkflowType).toBe('SyncReviewRequestsWorkflow');
    });

    test('falls back when the server sent no types', () => {
        expect(emptyWorkflow([]).WorkflowType).toBe('SyncReviewRequestsWorkflow');
    });
});

describe('validateDraft', () => {
    test('accepts a workable config', () => {
        expect(validate(baseDraft())).toEqual([]);
    });

    test('requires a positive sleep duration', () => {
        const problems = validate(baseDraft({ SleepDuration: 0 }));
        expect(problems).toHaveLength(1);
        expect(problems[0].field).toBe('SleepDuration');
        expect(problems[0].workflow).toBe(-1);
    });

    test('rejects an absurd sleep duration', () => {
        expect(validate(baseDraft({ SleepDuration: 5000 }))[0].field).toBe('SleepDuration');
    });

    test('rejects malformed global repos', () => {
        const problems = validate(baseDraft({ Repos: ['not-a-repo'] }));
        expect(problems.some(p => p.field === 'Repos' && p.workflow === -1)).toBe(true);
    });

    test('requires a workflow name and section title', () => {
        const draft = baseDraft();
        draft.Workflows[0].Name = '   ';
        draft.Workflows[0].SectionTitle = '';
        const fields = validate(draft).map(p => p.field);
        expect(fields).toContain('Name');
        expect(fields).toContain('SectionTitle');
    });

    test('rejects duplicate workflow names', () => {
        const draft = baseDraft();
        draft.Workflows.push({ ...draft.Workflows[0] });
        const problems = validate(draft);
        expect(problems).toHaveLength(1);
        expect(problems[0].workflow).toBe(1);
        expect(problems[0].message).toContain('duplicates');
    });

    test('rejects unknown workflow types', () => {
        const draft = baseDraft();
        draft.Workflows[0].WorkflowType = 'MagicWorkflow';
        expect(validate(draft)[0].field).toBe('WorkflowType');
    });

    test('requires an argument for filters that take one', () => {
        const draft = baseDraft();
        draft.Workflows[0].Filters = ['FilterByLabel'];
        const problems = validate(draft);
        expect(problems).toHaveLength(1);
        expect(problems[0].message).toContain('label');
    });

    test('accepts a filter with its argument', () => {
        const draft = baseDraft();
        draft.Workflows[0].Filters = ['FilterByLabel:bug'];
        expect(validate(draft)).toEqual([]);
    });

    test('rejects unknown filters', () => {
        const draft = baseDraft();
        draft.Workflows[0].Filters = ['FilterNope'];
        expect(validate(draft)[0].message).toContain('unknown filter');
    });

    test('requires repos somewhere', () => {
        const draft = baseDraft({ Repos: [] });
        const problems = validate(draft);
        expect(problems.some(p => p.field === 'Repos' && p.workflow === 0)).toBe(true);
    });

    test('accepts a workflow-level repo list when the global one is empty', () => {
        const draft = baseDraft({ Repos: [] });
        draft.Workflows[0].Repos = ['owner/repo'];
        expect(validate(draft)).toEqual([]);
    });

    test('requires a Jira epic for project list workflows', () => {
        const draft = baseDraft();
        draft.Workflows[0].WorkflowType = 'ProjectListWorkflow';
        expect(validate(draft)[0].field).toBe('JiraEpic');
    });

    test('requires a repo for the single repo workflow', () => {
        const draft = baseDraft();
        draft.Workflows[0].WorkflowType = 'SingleRepoSyncReviewRequestsWorkflow';
        expect(validate(draft)[0].field).toBe('Repo');
    });

    test('skips type checks when the server sent no registries', () => {
        const draft = baseDraft();
        draft.Workflows[0].WorkflowType = 'SomethingNew';
        draft.Workflows[0].Filters = ['FilterUnknown'];
        expect(validateDraft(draft, [], [])).toEqual([]);
    });
});

describe('validateDraft plugins', () => {
    const plugin = (overrides = {}) => ({
        ...emptyPlugin(),
        Name: 'Summarize',
        Command: 'summarize_diff',
        ...overrides,
    });

    test('accepts a working plugin', () => {
        expect(validate(baseDraft({ Plugins: [plugin()] }))).toEqual([]);
        expect(
            validate(
                baseDraft({
                    Plugins: [plugin({ Provider: 'openrouter', Model: 'openai/gpt-5' })],
                })
            )
        ).toEqual([]);
    });

    test('requires a name and a command', () => {
        const fields = problemFields(baseDraft({ Plugins: [plugin({ Name: ' ', Command: '' })] }));
        expect(fields).toEqual(['Plugins[0].Name', 'Plugins[0].Command']);
    });

    test('rejects duplicate names', () => {
        const problems = validate(
            baseDraft({ Plugins: [plugin(), plugin({ Name: 'Summarize ' })] })
        );
        expect(problems).toHaveLength(1);
        expect(problems[0].field).toBe('Plugins[1].Name');
        expect(problems[0].workflow).toBe(-1);
    });

    test('names only the two LLM backends a plugin can call', () => {
        expect(problemFields(baseDraft({ Plugins: [plugin({ Provider: 'command' })] }))).toEqual([
            'Plugins[0].Provider',
        ]);
    });

    test('needs a model for OpenRouter', () => {
        expect(problemFields(baseDraft({ Plugins: [plugin({ Provider: 'openrouter' })] }))).toEqual(
            ['Plugins[0].Model']
        );
    });
});

describe('validateDraft AI', () => {
    test('accepts working settings', () => {
        const draft = baseDraft({
            AI: { DefaultProvider: '', DefaultCommand: 'claude -p', DefaultModel: '' },
            AIFeatures: [
                fullAIFeature({ ID: 'comments-addressed', Enabled: true, Mode: 'agent' }),
                fullAIFeature({
                    ID: 'review-ease',
                    Enabled: true,
                    Provider: 'openrouter',
                    Model: 'openai/gpt-5',
                }),
            ],
        });
        expect(validate(draft)).toEqual([]);
    });

    test('rejects an unknown default provider', () => {
        const draft = baseDraft({
            AI: { DefaultProvider: 'openai', DefaultCommand: '', DefaultModel: '' },
        });
        expect(problemFields(draft)).toEqual(['AI.DefaultProvider']);
    });

    test('rejects unknown and duplicate feature IDs', () => {
        const draft = baseDraft({
            AIFeatures: [
                fullAIFeature({ ID: 'mermaid' }),
                fullAIFeature({ ID: 'review-ease' }),
                fullAIFeature({ ID: 'review-ease' }),
                fullAIFeature({ ID: ' ' }),
            ],
        });
        expect(problemFields(draft)).toEqual([
            'AIFeatures[0].ID',
            'AIFeatures[2].ID',
            'AIFeatures[3].ID',
        ]);
    });

    test('rejects a mode the feature does not run in', () => {
        const draft = baseDraft({
            AIFeatures: [
                fullAIFeature({ ID: 'review-ease', Mode: 'agent' }),
                fullAIFeature({ ID: 'comments-addressed', Mode: 'swarm' }),
            ],
        });
        const problems = validate(draft);
        expect(problems.map(p => p.field)).toEqual(['AIFeatures[0].Mode', 'AIFeatures[1].Mode']);
        expect(problems[0].message).toContain('supported: oneshot');
    });

    test('needs a command wherever an enabled feature lands on the command provider', () => {
        const draft = baseDraft({
            AIFeatures: [
                fullAIFeature({ ID: 'comments-addressed', Enabled: true, Provider: 'command' }),
            ],
        });
        expect(problemFields(draft)).toEqual(['AIFeatures[0].Command']);

        draft.AI.DefaultCommand = 'claude -p';
        expect(validate(draft)).toEqual([]);
    });

    test('needs a model wherever an enabled feature lands on OpenRouter', () => {
        const draft = baseDraft({
            AI: { DefaultProvider: 'openrouter', DefaultCommand: '', DefaultModel: '' },
            AIFeatures: [fullAIFeature({ ID: 'comments-addressed', Enabled: true })],
        });
        expect(problemFields(draft)).toEqual(['AIFeatures[0].Model']);

        draft.AI.DefaultModel = 'anthropic/claude-sonnet-4.5';
        expect(validate(draft)).toEqual([]);
    });

    test('leaves a disabled, half-configured entry alone', () => {
        const draft = baseDraft({
            AIFeatures: [fullAIFeature({ ID: 'comments-addressed', Provider: 'openrouter' })],
        });
        expect(validate(draft)).toEqual([]);
    });

    test('skips the registry checks when the server sent no AI features', () => {
        const draft = baseDraft({
            AIFeatures: [fullAIFeature({ ID: 'something-new', Mode: 'agent' })],
        });
        expect(validateDraft(draft, workflowTypes, filters, [])).toEqual([]);
    });
});

describe('resolveAIProvider', () => {
    const defaults = (overrides = {}) => ({
        DefaultProvider: '',
        DefaultCommand: '',
        DefaultModel: '',
        ...overrides,
    });
    const entry = (overrides = {}) => fullAIFeature({ ID: 'comments-addressed', ...overrides });

    test('falls back to Gemini when nothing is set', () => {
        expect(resolveAIProvider(defaults(), entry()).provider).toBe('gemini');
    });

    test("prefers the feature's own settings, and a named provider over a command", () => {
        // Rule 1: the feature's Provider.
        expect(
            resolveAIProvider(
                defaults({ DefaultCommand: 'claude -p' }),
                entry({ Provider: 'gemini', Command: 'llm' })
            ).provider
        ).toBe('gemini');
        // Rule 2: the feature's Command beats any [AI] default.
        expect(
            resolveAIProvider(
                defaults({ DefaultProvider: 'openrouter' }),
                entry({ Command: 'llm' })
            ).provider
        ).toBe('command');
        // Rule 3: [AI] DefaultProvider beats DefaultCommand.
        expect(
            resolveAIProvider(
                defaults({ DefaultProvider: 'gemini', DefaultCommand: 'claude -p' }),
                entry()
            ).provider
        ).toBe('gemini');
        // Rule 4: [AI] DefaultCommand.
        expect(resolveAIProvider(defaults({ DefaultCommand: 'claude -p' }), entry())).toEqual({
            provider: 'command',
            command: 'claude -p',
            model: '',
        });
    });

    test('takes the command and model from the feature, else the defaults', () => {
        const choice = resolveAIProvider(
            defaults({
                DefaultProvider: 'openrouter',
                DefaultModel: 'anthropic/claude-sonnet-4.5',
            }),
            entry({ Model: 'openai/gpt-5' })
        );
        expect(choice).toEqual({ provider: 'openrouter', command: '', model: 'openai/gpt-5' });
    });
});

describe('AI feature entries', () => {
    test('a new entry starts from what the legacy key stands for', () => {
        const seeded = seedAIFeature(aiFeatures[1], 'review-ease');
        expect(seeded.Enabled).toBe(true);
        expect(seeded.Automatic).toBe(true);
        expect(seeded.Provider).toBe('gemini');
    });

    test('and switched off otherwise', () => {
        expect(seedAIFeature(aiFeatures[0], 'comments-addressed')).toEqual(
            fullAIFeature({ ID: 'comments-addressed' })
        );
        expect(seedAIFeature(undefined, 'other').ID).toBe('other');
    });

    test('isBlankAIFeature spots an entry that sets nothing', () => {
        expect(isBlankAIFeature(fullAIFeature({ ID: 'x' }))).toBe(true);
        expect(isBlankAIFeature(fullAIFeature({ ID: 'x', Enabled: true }))).toBe(false);
        expect(isBlankAIFeature(fullAIFeature({ ID: 'x', Model: 'openai/gpt-5' }))).toBe(false);
    });
});

describe('groupProblems', () => {
    test('sorts problems out to the part of the editor that shows them', () => {
        const grouped = groupProblems([
            { workflow: -1, field: 'Repos', message: 'bad' },
            { workflow: 0, field: 'Name', message: 'is required' },
            { workflow: 0, field: 'SectionTitle', message: 'is required' },
            { workflow: -1, field: 'Plugins[1].Model', message: 'needs a model' },
            { workflow: -1, field: 'AI.DefaultCommand', message: 'unterminated quote' },
            { workflow: -1, field: 'AIFeatures[2].Mode', message: 'unknown mode' },
            { workflow: -1, field: 'ExperimentalLLMModel', message: 'needs a model' },
        ]);
        expect(grouped.global.map(p => p.field)).toEqual(['Repos', 'ExperimentalLLMModel']);
        expect(grouped.workflows.get(0)).toHaveLength(2);
        expect(grouped.workflows.get(1)).toBeUndefined();
        expect(grouped.plugins.get(1)).toEqual([
            { workflow: -1, field: 'Model', message: 'needs a model' },
        ]);
        expect(grouped.ai).toEqual([
            { workflow: -1, field: 'DefaultCommand', message: 'unterminated quote' },
        ]);
        expect(grouped.aiFeatures.get(2)?.[0].field).toBe('Mode');
    });
});
