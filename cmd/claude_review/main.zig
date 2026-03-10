const std = @import("std");

pub fn main() !void {
    var gpa = std.heap.GeneralPurposeAllocator(.{}){};
    defer _ = gpa.deinit();
    const allocator = gpa.allocator();

    const args = try std.process.argsAlloc(allocator);
    defer std.process.argsFree(allocator, args);

    var owner: []const u8 = "";
    var repo: []const u8 = "";
    var number: []const u8 = "0";

    var i: usize = 1;
    while (i < args.len) : (i += 1) {
        const arg = args[i];
        if (std.mem.eql(u8, arg, "--owner") and i + 1 < args.len) {
            i += 1;
            owner = args[i];
        } else if (std.mem.eql(u8, arg, "--repo") and i + 1 < args.len) {
            i += 1;
            repo = args[i];
        } else if (std.mem.eql(u8, arg, "--number") and i + 1 < args.len) {
            i += 1;
            number = args[i];
        }
        // --diff, --comments, --headers are accepted but ignored;
        // claude CLI fetches the PR directly via the identifier.
        else if ((std.mem.eql(u8, arg, "--diff") or
            std.mem.eql(u8, arg, "--comments") or
            std.mem.eql(u8, arg, "--headers")) and i + 1 < args.len)
        {
            i += 1; // skip value
        }
    }

    const stderr = std.io.getStdErr().writer();

    if (owner.len == 0 or repo.len == 0 or std.mem.eql(u8, number, "0")) {
        try stderr.writeAll("Error: --owner, --repo, and --number are required\n");
        std.process.exit(1);
    }

    // Build prompt: "review PR #<number> on repo <owner>/<repo>"
    const prompt = try std.fmt.allocPrint(
        allocator,
        "review PR #{s} on repo {s}/{s}",
        .{ number, owner, repo },
    );
    defer allocator.free(prompt);

    const argv = [_][]const u8{ "claude", "-p", prompt, "--model", "sonnet" };

    var child = std.process.Child.init(&argv, allocator);
    child.stdout_behavior = .Pipe;
    child.stderr_behavior = .Pipe;

    try child.spawn();

    const stdout_output = try child.stdout.?.readToEndAlloc(allocator, 10 * 1024 * 1024);
    defer allocator.free(stdout_output);

    const stderr_output = try child.stderr.?.readToEndAlloc(allocator, 1024 * 1024);
    defer allocator.free(stderr_output);

    const term = try child.wait();

    if (stderr_output.len > 0) {
        try stderr.writeAll(stderr_output);
    }

    switch (term) {
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

    const stdout = std.io.getStdOut().writer();
    try stdout.writeAll(stdout_output);
}
