const std = @import("std");

const default_model = "sonnet";
const default_timeout_sec: u64 = 270; // 4m30s — slightly under server's 5m

const allowed_tools =
    "Read,Grep,Glob,WebFetch," ++
    "Bash(git log:*),Bash(git blame:*),Bash(git diff:*),Bash(git show:*)," ++
    "Bash(rg:*),Bash(go test:*),Bash(go vet:*),Bash(go build:*)," ++
    "Bash(bun test:*),Bash(bun run:*),Bash(gh pr view:*)";

const system_prompt =
    \\You are a senior engineer reviewing a GitHub pull request. You have
    \\Read, Grep, Glob, Bash, and WebFetch tools available — use them to
    \\investigate, not just skim the diff:
    \\  - `git log -n 10 --oneline -- <file>` and `git blame` for history on touched code
    \\  - `rg` to find callers of changed symbols
    \\  - Read CLAUDE.md if present for project conventions
    \\  - Run the relevant test package (e.g. `go test ./pkg/...`) and `go vet`
    \\  - Read related files to gauge impact
    \\
    \\Strict output format (Markdown, in this exact order):
    \\
    \\## Summary
    \\3-5 bullets describing what this PR does.
    \\
    \\## Blockers
    \\Correctness bugs, security issues, broken contracts. Write "None" if empty.
    \\
    \\## Suggestions
    \\Non-blocking improvements worth considering. Write "None" if empty.
    \\
    \\## Nits
    \\Style and cleanup. Write "None" if empty.
    \\
    \\## Findings JSON
    \\```json
    \\[{"file":"path","line":42,"severity":"blocker|major|nit","message":"..."}]
    \\```
    \\
    \\Every finding must cite file:line. Prefer specific over general.
    \\If you discovered something via tool use, briefly say how.
    \\Do NOT repeat findings that appear in "Previous Automated Review"
    \\unless they are still present in the current diff.
;

const Args = struct {
    owner: []const u8 = "",
    repo: []const u8 = "",
    number: []const u8 = "",
    diff: []const u8 = "",
    comments: []const u8 = "",
    headers: []const u8 = "",
    post_review: bool = false,
};

const safe_chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-/";

fn isSafeIdent(s: []const u8) bool {
    if (s.len == 0 or s.len > 200) return false;
    for (s) |c| {
        if (std.mem.indexOfScalar(u8, safe_chars, c) == null) return false;
    }
    return true;
}

fn isNumeric(s: []const u8) bool {
    if (s.len == 0 or s.len > 12) return false;
    for (s) |c| {
        if (c < '0' or c > '9') return false;
    }
    return true;
}

fn diffFilePaths(allocator: std.mem.Allocator, diff: []const u8) !std.ArrayList([]const u8) {
    var paths = std.ArrayList([]const u8).init(allocator);
    var it = std.mem.splitScalar(u8, diff, '\n');
    while (it.next()) |line| {
        if (std.mem.startsWith(u8, line, "+++ b/")) {
            const rest = line["+++ b/".len..];
            const end = std.mem.indexOfScalar(u8, rest, '\t') orelse rest.len;
            try paths.append(rest[0..end]);
        }
    }
    return paths;
}

fn isTrivialPath(p: []const u8) bool {
    const trivial_suffixes = [_][]const u8{
        ".md",
        "/CHANGELOG",
        "/LICENSE",
        ".pb.go",
        ".gen.go",
        "_generated.go",
        "bun.lock",
        "bun.lockb",
        "package-lock.json",
        "yarn.lock",
        "Cargo.lock",
        "go.sum",
    };
    for (trivial_suffixes) |s| {
        if (std.mem.endsWith(u8, p, s)) return true;
    }
    return false;
}

fn isTrivialDiff(allocator: std.mem.Allocator, diff: []const u8) !bool {
    if (diff.len == 0) return false;
    var paths = try diffFilePaths(allocator, diff);
    defer paths.deinit();
    if (paths.items.len == 0) return false;
    for (paths.items) |p| {
        if (!isTrivialPath(p)) return false;
    }
    return true;
}

fn getEnv(allocator: std.mem.Allocator, name: []const u8) ?[]u8 {
    return std.process.getEnvVarOwned(allocator, name) catch null;
}

