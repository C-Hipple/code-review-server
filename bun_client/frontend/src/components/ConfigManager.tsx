import { useCallback, useEffect, useMemo, useState } from 'react';
import type { MouseEvent, ReactNode } from 'react';
import { getConfig, updateConfig } from '../api';
import {
    cleanDraft,
    draftFromConfig,
    emptyPlugin,
    emptyWorkflow,
    groupProblems,
    isBlankAIFeature,
    joinFilter,
    joinList,
    resolveAIProvider,
    seedAIFeature,
    splitFilter,
    splitList,
    validateDraft,
    AI_DEFAULT_PROVIDER_OPTIONS,
    AI_FEATURE_PROVIDER_OPTIONS,
    AI_MODE_LABELS,
    AI_PROVIDER_COMMAND,
    AI_PROVIDER_GEMINI,
    AI_PROVIDER_OPENROUTER,
    NOTIFICATION_OPTIONS,
    PLUGIN_PROVIDER_OPTIONS,
    PR_STATE_OPTIONS,
} from '../config_utils';
import type {
    AIFeatureEntry,
    AIFeatureTypeInfo,
    AISettings,
    ConfigDraft,
    ConfigValidationError,
    FilterInfo,
    PluginEntry,
    WorkflowEntry,
    WorkflowTypeInfo,
} from '../config_utils';
import {
    Button,
    Input,
    Select,
    TextArea,
    colors,
    spacing,
    borderRadius,
    fontSize,
} from '../design';

/**
 * Editor for the server's TOML configuration (~/.config/codereviewserver.toml):
 * the global settings, the workflows, the plugins, and the AI features.
 *
 * The workflow type, filter and AI feature pickers are built from the
 * registries the server sends with the config, so they always offer what this
 * server can actually run. Validation happens twice: here for immediate
 * feedback, and again on the server, which refuses to write a config it can't
 * run.
 */
