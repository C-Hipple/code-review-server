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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_review_item_deserialization() {
        let json = r#"{
            "section": "Needs Review",
            "section_priority": 1,
            "status": "PENDING",
            "title": "Add new feature",
            "owner": "octocat",
            "repo": "hello",
            "number": 42,
            "author": "alice",
            "url": "https://github.com/octocat/hello/pull/42"
        }"#;

        let item: ReviewItem = serde_json::from_str(json).expect("Failed to deserialize");
        assert_eq!(item.section, "Needs Review");
        assert_eq!(item.priority, 1);
        assert_eq!(item.number, 42);
        assert_eq!(item.author, "alice");
    }

    #[test]
    fn test_get_reviews_reply_deserialization() {
        let json = r#"{
            "content": "* Needs Review",
            "items": [
                {
                    "section": "Needs Review",
                    "section_priority": 1,
                    "status": "PENDING",
                    "title": "Test PR",
                    "owner": "owner",
                    "repo": "repo",
                    "number": 1,
                    "author": "author",
                    "url": "https://example.com/1"
                }
            ]
        }"#;

        let reply: GetReviewsReply = serde_json::from_str(json).expect("Failed to deserialize");
        assert_eq!(reply.items.len(), 1);
        assert_eq!(reply.items[0].number, 1);
    }

    #[test]
    fn test_pr_metadata_deserialization() {
        let json = r#"{
            "number": 42,
            "title": "Add feature",
            "author": "alice",
            "base_ref": "main",
            "head_ref": "feature-branch",
            "state": "open",
            "url": "https://github.com/octocat/hello/pull/42"
        }"#;

        let meta: PRMetadata = serde_json::from_str(json).expect("Failed to deserialize");
        assert_eq!(meta.number, 42);
        assert_eq!(meta.title, "Add feature");
        assert_eq!(meta.state, "open");
    }

    #[test]
    fn test_comment_json_with_defaults() {
        let json = r#"{
            "id": "123",
            "author": "bob",
            "body": "Looks good"
        }"#;

        let comment: CommentJSON = serde_json::from_str(json).expect("Failed to deserialize");
        assert_eq!(comment.id, "123");
        assert_eq!(comment.author, "bob");
        assert_eq!(comment.path, ""); // default
    }

    #[test]
    fn test_review_json_deserialization() {
        let json = r#"{
            "id": 999,
            "user": "reviewer",
            "state": "APPROVED"
        }"#;

        let review: ReviewJSON = serde_json::from_str(json).expect("Failed to deserialize");
        assert_eq!(review.id, 999);
        assert_eq!(review.user, "reviewer");
        assert_eq!(review.state, "APPROVED");
    }
}