fn cacheDir(allocator: std.mem.Allocator) ![]u8 {
    if (getEnv(allocator, "CRS_HOME")) |home| {
        defer allocator.free(home);
        return try std.fs.path.join(allocator, &.{ home, "cache", "claude_review" });
    }
    const home = getEnv(allocator, "HOME") orelse return error.NoHome;
    defer allocator.free(home);
    return try std.fs.path.join(allocator, &.{ home, ".cache", "crs", "claude_review" });
}

fn cacheFilePath(allocator: std.mem.Allocator, args: Args) ![]u8 {
    const dir = try cacheDir(allocator);
    defer allocator.free(dir);
    const name = try std.fmt.allocPrint(allocator, "{s}__{s}__{s}.md", .{ args.owner, args.repo, args.number });
    defer allocator.free(name);
    return try std.fs.path.join(allocator, &.{ dir, name });
}

fn loadPrior(allocator: std.mem.Allocator, path: []const u8) ?[]u8 {
    const f = std.fs.cwd().openFile(path, .{}) catch return null;
    defer f.close();
    return f.readToEndAlloc(allocator, 1 * 1024 * 1024) catch null;
}

fn savePrior(path: []const u8, content: []const u8) !void {
    if (std.fs.path.dirname(path)) |dir| {
        std.fs.cwd().makePath(dir) catch {};
    }
    const f = try std.fs.cwd().createFile(path, .{ .truncate = true });
    defer f.close();
    try f.writeAll(content);
}

fn buildPrompt(
    allocator: std.mem.Allocator,
    args: Args,
    prior: ?[]const u8,
) ![]u8 {
    var buf = std.ArrayList(u8).init(allocator);
    errdefer buf.deinit();
    const w = buf.writer();

    try w.print("Review PR #{s} on `{s}/{s}`.\n\n", .{ args.number, args.owner, args.repo });

    if (args.headers.len > 0) {
        try w.writeAll("## PR Metadata\n```json\n");
        try w.writeAll(args.headers);
        try w.writeAll("\n```\n\n");
    }

    if (args.comments.len > 0 and !std.mem.eql(u8, args.comments, "[]") and !std.mem.eql(u8, args.comments, "null")) {
        try w.writeAll("## Existing Review Comments (avoid duplicating these)\n```json\n");
        try w.writeAll(args.comments);
        try w.writeAll("\n```\n\n");
    }

    if (prior) |p| {
        try w.writeAll("## Previous Automated Review\n");
        try w.writeAll("Only re-report items still present in the current diff. Skip anything that has been fixed.\n\n");
        try w.writeAll(p);
        try w.writeAll("\n\n");
    }

    if (args.diff.len > 0) {
        try w.writeAll("## Diff\n```diff\n");
        try w.writeAll(args.diff);
        try w.writeAll("\n```\n\n");
    } else {
        try w.writeAll("## Diff\nNot provided directly — fetch via `gh pr view --json files,...`.\n\n");
    }

    try w.writeAll(
        \\## Instructions
        \\
        \\Be thorough. Use your tools liberally to dig in beyond the diff
        \\(history, callers, tests, linters). Cite file:line for every finding.
        \\Follow the strict output format from the system prompt exactly.
    );

    return buf.toOwnedSlice();
}

fn parseArgs(allocator: std.mem.Allocator) !Args {
    var args: Args = .{};
    const argv = try std.process.argsAlloc(allocator);
    defer std.process.argsFree(allocator, argv);

    var i: usize = 1;
    while (i < argv.len) : (i += 1) {
        const a = argv[i];
        if (std.mem.eql(u8, a, "--owner") and i + 1 < argv.len) {
            i += 1;
            args.owner = try allocator.dupe(u8, argv[i]);
        } else if (std.mem.eql(u8, a, "--repo") and i + 1 < argv.len) {
            i += 1;
            args.repo = try allocator.dupe(u8, argv[i]);
        } else if (std.mem.eql(u8, a, "--number") and i + 1 < argv.len) {
            i += 1;
            args.number = try allocator.dupe(u8, argv[i]);
        } else if (std.mem.eql(u8, a, "--diff") and i + 1 < argv.len) {
            i += 1;
            args.diff = try allocator.dupe(u8, argv[i]);
        } else if (std.mem.eql(u8, a, "--comments") and i + 1 < argv.len) {
            i += 1;
            args.comments = try allocator.dupe(u8, argv[i]);
        } else if (std.mem.eql(u8, a, "--headers") and i + 1 < argv.len) {
            i += 1;
            args.headers = try allocator.dupe(u8, argv[i]);
        } else if (std.mem.eql(u8, a, "--post-review")) {
            args.post_review = true;
        }
    }
    return args;
}