export default function ConfigManager() {
    const [draft, setDraft] = useState<ConfigDraft | null>(null);
    const [saved, setSaved] = useState<ConfigDraft | null>(null);
    const [workflowTypes, setWorkflowTypes] = useState<WorkflowTypeInfo[]>([]);
    const [filters, setFilters] = useState<FilterInfo[]>([]);
    const [aiFeatureTypes, setAIFeatureTypes] = useState<AIFeatureTypeInfo[]>([]);
    const [path, setPath] = useState('');
    // No config file yet: what's shown is the server's built-in default, and
    // saving is what creates the file.
    const [usingDefaults, setUsingDefaults] = useState(false);
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [loadError, setLoadError] = useState('');
    const [serverProblems, setServerProblems] = useState<ConfigValidationError[]>([]);
    const [status, setStatus] = useState<{ kind: 'ok' | 'error'; text: string } | null>(null);
    // Validation messages stay hidden until the first save attempt, so a
    // half-filled new workflow isn't shouting at the user while they type.
    const [showProblems, setShowProblems] = useState(false);
    const [expanded, setExpanded] = useState<number | null>(null);
    const [expandedPlugin, setExpandedPlugin] = useState<number | null>(null);
    // AI feature cards are keyed by feature ID; an entry naming no registered
    // feature by `#<index>`.
    const [expandedAI, setExpandedAI] = useState<string | null>(null);

    const load = useCallback(async () => {
        setLoading(true);
        setLoadError('');
        try {
            const reply = await getConfig();
            const loaded = draftFromConfig(reply.config);
            setDraft(loaded);
            setSaved(loaded);
            setWorkflowTypes(reply.workflow_types ?? []);
            setFilters(reply.filters ?? []);
            setAIFeatureTypes(reply.ai_features ?? []);
            setPath(reply.path);
            setUsingDefaults(reply.using_defaults ?? false);
            setServerProblems([]);
            setShowProblems(false);
            setStatus(reply.okay ? null : { kind: 'error', text: reply.message });
        } catch (e: unknown) {
            setLoadError(errorMessage(e));
        } finally {
            setLoading(false);
        }
    }, []);

    useEffect(() => {
        load();
    }, [load]);

    const clientProblems = useMemo(
        () => (draft ? validateDraft(draft, workflowTypes, filters, aiFeatureTypes) : []),
        [draft, workflowTypes, filters, aiFeatureTypes]
    );
    // Server problems are cleared as soon as the draft changes, so a stale
    // rejection doesn't linger next to a field the user already fixed.
    const problems = clientProblems.length > 0 ? clientProblems : serverProblems;
    const grouped = useMemo(() => groupProblems(problems), [problems]);
    const dirty = useMemo(
        () => !!draft && !!saved && JSON.stringify(draft) !== JSON.stringify(saved),
        [draft, saved]
    );

    const updateDraft = (change: Partial<ConfigDraft>) => {
        setDraft(current => (current ? { ...current, ...change } : current));
        setServerProblems([]);
        setStatus(null);
    };

    const updateWorkflow = (index: number, change: Partial<WorkflowEntry>) => {
        setDraft(current => {
            if (!current) return current;
            const workflows = current.Workflows.map((workflow, i) =>
                i === index ? { ...workflow, ...change } : workflow
            );
            return { ...current, Workflows: workflows };
        });
        setServerProblems([]);
        setStatus(null);
    };

    const addWorkflow = () => {
        if (!draft) return;
        const next = [...draft.Workflows, emptyWorkflow(workflowTypes)];
        updateDraft({ Workflows: next });
        setExpanded(next.length - 1);
    };

    const removeWorkflow = (index: number) => {
        if (!draft) return;
        const workflow = draft.Workflows[index];
        const label = workflow.Name?.trim() || 'this workflow';
        if (!window.confirm(`Remove ${label}? Its PRs drop out of the list on the next sync.`)) {
            return;
        }
        updateDraft({ Workflows: draft.Workflows.filter((_, i) => i !== index) });
        setExpanded(null);
    };

    const moveWorkflow = (index: number, delta: number) => {
        if (!draft) return;
        const target = index + delta;
        if (target < 0 || target >= draft.Workflows.length) return;
        const workflows = [...draft.Workflows];
        [workflows[index], workflows[target]] = [workflows[target], workflows[index]];
        updateDraft({ Workflows: workflows });
        setExpanded(target);
    };

    const updatePlugin = (index: number, change: Partial<PluginEntry>) => {
        setDraft(current => {
            if (!current) return current;
            const plugins = current.Plugins.map((plugin, i) =>
                i === index ? { ...plugin, ...change } : plugin
            );
            return { ...current, Plugins: plugins };
        });
        setServerProblems([]);
        setStatus(null);
    };

    const addPlugin = () => {
        if (!draft) return;
        const next = [...draft.Plugins, emptyPlugin()];
        updateDraft({ Plugins: next });
        setExpandedPlugin(next.length - 1);
    };

    const removePlugin = (index: number) => {
        if (!draft) return;
        const label = draft.Plugins[index].Name?.trim() || 'this plugin';
        if (!window.confirm(`Remove ${label}? It stops running on PRs once saved.`)) {
            return;
        }
        updateDraft({ Plugins: draft.Plugins.filter((_, i) => i !== index) });
        setExpandedPlugin(null);
    };

    const movePlugin = (index: number, delta: number) => {
        if (!draft) return;
        const target = index + delta;
        if (target < 0 || target >= draft.Plugins.length) return;
        const plugins = [...draft.Plugins];
        [plugins[index], plugins[target]] = [plugins[target], plugins[index]];
        updateDraft({ Plugins: plugins });
        setExpandedPlugin(target);
    };

    const updateAISettings = (change: Partial<AISettings>) => {
        if (!draft) return;
        updateDraft({ AI: { ...draft.AI, ...change } });
    };

    /**
     * Changes a registered feature's [[AIFeatures]] entry, creating it on the
     * first change. An entry the editor created that ends up setting nothing
     * is dropped again, so switching a feature on and back off leaves the
     * config as it was.
     */
    const updateAIFeature = (
        id: string,
        info: AIFeatureTypeInfo | undefined,
        change: Partial<AIFeatureEntry>
    ) => {
        const fromFile = new Set((saved?.AIFeatures ?? []).map(entry => entry.ID));
        setDraft(current => {
            if (!current) return current;
            const index = current.AIFeatures.findIndex(entry => entry.ID === id);
            const base = index >= 0 ? current.AIFeatures[index] : seedAIFeature(info, id);
            const next = { ...base, ...change };
            let features: AIFeatureEntry[];
            if (isBlankAIFeature(next) && !info?.legacy && !fromFile.has(id)) {
                features = current.AIFeatures.filter(entry => entry.ID !== id);
            } else if (index >= 0) {
                features = current.AIFeatures.map((entry, i) => (i === index ? next : entry));
            } else {
                features = [...current.AIFeatures, next];
            }
            return { ...current, AIFeatures: features };
        });
        setServerProblems([]);
        setStatus(null);
    };

    const updateAIFeatureAt = (index: number, change: Partial<AIFeatureEntry>) => {
        if (!draft) return;
        updateDraft({
            AIFeatures: draft.AIFeatures.map((entry, i) =>
                i === index ? { ...entry, ...change } : entry
            ),
        });
    };

    const removeAIFeatureAt = (index: number) => {
        if (!draft) return;
        updateDraft({ AIFeatures: draft.AIFeatures.filter((_, i) => i !== index) });
    };

    const save = async () => {
        if (!draft) return;
        setShowProblems(true);
        if (clientProblems.length > 0) {
            setStatus({
                kind: 'error',
                text: `Fix ${clientProblems.length} problem${clientProblems.length === 1 ? '' : 's'} before saving.`,
            });
            return;
        }

        setSaving(true);
        setStatus(null);
        try {
            const payload = cleanDraft(draft);
            const reply = await updateConfig({
                Repos: payload.Repos,
                SleepDuration: payload.SleepDuration,
                GithubUsername: payload.GithubUsername,
                RepoLocation: payload.RepoLocation,
                JiraDomain: payload.JiraDomain,
                AutoWorktree: payload.AutoWorktree,
                DesktopNotifications: payload.DesktopNotifications,
                Workflows: payload.Workflows,
                Plugins: payload.Plugins,
                AI: payload.AI,
                AIFeatures: payload.AIFeatures,
            });
            if (!reply.okay) {
                setServerProblems(reply.errors ?? []);
                setStatus({
                    kind: 'error',
                    text: reply.message || 'The server rejected the configuration.',
                });
                return;
            }
            const applied = draftFromConfig(reply.config);
            setDraft(applied);
            setSaved(applied);
            setServerProblems([]);
            if (reply.ai_features) setAIFeatureTypes(reply.ai_features);
            setPath(reply.path);
            setUsingDefaults(reply.using_defaults ?? false);
            setStatus({
                kind: 'ok',
                text: `${reply.message}. Workflow changes take effect on the next sync; plugin and AI feature changes apply from their next run.`,
            });
        } catch (e: unknown) {
            setStatus({ kind: 'error', text: errorMessage(e) });
        } finally {
            setSaving(false);
        }
    };

    if (loading) {
        return <div style={{ color: colors.textSecondary }}>Loading configuration…</div>;
    }
    if (loadError || !draft) {
        return (
            <div style={{ display: 'flex', flexDirection: 'column', gap: spacing.md }}>
                <div style={{ color: colors.textDanger }}>
                    Could not load the configuration: {loadError}
                </div>
                <div>
                    <Button variant="secondary" size="sm" onClick={load}>
                        Retry
                    </Button>
                </div>
            </div>
        );
    }

    const globalProblems = showProblems ? grouped.global : [];

    return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: spacing.xl }}>
            {usingDefaults ? (
                <div style={{ fontSize: fontSize.sm, color: colors.textSecondary }}>
                    No config file yet — these are the server&apos;s built-in defaults, which need
                    nothing but a GitHub token. Saving writes them, along with your changes, to{' '}
                    <code>{path}</code>.
                </div>
            ) : (
                <div style={{ fontSize: fontSize.sm, color: colors.textTertiary }}>
                    Editing <code>{path}</code>. The previous file is kept alongside it as{' '}
                    <code>.bak</code>; comments in the file are not preserved.
                </div>
            )}

            <section style={{ display: 'flex', flexDirection: 'column', gap: spacing.md }}>
                <SectionHeading>Global Settings</SectionHeading>
                <TextArea
                    label="Repositories (owner/repo, one per line)"
                    rows={3}
                    value={joinList(draft.Repos, '\n')}
                    onChange={e => updateDraft({ Repos: splitList(e.target.value, '\n') })}
                    placeholder="C-Hipple/code-review-server"
                />
                <div style={{ display: 'flex', gap: spacing.md, flexWrap: 'wrap' }}>
                    <div style={{ flex: '1 1 180px' }}>
                        <Input
                            label="GitHub username"
                            value={draft.GithubUsername}
                            onChange={e => updateDraft({ GithubUsername: e.target.value })}
                        />
                    </div>
                    <div style={{ flex: '1 1 180px' }}>
                        <Input
                            label="Sync interval (minutes)"
                            type="number"
                            min={1}
                            max={1440}
                            value={String(draft.SleepDuration)}
                            onChange={e =>
                                updateDraft({ SleepDuration: parseInt(e.target.value, 10) || 0 })
                            }
                        />
                    </div>
                </div>
                <div style={{ display: 'flex', gap: spacing.md, flexWrap: 'wrap' }}>
                    <div style={{ flex: '1 1 180px' }}>
                        <Input
                            label="Local repository location"
                            value={draft.RepoLocation}
                            onChange={e => updateDraft({ RepoLocation: e.target.value })}
                            placeholder="~/"
                        />
                    </div>
                    <div style={{ flex: '1 1 180px' }}>
                        <Input
                            label="Jira domain (optional)"
                            value={draft.JiraDomain}
                            onChange={e => updateDraft({ JiraDomain: e.target.value })}
                            placeholder="your-company.atlassian.net"
                        />
                    </div>
                </div>
                <div style={{ display: 'flex', gap: spacing.xl, flexWrap: 'wrap' }}>
                    <Checkbox
                        label="Desktop notifications"
                        checked={draft.DesktopNotifications}
                        onChange={value => updateDraft({ DesktopNotifications: value })}
                    />
                    <Checkbox
                        label="Manage git worktrees"
                        checked={draft.AutoWorktree}
                        onChange={value => updateDraft({ AutoWorktree: value })}
                    />
                </div>
                <ProblemList problems={globalProblems} />
            </section>

            <section style={{ display: 'flex', flexDirection: 'column', gap: spacing.md }}>
                <div
                    style={{
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'space-between',
                        gap: spacing.md,
                    }}
                >
                    <SectionHeading>Workflows ({draft.Workflows.length})</SectionHeading>
                    <Button variant="secondary" size="sm" onClick={addWorkflow}>
                        + Add workflow
                    </Button>
                </div>

                {draft.Workflows.length === 0 && (
                    <div style={{ fontSize: fontSize.sm, color: colors.textTertiary }}>
                        No workflows configured — nothing will be synced from GitHub.
                    </div>
                )}

                {draft.Workflows.map((workflow, index) => (
                    <WorkflowCard
                        key={index}
                        workflow={workflow}
                        workflowTypes={workflowTypes}
                        filters={filters}
                        expanded={expanded === index}
                        problems={showProblems ? (grouped.workflows.get(index) ?? []) : []}
                        onToggle={() => setExpanded(expanded === index ? null : index)}
                        onChange={change => updateWorkflow(index, change)}
                        onRemove={() => removeWorkflow(index)}
                        onMove={delta => moveWorkflow(index, delta)}
                        canMoveUp={index > 0}
                        canMoveDown={index < draft.Workflows.length - 1}
                    />
                ))}
            </section>

            <section style={{ display: 'flex', flexDirection: 'column', gap: spacing.md }}>
                <div
                    style={{
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'space-between',
                        gap: spacing.md,
                    }}
                >
                    <SectionHeading>Plugins ({draft.Plugins.length})</SectionHeading>
                    <Button variant="secondary" size="sm" onClick={addPlugin}>
                        + Add plugin
                    </Button>
                </div>
                <Hint>
                    Programs on the server&apos;s <code>$PATH</code> run for each PR, once per head
                    commit, with the PR data you choose passed as flags. Their output shows in the
                    review&apos;s plugin panel.
                </Hint>

                {draft.Plugins.length === 0 && <Hint>No plugins configured.</Hint>}

                {draft.Plugins.map((plugin, index) => (
                    <PluginCard
                        key={index}
                        plugin={plugin}
                        expanded={expandedPlugin === index}
                        problems={showProblems ? (grouped.plugins.get(index) ?? []) : []}
                        onToggle={() => setExpandedPlugin(expandedPlugin === index ? null : index)}
                        onChange={change => updatePlugin(index, change)}
                        onRemove={() => removePlugin(index)}
                        onMove={delta => movePlugin(index, delta)}
                        canMoveUp={index > 0}
                        canMoveDown={index < draft.Plugins.length - 1}
                    />
                ))}
            </section>

            <section style={{ display: 'flex', flexDirection: 'column', gap: spacing.md }}>
                <SectionHeading>AI Features</SectionHeading>
                <Hint>
                    Each feature is off until it is enabled here. A feature reaches a model through
                    a provider: the Gemini API (needs <code>GEMINI_API_KEY</code>), OpenRouter
                    (needs <code>OPENROUTER_API_KEY</code> and a model), or a command on this
                    machine such as <code>claude -p</code>, which reads the prompt on stdin and
                    prints its answer.
                </Hint>

                <AIDefaultsEditor
                    settings={draft.AI}
                    problems={showProblems ? grouped.ai : []}
                    onChange={updateAISettings}
                />

                {aiFeatureTypes.map(info => {
                    const index = draft.AIFeatures.findIndex(entry => entry.ID === info.id);
                    return (
                        <AIFeatureCard
                            key={info.id}
                            info={info}
                            entry={index >= 0 ? draft.AIFeatures[index] : undefined}
                            defaults={draft.AI}
                            expanded={expandedAI === info.id}
                            problems={
                                showProblems && index >= 0
                                    ? (grouped.aiFeatures.get(index) ?? [])
                                    : []
                            }
                            onToggle={() => setExpandedAI(expandedAI === info.id ? null : info.id)}
                            onChange={change => updateAIFeature(info.id, info, change)}
                            onRemove={index >= 0 ? () => removeAIFeatureAt(index) : undefined}
                        />
                    );
                })}

                {draft.AIFeatures.map((entry, index) => {
                    if (aiFeatureTypes.some(info => info.id === entry.ID)) return null;
                    const key = `#${index}`;
                    return (
                        <AIFeatureCard
                            key={key}
                            entry={entry}
                            unknown={aiFeatureTypes.length > 0}
                            defaults={draft.AI}
                            expanded={expandedAI === key}
                            problems={showProblems ? (grouped.aiFeatures.get(index) ?? []) : []}
                            onToggle={() => setExpandedAI(expandedAI === key ? null : key)}
                            onChange={change => updateAIFeatureAt(index, change)}
                            onRemove={() => removeAIFeatureAt(index)}
                        />
                    );
                })}

                {aiFeatureTypes.length === 0 && draft.AIFeatures.length === 0 && (
                    <Hint>This server lists no AI features.</Hint>
                )}
            </section>

            {status && (
                <div
                    style={{
                        fontSize: fontSize.sm,
                        color: status.kind === 'ok' ? colors.textSuccess : colors.textDanger,
                        background: status.kind === 'ok' ? colors.bgSuccessDim : colors.bgDangerDim,
                        border: `1px solid ${
                            status.kind === 'ok' ? colors.borderSuccessDim : colors.borderDangerDim
                        }`,
                        borderRadius: borderRadius.md,
                        padding: spacing.md,
                    }}
                >
                    {status.text}
                </div>
            )}

            <div
                style={{
                    display: 'flex',
                    gap: spacing.md,
                    alignItems: 'center',
                    justifyContent: 'flex-end',
                }}
            >
                {dirty && (
                    <span style={{ fontSize: fontSize.sm, color: colors.textTertiary }}>
                        Unsaved changes
                    </span>
                )}
                <Button variant="secondary" size="sm" onClick={load} disabled={saving}>
                    Reload
                </Button>
                <Button size="sm" onClick={save} loading={saving} disabled={saving || !dirty}>
                    Save configuration
                </Button>
            </div>
        </div>
    );
}

