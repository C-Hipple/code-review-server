import { expect, test, describe } from 'bun:test';
import {
    cleanDraft,
    cleanList,
    draftFromConfig,
    emptyWorkflow,
    isValidRepo,
    joinFilter,
    joinList,
    problemsByWorkflow,
    splitFilter,
    splitList,
    validateDraft,
} from './config_utils';
import type { ConfigDraft, FilterInfo, ServerConfig, WorkflowTypeInfo } from './config_utils';

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
    ...overrides,
});

const validate = (draft: ConfigDraft) => validateDraft(draft, workflowTypes, filters);

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
            ExperimentalLLMFileOrdering: false,
            ExperimentalLLMReviewEase: false,
        } as ServerConfig;

        const draft = draftFromConfig(config);
        draft.Workflows[0].Name = 'B';
        draft.Repos.push('other/repo');

        expect(config.Workflows[0].Name).toBe('A');
        expect(config.Repos).toEqual(['owner/repo']);
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

describe('problemsByWorkflow', () => {
    test('groups problems by their workflow index', () => {
        const grouped = problemsByWorkflow([
            { workflow: -1, field: 'Repos', message: 'bad' },
            { workflow: 0, field: 'Name', message: 'is required' },
            { workflow: 0, field: 'SectionTitle', message: 'is required' },
        ]);
        expect(grouped.get(-1)).toHaveLength(1);
        expect(grouped.get(0)).toHaveLength(2);
        expect(grouped.get(1)).toBeUndefined();
    });
});