fn postReview(
    allocator: std.mem.Allocator,
    args: Args,
    body: []const u8,
) !void {
    // Write body to a temp file (avoids ARG_MAX limits for long reviews).
    const tmp_dir = std.posix.getenv("TMPDIR") orelse "/tmp";
    const tmp_path = try std.fmt.allocPrint(
        allocator,
        "{s}/claude_review_{s}_{s}_{s}.md",
        .{ tmp_dir, args.owner, args.repo, args.number },
    );
    defer allocator.free(tmp_path);

    {
        const f = try std.fs.cwd().createFile(tmp_path, .{ .truncate = true });
        defer f.close();
        try f.writeAll(body);
    }
    defer std.fs.cwd().deleteFile(tmp_path) catch {};

    const repo_arg = try std.fmt.allocPrint(allocator, "{s}/{s}", .{ args.owner, args.repo });
    defer allocator.free(repo_arg);

    const argv = [_][]const u8{ "gh", "pr", "review", args.number, "--repo", repo_arg, "--comment", "--body-file", tmp_path };
    var child = std.process.Child.init(&argv, allocator);
    child.stdout_behavior = .Inherit;
    child.stderr_behavior = .Inherit;
    _ = try child.spawnAndWait();
}

const Watchdog = struct {
    child: *std.process.Child,
    timeout_sec: u64,
    done: std.atomic.Value(bool),
    killed: std.atomic.Value(bool),

    fn run(self: *Watchdog) void {
        const start = std.time.timestamp();
        while (!self.done.load(.acquire)) {
            std.Thread.sleep(500 * std.time.ns_per_ms);
            const elapsed = std.time.timestamp() - start;
            if (elapsed >= @as(i64, @intCast(self.timeout_sec))) {
                self.killed.store(true, .release);
                _ = self.child.kill() catch {};
                return;
            }
        }
    }
};