/** One workflow entry, collapsed to a summary row until it's opened. */
function WorkflowCard({
    workflow,
    workflowTypes,
    filters,
    expanded,
    problems,
    onToggle,
    onChange,
    onRemove,
    onMove,
    canMoveUp,
    canMoveDown,
}: {
    workflow: WorkflowEntry;
    workflowTypes: WorkflowTypeInfo[];
    filters: FilterInfo[];
    expanded: boolean;
    problems: ConfigValidationError[];
    onToggle: () => void;
    onChange: (change: Partial<WorkflowEntry>) => void;
    onRemove: () => void;
    onMove: (delta: number) => void;
    canMoveUp: boolean;
    canMoveDown: boolean;
}) {
    const typeInfo = workflowTypes.find(t => t.name === workflow.WorkflowType);
    const shows = (field: string) =>
        !typeInfo ||
        typeInfo.required_fields.includes(field) ||
        typeInfo.optional_fields.includes(field);

    return (
        <ListCard
            title={workflow.Name?.trim() || <em>Unnamed workflow</em>}
            subtitle={`${workflow.WorkflowType} → ${workflow.SectionTitle?.trim() || 'no section'}`}
            expanded={expanded}
            problemCount={problems.length}
            onToggle={onToggle}
            actions={
                <ReorderRemoveButtons
                    noun="workflow"
                    onMove={onMove}
                    onRemove={onRemove}
                    canMoveUp={canMoveUp}
                    canMoveDown={canMoveDown}
                />
            }
        >
            <div style={{ display: 'flex', gap: spacing.md, flexWrap: 'wrap' }}>
                <div style={{ flex: '1 1 200px' }}>
                    <Input
                        label="Name"
                        value={workflow.Name ?? ''}
                        onChange={e => onChange({ Name: e.target.value })}
                        placeholder="My Open PRs"
                    />
                </div>
                <div style={{ flex: '1 1 200px' }}>
                    <Input
                        label="Section title"
                        value={workflow.SectionTitle ?? ''}
                        onChange={e => onChange({ SectionTitle: e.target.value })}
                        placeholder="Needs My Review"
                    />
                </div>
            </div>

            <div>
                <Select
                    label="Workflow type"
                    value={workflow.WorkflowType}
                    onChange={e => onChange({ WorkflowType: e.target.value })}
                    style={{ width: '100%' }}
                    options={workflowTypes.map(type => ({
                        value: type.name,
                        label: type.deprecated ? `${type.name} (deprecated)` : type.name,
                    }))}
                />
                {typeInfo && (
                    <div
                        style={{
                            fontSize: fontSize.sm,
                            color: colors.textTertiary,
                            marginTop: spacing.xs,
                        }}
                    >
                        {typeInfo.description}
                        {typeInfo.deprecated && typeInfo.deprecated_by && (
                            <> Use {typeInfo.deprecated_by} instead.</>
                        )}
                    </div>
                )}
            </div>

            {shows('Repo') && (
                <Input
                    label="Repository (owner/repo)"
                    value={workflow.Repo ?? ''}
                    onChange={e => onChange({ Repo: e.target.value })}
                    placeholder="C-Hipple/code-review-server"
                />
            )}

            {shows('Repos') && (
                <TextArea
                    label="Repositories (one per line, blank inherits the global list)"
                    rows={2}
                    value={joinList(workflow.Repos, '\n')}
                    onChange={e => onChange({ Repos: splitList(e.target.value, '\n') })}
                    placeholder="owner/repo"
                />
            )}

            {shows('JiraEpic') && (
                <Input
                    label="Jira epic key"
                    value={workflow.JiraEpic ?? ''}
                    onChange={e => onChange({ JiraEpic: e.target.value })}
                    placeholder="BOARD-123"
                />
            )}

            {shows('Filters') && (
                <FilterEditor
                    filters={filters}
                    selected={workflow.Filters ?? []}
                    onChange={next => onChange({ Filters: next })}
                />
            )}

            {shows('Teams') && (
                <Input
                    label="Teams (slugs, comma separated)"
                    value={joinList(workflow.Teams, ',')}
                    onChange={e => onChange({ Teams: splitList(e.target.value, ',') })}
                    placeholder="growth-pod-review,backend-team"
                />
            )}

            <div style={{ display: 'flex', gap: spacing.md, flexWrap: 'wrap' }}>
                {shows('PRState') && (
                    <div style={{ flex: '1 1 180px' }}>
                        <Select
                            label="PR state"
                            value={workflow.PRState ?? ''}
                            onChange={e => onChange({ PRState: e.target.value })}
                            style={{ width: '100%' }}
                            options={PR_STATE_OPTIONS}
                        />
                    </div>
                )}
                {shows('DesktopNotifications') && (
                    <div style={{ flex: '1 1 180px' }}>
                        <Select
                            label="Desktop notifications"
                            value={notificationValue(workflow.DesktopNotifications)}
                            onChange={e =>
                                onChange({
                                    DesktopNotifications: notificationSetting(e.target.value),
                                })
                            }
                            style={{ width: '100%' }}
                            options={NOTIFICATION_OPTIONS}
                        />
                    </div>
                )}
            </div>

            {shows('IncludeDiff') && (
                <Checkbox
                    label="Include the full diff in the section body"
                    checked={!!workflow.IncludeDiff}
                    onChange={value => onChange({ IncludeDiff: value })}
                />
            )}

            <ProblemList problems={problems} />
        </ListCard>
    );
}

