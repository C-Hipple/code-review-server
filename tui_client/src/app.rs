use anyhow::Result;
use crossterm::event::{KeyCode, KeyEvent};

use crate::rpc::RpcClient;
use crate::types::{GetPRReply, GetReviewsReply, PRMetadata, ReviewItem};

/// Which screen the user is viewing.
#[derive(Debug, Clone, PartialEq)]
pub enum View {
    /// Sections list with PR items
    Sections,
    /// PR detail view
    PRDetail,
}

/// A section header with its contained items.
#[derive(Debug, Clone)]
pub struct Section {
    pub name: String,
    pub priority: i32,
    pub items: Vec<ReviewItem>,
}

/// Application state.
pub struct App {
    pub rpc: RpcClient,
    pub view: View,

    // -- Sections view --
    pub sections: Vec<Section>,
    /// Flat index across all rows (section headers + items)
    pub list_cursor: usize,
    /// Total number of rows in the flat list
    pub list_len: usize,

    // -- PR detail view --
    pub pr_metadata: Option<PRMetadata>,
    pub pr_diff: String,
    pub pr_comments: Vec<crate::types::CommentJSON>,
    pub pr_reviews: Vec<crate::types::ReviewJSON>,
    pub pr_scroll: u16,
    pub pr_content: String,

    // Status / error bar
    pub status_msg: String,
}

impl App {
    pub fn new() -> Result<Self> {
        let rpc = RpcClient::new()?;
        Ok(Self {
            rpc,
            view: View::Sections,
            sections: Vec::new(),
            list_cursor: 0,
            list_len: 0,
            pr_metadata: None,
            pr_diff: String::new(),
            pr_comments: Vec::new(),
            pr_reviews: Vec::new(),
            pr_scroll: 0,
            pr_content: String::new(),
            status_msg: String::from("Loading..."),
        })
    }

    /// Fetch all review sections from the server.
    pub fn load_reviews(&mut self) -> Result<()> {
        self.status_msg = "Fetching reviews...".into();
        let result = self.rpc.call("GetAllReviews", serde_json::json!({}))?;
        let reply: GetReviewsReply = serde_json::from_value(result)?;
        self.build_sections(reply.items);
        self.status_msg = format!("{} sections loaded", self.sections.len());
        Ok(())
    }

    /// Group ReviewItems into sections, preserving server ordering.
    fn build_sections(&mut self, items: Vec<ReviewItem>) {
        self.sections.clear();
        for item in items {
            if let Some(sec) = self.sections.iter_mut().find(|s| s.name == item.section) {
                sec.items.push(item);
            } else {
                let name = item.section.clone();
                let priority = item.priority;
                self.sections.push(Section {
                    name,
                    priority,
                    items: vec![item],
                });
            }
        }
        // Sort sections by priority then name
        self.sections
            .sort_by(|a, b| a.priority.cmp(&b.priority).then(a.name.cmp(&b.name)));

        // Compute flat list length: each section header + its items
        self.list_len = self
            .sections
            .iter()
            .map(|s| 1 + s.items.len())
            .sum();
        self.list_cursor = if self.list_len > 0 { 0 } else { 0 };
    }

    /// Load a specific PR by owner/repo/number.
    fn load_pr(&mut self, owner: &str, repo: &str, number: i32) -> Result<()> {
        self.status_msg = format!("Loading PR #{number}...");
        let result = self.rpc.call(
            "GetPR",
            serde_json::json!({
                "Owner": owner,
                "Repo": repo,
                "Number": number,
            }),
        )?;
        let reply: GetPRReply = serde_json::from_value(result)?;
        self.pr_metadata = reply.metadata;
        self.pr_diff = reply.diff;
        self.pr_comments = reply.comments;
        self.pr_reviews = reply.reviews;
        self.pr_content = reply.content;
        self.pr_scroll = 0;
        self.view = View::PRDetail;
        if let Some(ref m) = self.pr_metadata {
            self.status_msg = format!("PR #{}: {}", m.number, m.title);
        }
        Ok(())
    }

    /// Map the flat cursor position to (section_index, item_index_within_section) or just a section header.
    pub fn cursor_to_entry(&self) -> CursorEntry {
        let mut pos = 0;
        for (si, section) in self.sections.iter().enumerate() {
            if pos == self.list_cursor {
                return CursorEntry::SectionHeader;
            }
            pos += 1;
            for ii in 0..section.items.len() {
                if pos == self.list_cursor {
                    return CursorEntry::Item(si, ii);
                }
                pos += 1;
            }
        }
        CursorEntry::SectionHeader
    }

    /// Handle a key event. Returns true if the app should quit.
    pub fn handle_key(&mut self, key: KeyEvent) -> Result<bool> {
        match self.view {
            View::Sections => self.handle_sections_key(key),
            View::PRDetail => self.handle_pr_detail_key(key),
        }
    }

