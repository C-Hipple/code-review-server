import { useCallback, useEffect, useMemo, useState } from 'react';
import type { MouseEvent, ReactNode } from 'react';
import { getConfig, updateConfig } from '../api';
import {
    cleanDraft,
    draftFromConfig,
    emptyWorkflow,
    joinFilter,
    joinList,
    problemsByWorkflow,
    splitFilter,
    splitList,
    validateDraft,
    NOTIFICATION_OPTIONS,
    PR_STATE_OPTIONS,
} from '../config_utils';
import type {
    ConfigDraft,
    ConfigValidationError,
    FilterInfo,
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
 * Editor for the server's TOML configuration (~/.config/codereviewserver.toml).
 *
 * The workflow type and filter pickers are built from the registries the
 * server sends with the config, so they always offer what this server can
 * actually run. Validation happens twice: here for immediate feedback, and
 * again on the server, which refuses to write a config it can't run.
 */
export default function ConfigManager() {
    const [draft, setDraft] = useState<ConfigDraft | null>(null);
    const [saved, setSaved] = useState<ConfigDraft | null>(null);
    const [workflowTypes, setWorkflowTypes] = useState<WorkflowTypeInfo[]>([]);
    const [filters, setFilters] = useState<FilterInfo[]>([]);
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
        () => (draft ? validateDraft(draft, workflowTypes, filters) : []),
        [draft, workflowTypes, filters]
    );
    // Server problems are cleared as soon as the draft changes, so a stale
    // rejection doesn't linger next to a field the user already fixed.
    const problems = clientProblems.length > 0 ? clientProblems : serverProblems;
    const grouped = useMemo(() => problemsByWorkflow(problems), [problems]);
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
            setPath(reply.path);
            setUsingDefaults(reply.using_defaults ?? false);
            setStatus({
                kind: 'ok',
                text: `${reply.message}. Workflow changes take effect on the next sync.`,
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

    const globalProblems = showProblems ? (grouped.get(-1) ?? []) : [];

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
                        problems={showProblems ? (grouped.get(index) ?? []) : []}
                        onToggle={() => setExpanded(expanded === index ? null : index)}
                        onChange={change => updateWorkflow(index, change)}
                        onRemove={() => removeWorkflow(index)}
                        onMove={delta => moveWorkflow(index, delta)}
                        canMoveUp={index > 0}
                        canMoveDown={index < draft.Workflows.length - 1}
                    />
                ))}
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
        <div
            style={{
                border: `1px solid ${problems.length > 0 ? colors.borderDangerDim : colors.border}`,
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
                    <div style={{ fontWeight: 500 }}>
                        {workflow.Name?.trim() || <em>Unnamed workflow</em>}
                    </div>
                    <div
                        style={{
                            fontSize: fontSize.sm,
                            color: colors.textTertiary,
                            overflow: 'hidden',
                            textOverflow: 'ellipsis',
                            whiteSpace: 'nowrap',
                        }}
                    >
                        {workflow.WorkflowType} → {workflow.SectionTitle?.trim() || 'no section'}
                    </div>
                </div>
                {problems.length > 0 && (
                    <span style={{ color: colors.textDanger, fontSize: fontSize.sm }}>
                        {problems.length} issue{problems.length === 1 ? '' : 's'}
                    </span>
                )}
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
                    label="Remove workflow"
                    danger
                    onClick={e => {
                        e.stopPropagation();
                        onRemove();
                    }}
                >
                    ×
                </IconButton>
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
                                            DesktopNotifications: notificationSetting(
                                                e.target.value
                                            ),
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
                </div>
            )}
        </div>
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
    onChange,
}: {
    label: string;
    checked: boolean;
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
                cursor: 'pointer',
            }}
        >
            <input
                type="checkbox"
                checked={checked}
                onChange={e => onChange(e.target.checked)}
                style={{ cursor: 'pointer' }}
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