/** One [[Plugins]] entry, collapsed to a summary row until it's opened. */
function PluginCard({
    plugin,
    expanded,
    problems,
    onToggle,
    onChange,
    onRemove,
    onMove,
    canMoveUp,
    canMoveDown,
}: {
    plugin: PluginEntry;
    expanded: boolean;
    problems: ConfigValidationError[];
    onToggle: () => void;
    onChange: (change: Partial<PluginEntry>) => void;
    onRemove: () => void;
    onMove: (delta: number) => void;
    canMoveUp: boolean;
    canMoveDown: boolean;
}) {
    const summary = [
        plugin.Command?.trim() || 'no command',
        plugin.OnlyOnDemand ? 'on demand' : 'automatic',
        plugin.Provider?.trim()
            ? [plugin.Provider.trim(), plugin.Model?.trim()].filter(Boolean).join(': ')
            : '',
    ]
        .filter(Boolean)
        .join(' · ');

    return (
        <ListCard
            title={plugin.Name?.trim() || <em>Unnamed plugin</em>}
            subtitle={summary}
            expanded={expanded}
            problemCount={problems.length}
            onToggle={onToggle}
            actions={
                <ReorderRemoveButtons
                    noun="plugin"
                    onMove={onMove}
                    onRemove={onRemove}
                    canMoveUp={canMoveUp}
                    canMoveDown={canMoveDown}
                />
            }
        >
            <div style={{ display: 'flex', gap: spacing.md, flexWrap: 'wrap' }}>
                <div style={{ flex: '1 1 200px' }}>
                    <Input
                        label="Name"
                        value={plugin.Name ?? ''}
                        onChange={e => onChange({ Name: e.target.value })}
                        placeholder="Summarize Diff"
                    />
                </div>
                <div style={{ flex: '1 1 200px' }}>
                    <Input
                        label="Command (on the server's $PATH)"
                        value={plugin.Command ?? ''}
                        onChange={e => onChange({ Command: e.target.value })}
                        placeholder="summarize_diff"
                    />
                </div>
            </div>

            <div style={{ display: 'flex', flexDirection: 'column', gap: spacing.xs }}>
                <FieldLabel>Passes the plugin</FieldLabel>
                <div style={{ display: 'flex', gap: spacing.lg, flexWrap: 'wrap' }}>
                    <Checkbox
                        label="Diff (--diff)"
                        checked={!!plugin.IncludeDiff}
                        onChange={value => onChange({ IncludeDiff: value })}
                    />
                    <Checkbox
                        label="PR metadata (--headers)"
                        checked={!!plugin.IncludeHeaders}
                        onChange={value => onChange({ IncludeHeaders: value })}
                    />
                    <Checkbox
                        label="Comments (--comments)"
                        checked={!!plugin.IncludeComments}
                        onChange={value => onChange({ IncludeComments: value })}
                    />
                    <Checkbox
                        label="Head branch (--branch)"
                        checked={!!plugin.IncludeBranch}
                        onChange={value => onChange({ IncludeBranch: value })}
                    />
                </div>
            </div>

            <div>
                <Checkbox
                    label="Only on demand"
                    checked={!!plugin.OnlyOnDemand}
                    onChange={value => onChange({ OnlyOnDemand: value })}
                />
                <Hint>
                    An on-demand plugin never runs by itself; run it from the review&apos;s plugin
                    panel. Worth it for plugins that cost money per run.
                </Hint>
            </div>

            <div style={{ display: 'flex', gap: spacing.md, flexWrap: 'wrap' }}>
                <div style={{ flex: '1 1 200px' }}>
                    <Select
                        label="LLM provider"
                        value={plugin.Provider ?? ''}
                        onChange={e => onChange({ Provider: e.target.value })}
                        style={{ width: '100%' }}
                        options={withCurrent(PLUGIN_PROVIDER_OPTIONS, plugin.Provider)}
                    />
                </div>
                <div style={{ flex: '1 1 200px' }}>
                    <Input
                        label={
                            plugin.Provider === AI_PROVIDER_OPENROUTER
                                ? 'Model (required for OpenRouter)'
                                : 'Model (OpenRouter)'
                        }
                        value={plugin.Model ?? ''}
                        onChange={e => onChange({ Model: e.target.value })}
                        placeholder="anthropic/claude-sonnet-4.5"
                    />
                </div>
            </div>
            <Hint>
                Reaches the plugin as <code>CRS_LLM_PROVIDER</code> and <code>CRS_LLM_MODEL</code>.
                The bundled plugins (summarize_diff, security_check, style_guidelines) honor them; a
                plugin that calls no model ignores them. style_guidelines needs a model that
                supports tool calling.
            </Hint>

            <ProblemList problems={problems} />
        </ListCard>
    );
}