    fn handle_sections_key(&mut self, key: KeyEvent) -> Result<bool> {
        match key.code {
            // Quit
            KeyCode::Char('q') => return Ok(true),

            // Vim navigation
            KeyCode::Char('j') | KeyCode::Down => {
                if self.list_len > 0 && self.list_cursor < self.list_len - 1 {
                    self.list_cursor += 1;
                }
            }
            KeyCode::Char('k') | KeyCode::Up => {
                if self.list_cursor > 0 {
                    self.list_cursor -= 1;
                }
            }
            KeyCode::Char('g') => {
                self.list_cursor = 0;
            }
            KeyCode::Char('G') => {
                if self.list_len > 0 {
                    self.list_cursor = self.list_len - 1;
                }
            }
            KeyCode::Char('d') => {
                // Half-page down
                let jump = 10;
                self.list_cursor = (self.list_cursor + jump).min(self.list_len.saturating_sub(1));
            }
            KeyCode::Char('u') => {
                // Half-page up
                let jump = 10;
                self.list_cursor = self.list_cursor.saturating_sub(jump);
            }

            // Open PR detail
            KeyCode::Enter | KeyCode::Char('l') => {
                if let CursorEntry::Item(si, ii) = self.cursor_to_entry() {
                    let item = &self.sections[si].items[ii];
                    let owner = item.owner.clone();
                    let repo = item.repo.clone();
                    let number = item.number;
                    if let Err(e) = self.load_pr(&owner, &repo, number) {
                        self.status_msg = format!("Error: {e}");
                    }
                }
            }

            // Open PR URL in browser
            KeyCode::Char('o') => {
                if let CursorEntry::Item(si, ii) = self.cursor_to_entry() {
                    let url = self.sections[si].items[ii].url.clone();
                    if !url.is_empty() {
                        if let Err(e) = open::that(&url) {
                            self.status_msg = format!("Failed to open browser: {e}");
                        } else {
                            self.status_msg = format!("Opened {url}");
                        }
                    }
                }
            }

            // Refresh
            KeyCode::Char('r') => {
                if let Err(e) = self.load_reviews() {
                    self.status_msg = format!("Refresh error: {e}");
                }
            }

            _ => {}
        }
        Ok(false)
    }

    fn handle_pr_detail_key(&mut self, key: KeyEvent) -> Result<bool> {
        match key.code {
            KeyCode::Char('q') | KeyCode::Esc | KeyCode::Char('h') | KeyCode::Backspace => {
                self.view = View::Sections;
                self.status_msg = format!("{} sections", self.sections.len());
            }

            // Scroll diff/content
            KeyCode::Char('j') | KeyCode::Down => {
                self.pr_scroll = self.pr_scroll.saturating_add(1);
            }
            KeyCode::Char('k') | KeyCode::Up => {
                self.pr_scroll = self.pr_scroll.saturating_sub(1);
            }
            KeyCode::Char('d') => {
                self.pr_scroll = self.pr_scroll.saturating_add(20);
            }
            KeyCode::Char('u') => {
                self.pr_scroll = self.pr_scroll.saturating_sub(20);
            }
            KeyCode::Char('g') => {
                self.pr_scroll = 0;
            }
            KeyCode::Char('G') => {
                let total = self.pr_content.lines().count() as u16;
                self.pr_scroll = total.saturating_sub(10);
            }

            // Open in browser
            KeyCode::Char('o') => {
                if let Some(ref m) = self.pr_metadata {
                    let url = &m.url;
                    if !url.is_empty() {
                        if let Err(e) = open::that(url) {
                            self.status_msg = format!("Failed to open browser: {e}");
                        } else {
                            self.status_msg = format!("Opened {url}");
                        }
                    }
                }
            }

            // Sync / refresh from GitHub
            KeyCode::Char('r') => {
                if let Some(ref m) = self.pr_metadata.clone() {
                    let owner = m.author.clone(); // We'll need actual owner
                    // Unfortunately metadata doesn't have owner, re-derive from sections
                    if let Some((o, r, n)) = self.find_pr_owner_repo(m.number) {
                        self.status_msg = "Syncing PR...".into();
                        match self.rpc.call(
                            "SyncPR",
                            serde_json::json!({
                                "Owner": o,
                                "Repo": r,
                                "Number": n,
                            }),
                        ) {
                            Ok(result) => {
                                if let Ok(reply) = serde_json::from_value::<GetPRReply>(result) {
                                    self.pr_metadata = reply.metadata;
                                    self.pr_diff = reply.diff;
                                    self.pr_comments = reply.comments;
                                    self.pr_reviews = reply.reviews;
                                    self.pr_content = reply.content;
                                    self.status_msg = "PR synced".into();
                                }
                            }
                            Err(e) => {
                                self.status_msg = format!("Sync error: {e}");
                            }
                        }
                    }
                    drop(owner);
                }
            }

            _ => {}
        }
        Ok(false)
    }

