const std = @import("std");

pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    const mode = b.standardOptimizeOption(.{});

    const exe = b.addExecutable(.{
        .name = "claude_review",
        .root_source_file = .{ .src_path = .{
            .owner = b,
            .sub_path = "main.zig",
        } },
        .target = target,
        .optimize = mode,
    });

    b.installArtifact(exe);
}