/** The [AI] table: the provider, command and model every feature inherits. */
function AIDefaultsEditor({
    settings,
    problems,
    onChange,
}: {
    settings: Required<AISettings>;
    problems: ConfigValidationError[];
    onChange: (change: Partial<AISettings>) => void;
}) {
    return (
        <div
            style={{
                border: `1px solid ${problems.length > 0 ? colors.borderDangerDim : colors.border}`,
                borderRadius: borderRadius.md,
                background: colors.bgPrimary,
                padding: spacing.md,
                display: 'flex',
                flexDirection: 'column',
                gap: spacing.md,
            }}
        >
            <div style={{ fontWeight: 500 }}>Defaults for every feature</div>
            <Select
                label="Default provider"
                value={settings.DefaultProvider}
                onChange={e => onChange({ DefaultProvider: e.target.value })}
                style={{ width: '100%' }}
                options={withCurrent(AI_DEFAULT_PROVIDER_OPTIONS, settings.DefaultProvider)}
            />
            <div style={{ display: 'flex', gap: spacing.md, flexWrap: 'wrap' }}>
                <div style={{ flex: '1 1 200px' }}>
                    <Input
                        label="Default command"
                        value={settings.DefaultCommand}
                        onChange={e => onChange({ DefaultCommand: e.target.value })}
                        placeholder="claude -p"
                    />
                </div>
                <div style={{ flex: '1 1 200px' }}>
                    <Input
                        label="Default model (OpenRouter)"
                        value={settings.DefaultModel}
                        onChange={e => onChange({ DefaultModel: e.target.value })}
                        placeholder="anthropic/claude-sonnet-4.5"
                    />
                </div>
            </div>
            <Hint>
                A feature&apos;s own settings beat these. At each level a named provider beats the
                one a command implies, and with nothing set a feature runs on Gemini. The command is
                split into words like a shell would (quotes work, pipes and variables don&apos;t).
                Only OpenRouter reads the model; Gemini stays on gemini-flash-latest.
            </Hint>
            <ProblemList problems={problems} />
        </div>
    );
}