    /// Find the owner/repo for a PR number from our cached sections.
    fn find_pr_owner_repo(&self, number: i32) -> Option<(String, String, i32)> {
        for section in &self.sections {
            for item in &section.items {
                if item.number == number {
                    return Some((item.owner.clone(), item.repo.clone(), item.number));
                }
            }
        }
        None
    }
}

/// What the cursor is pointing at in the flat sections list.
pub enum CursorEntry {
    SectionHeader,
    Item(usize, usize),
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Test helper to compute total list length from sections
    fn compute_list_len(sections: &[Section]) -> usize {
        sections.iter().map(|s| 1 + s.items.len()).sum()
    }

    #[test]
    fn test_section_grouping() {
        let items = vec![
            ReviewItem {
                section: "Needs Review".to_string(),
                priority: 1,
                status: "PENDING".to_string(),
                title: "PR 1".to_string(),
                owner: "test".to_string(),
                repo: "repo".to_string(),
                number: 1,
                author: "alice".to_string(),
                url: "https://example.com/1".to_string(),
                release_status: String::new(),
            },
            ReviewItem {
                section: "Needs Review".to_string(),
                priority: 1,
                status: "PENDING".to_string(),
                title: "PR 2".to_string(),
                owner: "test".to_string(),
                repo: "repo".to_string(),
                number: 2,
                author: "bob".to_string(),
                url: "https://example.com/2".to_string(),
                release_status: String::new(),
            },
            ReviewItem {
                section: "In Progress".to_string(),
                priority: 2,
                status: "IN_PROGRESS".to_string(),
                title: "PR 3".to_string(),
                owner: "test".to_string(),
                repo: "repo".to_string(),
                number: 3,
                author: "charlie".to_string(),
                url: "https://example.com/3".to_string(),
                release_status: String::new(),
            },
        ];

        let mut sections: Vec<Section> = Vec::new();
        for item in items {
            if let Some(sec) = sections.iter_mut().find(|s| s.name == item.section) {
                sec.items.push(item);
            } else {
                let name = item.section.clone();
                let priority = item.priority;
                sections.push(Section {
                    name,
                    priority,
                    items: vec![item],
                });
            }
        }
        sections.sort_by(|a, b| a.priority.cmp(&b.priority).then(a.name.cmp(&b.name)));

        assert_eq!(sections.len(), 2);
        assert_eq!(sections[0].name, "Needs Review");
        assert_eq!(sections[0].items.len(), 2);
        assert_eq!(sections[1].name, "In Progress");
        assert_eq!(sections[1].items.len(), 1);
    }

    #[test]
    fn test_list_len_calculation() {
        let sections = vec![
            Section {
                name: "Section A".to_string(),
                priority: 1,
                items: vec![
                    ReviewItem {
                        section: "Section A".to_string(),
                        priority: 1,
                        status: "PENDING".to_string(),
                        title: "PR 1".to_string(),
                        owner: "test".to_string(),
                        repo: "repo".to_string(),
                        number: 1,
                        author: "alice".to_string(),
                        url: "https://example.com/1".to_string(),
                        release_status: String::new(),
                    },
                    ReviewItem {
                        section: "Section A".to_string(),
                        priority: 1,
                        status: "PENDING".to_string(),
                        title: "PR 2".to_string(),
                        owner: "test".to_string(),
                        repo: "repo".to_string(),
                        number: 2,
                        author: "bob".to_string(),
                        url: "https://example.com/2".to_string(),
                        release_status: String::new(),
                    },
                ],
            },
            Section {
                name: "Section B".to_string(),
                priority: 2,
                items: vec![ReviewItem {
                    section: "Section B".to_string(),
                    priority: 2,
                    status: "APPROVED".to_string(),
                    title: "PR 3".to_string(),
                    owner: "test".to_string(),
                    repo: "repo".to_string(),
                    number: 3,
                    author: "charlie".to_string(),
                    url: "https://example.com/3".to_string(),
                    release_status: String::new(),
                }],
            },
        ];

        // list_len = 1 (header A) + 2 (items) + 1 (header B) + 1 (item) = 5
        let len = compute_list_len(&sections);
        assert_eq!(len, 5);
    }

    #[test]
    fn test_cursor_navigation_bounds() {
        let mut cursor = 0;
        let list_len = 5;

        // Navigate down
        if list_len > 0 && cursor < list_len - 1 {
            cursor += 1;
        }
        assert_eq!(cursor, 1);

        // Navigate to end
        cursor = list_len - 1;
        if list_len > 0 && cursor < list_len - 1 {
            cursor += 1;
        }
        assert_eq!(cursor, list_len - 1); // Should not move

        // Navigate up
        if cursor > 0 {
            cursor -= 1;
        }
        assert_eq!(cursor, list_len - 2);

        // Navigate to start
        cursor = 0;
        if cursor > 0 {
            cursor -= 1;
        }
        assert_eq!(cursor, 0); // Should not move
    }
}
