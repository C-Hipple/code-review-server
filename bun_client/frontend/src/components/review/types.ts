import { colors } from '../../design';
import type { PRAnnotation } from '../../plugin_utils';

// Above this many changed lines in a single file, the diff renders that file
// without per-line syntax highlighting to avoid thousands of Prism instances.
export const MAX_HIGHLIGHT_LINES_PER_FILE = 2000;

// Stable slug for in-page anchor IDs from file paths.
export const slugify = (s: string): string =>
    s.replace(/[^a-zA-Z0-9_-]+/g, '-').replace(/^-+|-+$/g, '');

// Strip HTML comments from text (e.g., <!-- comment -->)
export const stripHtmlComments = (text: string): string => {
    return text.replace(/<!--[\s\S]*?-->/g, '');
};

export interface Comment {
    id: string;
    author: string;
    body: string;
    path: string;
    position: string;
    in_reply_to: number;
    created_at: string;
    outdated: boolean;
    diff_hunk: string;
}

export interface ReviewData {
    id: number;
    user: string;
    body: string;
    state: string;
    submitted_at: string;
    html_url: string;
}

// The plugin response contract lives in plugin_utils, alongside the helpers
// that interpret it; re-exported here so review components have one import.
export type {
    PluginAnnotation,
    PluginBody,
    PluginBodyType,
    PluginResult,
    PRAnnotation,
} from '../../plugin_utils';

export interface PRMetadata {
    number: number;
    title: string;
    author: string;
    base_ref: string;
    head_ref: string;
    state: string;
    milestone: string;
    labels: string[];
    assignees: string[];
    reviewers: string[]; // Requested individual reviewers
    requested_teams: string[]; // Requested team reviewers
    approved_by: string[]; // Users who approved
    changes_requested_by: string[]; // Users who requested changes
    commented_by: string[]; // Users who commented
    draft: boolean;
    ci_status: string;
    ci_failures: string[];
    body: string;
    url: string;
    repo_path: string;
    worktree_path: string;
}

export interface PRResponse {
    content: string;
    diff: string;
    comments: Comment[];
    outdated_comments: Comment[];
    reviews: ReviewData[];
    metadata: PRMetadata;
    feedback: string;
    // Annotations from every plugin that has already run for this PR, each
    // tagged with its source plugin. The review view builds the same list from
    // the plugin outputs it loads (see annotation_utils) so a plugin rerun
    // refreshes the diff, and reads this field for clients that don't.
    annotations?: PRAnnotation[];
    // Only set by SyncPR: true when the sync pulled in a new head SHA or new comments.
    updated?: boolean;
}

// Map file extensions to Prism language identifiers
export const getLanguageFromFilename = (filename: string): string => {
    const ext = filename.split('.').pop()?.toLowerCase() || '';
    const languageMap: Record<string, string> = {
        // JavaScript/TypeScript
        js: 'javascript',
        jsx: 'jsx',
        ts: 'typescript',
        tsx: 'tsx',
        mjs: 'javascript',
        cjs: 'javascript',
        // Web
        html: 'html',
        htm: 'html',
        css: 'css',
        scss: 'scss',
        sass: 'sass',
        less: 'less',
        json: 'json',
        xml: 'xml',
        svg: 'xml',
        // Backend
        py: 'python',
        rb: 'ruby',
        go: 'go',
        rs: 'rust',
        java: 'java',
        kt: 'kotlin',
        scala: 'scala',
        php: 'php',
        c: 'c',
        h: 'c',
        cpp: 'cpp',
        cc: 'cpp',
        cxx: 'cpp',
        hpp: 'cpp',
        cs: 'csharp',
        swift: 'swift',
        // Shell/Config
        sh: 'bash',
        bash: 'bash',
        zsh: 'bash',
        fish: 'bash',
        ps1: 'powershell',
        yaml: 'yaml',
        yml: 'yaml',
        toml: 'toml',
        ini: 'ini',
        conf: 'ini',
        // Data/Docs
        md: 'markdown',
        mdx: 'markdown',
        sql: 'sql',
        graphql: 'graphql',
        gql: 'graphql',
        // Other
        dockerfile: 'docker',
        makefile: 'makefile',
        lua: 'lua',
        r: 'r',
        dart: 'dart',
        ex: 'elixir',
        exs: 'elixir',
        erl: 'erlang',
        hrl: 'erlang',
        clj: 'clojure',
        vim: 'vim',
        el: 'lisp',
        lisp: 'lisp',
        hs: 'haskell',
        ml: 'ocaml',
        proto: 'protobuf',
    };

    // Handle special filenames
    const basename = filename.split('/').pop()?.toLowerCase() || '';
    if (basename === 'dockerfile') return 'docker';
    if (basename === 'makefile' || basename === 'gnumakefile') return 'makefile';
    if (basename.endsWith('.d.ts')) return 'typescript';

    return languageMap[ext] || 'text';
};

// Get icon and color for file status
export const getFileStatusInfo = (status?: 'modified' | 'new' | 'deleted' | 'renamed') => {
    switch (status) {
        case 'new':
            return {
                icon: '+',
                label: 'new file',
                color: colors.success,
                bg: colors.bgSuccessDim,
            };
        case 'deleted':
            return {
                icon: '−',
                label: 'deleted',
                color: colors.danger,
                bg: colors.bgDangerDim,
            };
        case 'renamed':
            return {
                icon: '→',
                label: 'renamed',
                color: colors.warning,
                bg: colors.bgWarningDim,
            };
        default:
            return {
                icon: '●',
                label: 'modified',
                color: colors.accent,
                bg: colors.bgInfoDimStrong,
            };
    }
};