/**
 * One AI feature: a registered one (`info`), whether or not the config has an
 * entry for it, or an entry naming no registered feature.
 */
function AIFeatureCard({
    info,
    entry,
    unknown = false,
    defaults,
    expanded,
    problems,
    onToggle,
    onChange,
    onRemove,
}: {
    info?: AIFeatureTypeInfo;
    entry?: AIFeatureEntry;
    /** The entry names a feature this server doesn't have. */
    unknown?: boolean;
    defaults: Required<AISettings>;
    expanded: boolean;
    problems: ConfigValidationError[];
    onToggle: () => void;
    onChange: (change: Partial<AIFeatureEntry>) => void;
    /** Drops the config's entry; absent when there is none. */
    onRemove?: () => void;
}) {
    // What the server goes by: the config's own entry, else the one a legacy
    // root-level key stands for.
    const effective = entry ?? info?.legacy;
    const enabled = !!effective?.Enabled;
    const choice = resolveAIProvider(defaults, effective ?? { ID: info?.id ?? '' });
    const modes = info?.modes ?? [];
    const id = info?.id ?? entry?.ID ?? '';

    const summary = enabled
        ? [
              effective?.Automatic ? 'automatic' : 'on request',
              effective?.Mode?.trim() || (modes.length > 1 ? modes[0] : ''),
              `runs on ${describeProvider(choice)}`,
          ]
              .filter(Boolean)
              .join(' · ')
        : 'off';

    return (
        <ListCard
            title={
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: spacing.sm }}>
                    <input
                        type="checkbox"
                        aria-label={`Enable ${info?.name ?? id}`}
                        checked={enabled}
                        onClick={e => e.stopPropagation()}
                        onChange={e => onChange({ Enabled: e.target.checked })}
                        style={{ cursor: 'pointer' }}
                    />
                    <span>{info?.name ?? (id || <em>Entry without an ID</em>)}</span>
                    {info?.applied && (
                        <span style={{ fontSize: fontSize.sm, color: colors.textTertiary }}>
                            applied
                        </span>
                    )}
                </span>
            }
            subtitle={`${id}${unknown ? ' (unknown feature)' : ''} · ${summary}`}
            expanded={expanded}
            problemCount={problems.length}
            onToggle={onToggle}
        >
            {info?.description && <Hint>{info.description}</Hint>}
            {info?.applied && (
                <Hint>
                    An applied feature has no report to open: the server uses its result directly,
                    and asks for it whenever a PR is opened.
                </Hint>
            )}
            {unknown && (
                <Hint>
                    This server has no feature named <code>{id}</code>; remove the entry or fix its
                    ID in the config file.
                </Hint>
            )}
            {info?.legacy_key && !entry && (
                <Notice>
                    Switched on by the legacy <code>{info.legacy_key}</code> key in the config file.
                    Changing anything here writes an <code>[[AIFeatures]]</code> entry, which takes
                    precedence over that key.
                </Notice>
            )}
            {info?.legacy_key && entry && (
                <Hint>
                    This entry takes precedence over the legacy <code>{info.legacy_key}</code> key;
                    removing it falls back to that key.
                </Hint>
            )}

            <div style={{ display: 'flex', gap: spacing.lg, flexWrap: 'wrap' }}>
                <Checkbox
                    label="Enabled"
                    checked={enabled}
                    onChange={value => onChange({ Enabled: value })}
                />
                <Checkbox
                    label="Run automatically when a PR is fetched or updated"
                    checked={!!effective?.Automatic}
                    disabled={!enabled}
                    onChange={value => onChange({ Automatic: value })}
                />
            </div>

            {(modes.length > 1 || effective?.Mode) && (
                <Select
                    label="Mode"
                    value={effective?.Mode ?? ''}
                    onChange={e => onChange({ Mode: e.target.value })}
                    style={{ width: '100%' }}
                    options={withCurrent(
                        [
                            {
                                value: '',
                                label: modes[0]
                                    ? `Default: ${AI_MODE_LABELS[modes[0]] ?? modes[0]}`
                                    : 'Default',
                            },
                            ...modes.map(mode => ({
                                value: mode,
                                label: AI_MODE_LABELS[mode] ?? mode,
                            })),
                        ],
                        effective?.Mode
                    )}
                />
            )}

            <Select
                label="Provider"
                value={effective?.Provider ?? ''}
                onChange={e => onChange({ Provider: e.target.value })}
                style={{ width: '100%' }}
                options={withCurrent(AI_FEATURE_PROVIDER_OPTIONS, effective?.Provider)}
            />
            <div style={{ display: 'flex', gap: spacing.md, flexWrap: 'wrap' }}>
                <div style={{ flex: '1 1 200px' }}>
                    <Input
                        label="Command (overrides the default)"
                        value={effective?.Command ?? ''}
                        onChange={e => onChange({ Command: e.target.value })}
                        placeholder={defaults.DefaultCommand || 'claude -p'}
                    />
                </div>
                <div style={{ flex: '1 1 200px' }}>
                    <Input
                        label="Model (overrides the default)"
                        value={effective?.Model ?? ''}
                        onChange={e => onChange({ Model: e.target.value })}
                        placeholder={defaults.DefaultModel || 'anthropic/claude-sonnet-4.5'}
                    />
                </div>
            </div>
            <Hint>
                Runs on {describeProvider(choice)}.
                {effective?.Command?.trim() && !effective?.Provider?.trim() && (
                    <> Setting a command here picks the command provider.</>
                )}
            </Hint>

            {onRemove && (
                <div>
                    <Button variant="secondary" size="sm" onClick={onRemove}>
                        {info?.legacy_key
                            ? `Remove entry (fall back to ${info.legacy_key})`
                            : 'Remove entry'}
                    </Button>
                </div>
            )}

            <ProblemList problems={problems} />
        </ListCard>
    );
}

