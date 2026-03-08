#![allow(dead_code)]

use serde::{Deserialize, Serialize};

/// JSON-RPC 1.0 request
#[derive(Serialize)]
pub struct RpcRequest {
    pub method: String,
    pub params: Vec<serde_json::Value>,
    pub id: u64,
}

/// JSON-RPC 1.0 response
#[derive(Deserialize)]
pub struct RpcResponse {
    pub result: Option<serde_json::Value>,
    pub error: Option<serde_json::Value>,
    pub id: u64,
}

/// A single review item from GetAllReviews
#[derive(Debug, Clone, Deserialize)]
pub struct ReviewItem {
    pub section: String,
    #[serde(rename = "section_priority")]
    pub priority: i32,
    pub status: String,
    pub title: String,
    pub owner: String,
    pub repo: String,
    pub number: i32,
    pub author: String,
    pub url: String,
    #[serde(default)]
    pub release_status: String,
}

/// Reply from GetAllReviews
#[derive(Debug, Deserialize)]
pub struct GetReviewsReply {
    pub content: String,
    pub items: Vec<ReviewItem>,
}

/// Reply from GetPR
#[derive(Debug, Deserialize)]
pub struct GetPRReply {
    pub okay: bool,
    pub content: String,
    pub metadata: Option<PRMetadata>,
    pub diff: String,
    #[serde(default)]
    pub comments: Vec<CommentJSON>,
    #[serde(default)]
    pub outdated_comments: Vec<CommentJSON>,
    #[serde(default)]
    pub reviews: Vec<ReviewJSON>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct PRMetadata {
    pub number: i32,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub author: String,
    #[serde(default)]
    pub base_ref: String,
    #[serde(default)]
    pub head_ref: String,
    #[serde(default)]
    pub state: String,
    #[serde(default)]
    pub milestone: String,
    #[serde(default)]
    pub labels: Vec<String>,
    #[serde(default)]
    pub assignees: Vec<String>,
    #[serde(default)]
    pub reviewers: Vec<String>,
    #[serde(default)]
    pub requested_teams: Vec<String>,
    #[serde(default)]
    pub approved_by: Vec<String>,
    #[serde(default)]
    pub changes_requested_by: Vec<String>,
    #[serde(default)]
    pub commented_by: Vec<String>,
    #[serde(default)]
    pub draft: bool,
    #[serde(default)]
    pub ci_status: String,
    #[serde(default)]
    pub ci_failures: Vec<String>,
    #[serde(default)]
    pub body: String,
    #[serde(default)]
    pub url: String,
    #[serde(default)]
    pub worktree_path: String,
}

#[derive(Debug, Clone, Deserialize)]
pub struct CommentJSON {
    pub id: String,
    pub author: String,
    pub body: String,
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub position: String,
    #[serde(default)]
    pub in_reply_to: i64,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub outdated: bool,
    #[serde(default)]
    pub diff_hunk: String,
}

#[derive(Debug, Clone, Deserialize)]
pub struct ReviewJSON {
    pub id: i64,
    pub user: String,
    #[serde(default)]
    pub body: String,
    pub state: String,
    #[serde(default)]
    pub submitted_at: String,
    #[serde(default)]
    pub html_url: String,
}