pub fn main() !void {
    var gpa = std.heap.GeneralPurposeAllocator(.{}){};
    defer _ = gpa.deinit();
    const allocator = gpa.allocator();

    const stdout = std.io.getStdOut().writer();
    const stderr = std.io.getStdErr().writer();

    const args = try parseArgs(allocator);

    if (args.owner.len == 0 or args.repo.len == 0 or args.number.len == 0) {
        try stderr.writeAll("Error: --owner, --repo, and --number are required\n");
        std.process.exit(1);
    }
    if (!isSafeIdent(args.owner) or !isSafeIdent(args.repo) or !isNumeric(args.number)) {
        try stderr.writeAll("Error: invalid characters in --owner, --repo, or --number\n");
        std.process.exit(2);
    }

    // Trivial-diff fast path.
    if (try isTrivialDiff(allocator, args.diff)) {
        try stdout.writeAll(
            \\## Summary
            \\Docs / lockfile / generated-file changes only.
            \\
            \\## Blockers
            \\None
            \\
            \\## Suggestions
            \\None
            \\
            \\## Nits
            \\None
            \\
            \\## Findings JSON
            \\```json
            \\[]
            \\```
            \\
        );
        return;
    }

    // Load prior automated review for this PR (if any).
    var prior_path_buf: ?[]u8 = null;
    var prior: ?[]u8 = null;
    if (cacheFilePath(allocator, args)) |p| {
        prior_path_buf = p;
        prior = loadPrior(allocator, p);
    } else |_| {}
    defer if (prior) |p| allocator.free(p);
    defer if (prior_path_buf) |p| allocator.free(p);

    const prompt = try buildPrompt(allocator, args, prior);
    defer allocator.free(prompt);

    // Model — env override, else default.
    const model_env = getEnv(allocator, "CRS_CLAUDE_REVIEW_MODEL");
    defer if (model_env) |m| allocator.free(m);
    const model: []const u8 = model_env orelse default_model;

    // Timeout — env override, else default.
    var timeout_sec: u64 = default_timeout_sec;
    if (getEnv(allocator, "CRS_CLAUDE_REVIEW_TIMEOUT_SEC")) |t| {
        defer allocator.free(t);
        timeout_sec = std.fmt.parseInt(u64, t, 10) catch default_timeout_sec;
    }

    // Pass the prompt via stdin (large diffs would exceed ARG_MAX as an arg).
    const claude_argv = [_][]const u8{
        "claude",
        "-p",
        "--model",                model,
        "--output-format",        "text",
        "--append-system-prompt", system_prompt,
        "--allowedTools",         allowed_tools,
    };

    var child = std.process.Child.init(&claude_argv, allocator);
    child.stdin_behavior = .Pipe;
    child.stdout_behavior = .Pipe;
    child.stderr_behavior = .Pipe;
    try child.spawn();

    if (child.stdin) |stdin_pipe| {
        stdin_pipe.writeAll(prompt) catch {};
        stdin_pipe.close();
        child.stdin = null;
    }

    var watchdog: Watchdog = .{
        .child = &child,
        .timeout_sec = timeout_sec,
        .done = std.atomic.Value(bool).init(false),
        .killed = std.atomic.Value(bool).init(false),
    };
    const wd_thread = try std.Thread.spawn(.{}, Watchdog.run, .{&watchdog});

    // Stream stdout to our stdout AND capture it for caching/posting.
    var captured = std.ArrayList(u8).init(allocator);
    defer captured.deinit();

    var read_buf: [4096]u8 = undefined;
    while (true) {
        const n = child.stdout.?.read(&read_buf) catch break;
        if (n == 0) break;
        try stdout.writeAll(read_buf[0..n]);
        try captured.appendSlice(read_buf[0..n]);
    }

    const stderr_output = child.stderr.?.readToEndAlloc(allocator, 1024 * 1024) catch &[_]u8{};
    defer if (stderr_output.len > 0) allocator.free(stderr_output);

    const term = try child.wait();
    watchdog.done.store(true, .release);
    wd_thread.join();

    if (stderr_output.len > 0) {
        stderr.writeAll(stderr_output) catch {};
    }

    if (watchdog.killed.load(.acquire)) {
        try stderr.print("\nclaude_review: timed out after {d}s; returning partial output\n", .{timeout_sec});
        // Still cache + exit 0 so partial result is stored.
    } else switch (term) {
        .Exited => |code| {
            if (code != 0) {
                try stderr.print("claude exited with code {d}\n", .{code});
                std.process.exit(code);
            }
        },
        else => {
            try stderr.writeAll("claude terminated abnormally\n");
            std.process.exit(1);
        },
    }

    // Persist for next run's "previous review" context.
    if (prior_path_buf) |p| {
        savePrior(p, captured.items) catch |e| {
            stderr.print("claude_review: failed to save cache: {s}\n", .{@errorName(e)}) catch {};
        };
    }

    if (args.post_review and captured.items.len > 0) {
        postReview(allocator, args, captured.items) catch |e| {
            stderr.print("claude_review: failed to post review: {s}\n", .{@errorName(e)}) catch {};
        };
    }
}

test "isSafeIdent accepts normal owner/repo" {
    try std.testing.expect(isSafeIdent("c-hipple"));
    try std.testing.expect(isSafeIdent("code-review-server"));
    try std.testing.expect(isSafeIdent("foo.bar_baz"));
    try std.testing.expect(!isSafeIdent(""));
    try std.testing.expect(!isSafeIdent("foo; rm -rf /"));
    try std.testing.expect(!isSafeIdent("foo bar"));
}

test "isNumeric accepts PR numbers" {
    try std.testing.expect(isNumeric("42"));
    try std.testing.expect(!isNumeric(""));
    try std.testing.expect(!isNumeric("4a"));
    try std.testing.expect(!isNumeric("-1"));
}

test "isTrivialDiff true for docs-only" {
    const allocator = std.testing.allocator;
    const diff =
        \\diff --git a/README.md b/README.md
        \\--- a/README.md
        \\+++ b/README.md
        \\@@ -1 +1,2 @@
        \\ hi
        \\+more
    ;
    try std.testing.expect(try isTrivialDiff(allocator, diff));
}

test "isTrivialDiff false when any code file changes" {
    const allocator = std.testing.allocator;
    const diff =
        \\diff --git a/README.md b/README.md
        \\+++ b/README.md
        \\@@
        \\diff --git a/foo.go b/foo.go
        \\+++ b/foo.go
        \\@@
    ;
    try std.testing.expect(!(try isTrivialDiff(allocator, diff)));
}