/** Says in a few words what a resolved provider choice runs. */
function describeProvider(choice: { provider: string; command: string; model: string }): string {
    switch (choice.provider) {
        case AI_PROVIDER_GEMINI:
            return 'the Gemini API';
        case AI_PROVIDER_OPENROUTER:
            return choice.model ? `OpenRouter (${choice.model})` : 'OpenRouter (no model set)';
        case AI_PROVIDER_COMMAND:
            return choice.command ? `the command "${choice.command}"` : 'a command (none set)';
        default:
            return `"${choice.provider}"`;
    }
}

/**
 * Keeps a value the options don't list selectable, so a hand-written config
 * naming something unexpected isn't silently changed by an unrelated edit.
 */
function withCurrent(
    options: Array<{ value: string; label: string }>,
    current: string | undefined
): Array<{ value: string; label: string }> {
    if (!current || options.some(option => option.value === current)) return options;
    return [...options, { value: current, label: `${current} (unknown)` }];
}

/** A collapsible card: a summary row that opens onto the editor beneath it. */
function ListCard({
    title,
    subtitle,
    expanded,
    problemCount,
    onToggle,
    actions,
    children,
}: {
    title: ReactNode;
    subtitle: ReactNode;
    expanded: boolean;
    problemCount: number;
    onToggle: () => void;
    actions?: ReactNode;
    children: ReactNode;
}) {
    return (
        <div
            style={{
                border: `1px solid ${problemCount > 0 ? colors.borderDangerDim : colors.border}`,
                borderRadius: borderRadius.md,
                background: colors.bgPrimary,
            }}
        >
            <div
                style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: spacing.sm,
                    padding: spacing.md,
                    cursor: 'pointer',
                }}
                onClick={onToggle}
            >
                <span style={{ color: colors.textSecondary, width: '12px' }}>
                    {expanded ? '▾' : '▸'}
                </span>
                <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ fontWeight: 500 }}>{title}</div>
                    <div
                        style={{
                            fontSize: fontSize.sm,
                            color: colors.textTertiary,
                            overflow: 'hidden',
                            textOverflow: 'ellipsis',
                            whiteSpace: 'nowrap',
                        }}
                    >
                        {subtitle}
                    </div>
                </div>
                {problemCount > 0 && (
                    <span style={{ color: colors.textDanger, fontSize: fontSize.sm }}>
                        {problemCount} issue{problemCount === 1 ? '' : 's'}
                    </span>
                )}
                {actions}
            </div>

            {expanded && (
                <div
                    style={{
                        borderTop: `1px solid ${colors.border}`,
                        padding: spacing.md,
                        display: 'flex',
                        flexDirection: 'column',
                        gap: spacing.md,
                    }}
                >
                    {children}
                </div>
            )}
        </div>
    );
}

/** The move up / move down / remove buttons of a reorderable card. */
function ReorderRemoveButtons({
    noun,
    onMove,
    onRemove,
    canMoveUp,
    canMoveDown,
}: {
    noun: string;
    onMove: (delta: number) => void;
    onRemove: () => void;
    canMoveUp: boolean;
    canMoveDown: boolean;
}) {
    return (
        <>
            <IconButton
                label="Move up"
                disabled={!canMoveUp}
                onClick={e => {
                    e.stopPropagation();
                    onMove(-1);
                }}
            >
                ↑
            </IconButton>
            <IconButton
                label="Move down"
                disabled={!canMoveDown}
                onClick={e => {
                    e.stopPropagation();
                    onMove(1);
                }}
            >
                ↓
            </IconButton>
            <IconButton
                label={`Remove ${noun}`}
                danger
                onClick={e => {
                    e.stopPropagation();
                    onRemove();
                }}
            >
                ×
            </IconButton>
        </>
    );
}

/** Picker for a workflow's filters, including the ones that take an argument. */
function FilterEditor({
    filters,
    selected,
    onChange,
}: {
    filters: FilterInfo[];
    selected: string[];
    onChange: (filters: string[]) => void;
}) {
    const infoFor = (name: string) => filters.find(f => f.name === name);

    const replace = (index: number, entry: string) => {
        onChange(selected.map((existing, i) => (i === index ? entry : existing)));
    };

    return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: spacing.xs }}>
            <label style={{ fontSize: fontSize.sm, color: colors.textSecondary, fontWeight: 500 }}>
                Filters
            </label>
            {selected.length === 0 && (
                <div style={{ fontSize: fontSize.sm, color: colors.textTertiary }}>
                    No filters — every pull request in the repositories is included.
                </div>
            )}
            {selected.map((entry, index) => {
                const { name, arg } = splitFilter(entry);
                const info = infoFor(name);
                return (
                    <div
                        key={index}
                        style={{ display: 'flex', gap: spacing.sm, alignItems: 'center' }}
                    >
                        <div style={{ flex: '2 1 200px' }}>
                            <Select
                                value={name}
                                onChange={e => replace(index, joinFilter(e.target.value, arg))}
                                style={{ width: '100%' }}
                                options={
                                    info
                                        ? filters.map(f => ({ value: f.name, label: f.name }))
                                        : // Keep an unrecognized filter selectable so editing a
                                          // neighbouring field can't silently drop it.
                                          [
                                              { value: name, label: `${name} (unknown)` },
                                              ...filters.map(f => ({
                                                  value: f.name,
                                                  label: f.name,
                                              })),
                                          ]
                                }
                            />
                        </div>
                        {info?.requires_arg && (
                            <div style={{ flex: '1 1 140px' }}>
                                <Input
                                    value={arg}
                                    onChange={e => replace(index, joinFilter(name, e.target.value))}
                                    placeholder={info.arg_label || 'value'}
                                />
                            </div>
                        )}
                        <IconButton
                            label="Remove filter"
                            danger
                            onClick={() => onChange(selected.filter((_, i) => i !== index))}
                        >
                            ×
                        </IconButton>
                    </div>
                );
            })}
            {selected.length > 0 && (
                <div style={{ fontSize: fontSize.sm, color: colors.textTertiary }}>
                    {infoFor(splitFilter(selected[selected.length - 1]).name)?.description}
                </div>
            )}
            <div>
                <Button
                    variant="secondary"
                    size="sm"
                    disabled={filters.length === 0}
                    onClick={() => {
                        const unused = filters.find(
                            f => !selected.some(entry => splitFilter(entry).name === f.name)
                        );
                        const next = unused ?? filters[0];
                        if (next) onChange([...selected, next.name]);
                    }}
                >
                    + Add filter
                </Button>
            </div>
        </div>
    );
}

function SectionHeading({ children }: { children: ReactNode }) {
    return (
        <div
            style={{
                fontSize: fontSize.sm,
                fontWeight: 600,
                color: colors.textSecondary,
                textTransform: 'uppercase',
                letterSpacing: '0.5px',
            }}
        >
            {children}
        </div>
    );
}

function FieldLabel({ children }: { children: ReactNode }) {
    return (
        <label style={{ fontSize: fontSize.sm, color: colors.textSecondary, fontWeight: 500 }}>
            {children}
        </label>
    );
}

function Hint({ children }: { children: ReactNode }) {
    return <div style={{ fontSize: fontSize.sm, color: colors.textTertiary }}>{children}</div>;
}

function Notice({ children }: { children: ReactNode }) {
    return (
        <div
            style={{
                fontSize: fontSize.sm,
                color: colors.textWarning,
                background: colors.bgWarningDim,
                border: `1px solid ${colors.borderWarningDim}`,
                borderRadius: borderRadius.md,
                padding: spacing.sm,
            }}
        >
            {children}
        </div>
    );
}

function ProblemList({ problems }: { problems: ConfigValidationError[] }) {
    if (problems.length === 0) return null;
    return (
        <ul
            style={{
                margin: 0,
                paddingLeft: spacing.xl,
                color: colors.textDanger,
                fontSize: fontSize.sm,
            }}
        >
            {problems.map((problem, i) => (
                <li key={i}>
                    <strong>{problem.field}</strong> {problem.message}
                </li>
            ))}
        </ul>
    );
}

function Checkbox({
    label,
    checked,
    disabled = false,
    onChange,
}: {
    label: string;
    checked: boolean;
    disabled?: boolean;
    onChange: (checked: boolean) => void;
}) {
    return (
        <label
            style={{
                display: 'flex',
                alignItems: 'center',
                gap: spacing.sm,
                fontSize: fontSize.base,
                color: colors.textSecondary,
                cursor: disabled ? 'not-allowed' : 'pointer',
                opacity: disabled ? 0.5 : 1,
            }}
        >
            <input
                type="checkbox"
                checked={checked}
                disabled={disabled}
                onChange={e => onChange(e.target.checked)}
                style={{ cursor: disabled ? 'not-allowed' : 'pointer' }}
            />
            {label}
        </label>
    );
}

function IconButton({
    children,
    label,
    danger = false,
    disabled = false,
    onClick,
}: {
    children: ReactNode;
    label: string;
    danger?: boolean;
    disabled?: boolean;
    onClick: (e: MouseEvent) => void;
}) {
    return (
        <button
            type="button"
            title={label}
            aria-label={label}
            disabled={disabled}
            onClick={onClick}
            style={{
                background: 'transparent',
                border: `1px solid ${colors.border}`,
                borderRadius: borderRadius.sm,
                color: danger ? colors.textDanger : colors.textSecondary,
                cursor: disabled ? 'not-allowed' : 'pointer',
                opacity: disabled ? 0.4 : 1,
                width: '24px',
                height: '24px',
                lineHeight: 1,
                padding: 0,
                flexShrink: 0,
            }}
        >
            {children}
        </button>
    );
}

/** Maps the tri-state per-workflow notification override to a select value. */
function notificationValue(setting: boolean | null | undefined): string {
    if (setting === true) return 'on';
    if (setting === false) return 'off';
    return 'inherit';
}

function notificationSetting(value: string): boolean | null {
    if (value === 'on') return true;
    if (value === 'off') return false;
    return null;
}

function errorMessage(e: unknown): string {
    const raw = e instanceof Error ? e.message : String(e);
    try {
        const parsed = JSON.parse(raw);
        if (typeof parsed === 'string') return parsed;
        return parsed?.message ?? raw;
    } catch {
        return raw;
    }
}
