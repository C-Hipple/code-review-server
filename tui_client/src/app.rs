use anyhow::Result;
use crossterm::event::{KeyCode, KeyEvent};
use ratatui::widgets::ListState;
use std::collections::HashMap;
use std::sync::mpsc::{self, Receiver};
use std::sync::{Arc, Mutex};

use crate::rpc::RpcClient;
use crate::types::{
    GetAdjacentPRReply, GetPRReply, GetPluginOutputReply, GetReviewsReply, ListPluginsReply,
    PRMetadata, PluginResult, ReviewItem,
};

/// Which screen the user is viewing.
#[derive(Debug, Clone, PartialEq)]
pub enum View {
    /// Sections list with PR items
    Sections,
    /// PR detail view
    PRDetail,
    /// Plugin selector (pick one plugin to view)
    PluginSelector,
    /// Plugin output (single plugin or all)
    PluginOutput,
}

/// A section header with its contained items.
#[derive(Debug, Clone)]
pub struct Section {
    pub name: String,
    pub priority: i32,
    pub items: Vec<ReviewItem>,
    pub collapsed: bool,
}

/// Result carried from a background PR load thread.
pub struct LoadedPR {
    pub reply: GetPRReply,
    pub owner: String,
    pub repo: String,
}

/// Application state.
pub struct App {
    pub rpc: Arc<Mutex<RpcClient>>,
    pub view: View,

    // -- Sections view --
    pub sections: Vec<Section>,
    /// Flat index across all rows (section headers + items)
    pub list_cursor: usize,
    /// Total number of rows in the flat list
    pub list_len: usize,
    /// Ratatui list state — tracks scroll offset automatically
    pub list_state: ListState,

    // -- PR detail view --
    pub pr_owner: String,
    pub pr_repo: String,
    pub pr_metadata: Option<PRMetadata>,
    pub pr_diff: String,
    pub pr_comments: Vec<crate::types::CommentJSON>,
    pub pr_reviews: Vec<crate::types::ReviewJSON>,
    pub pr_scroll: u16,
    pub pr_content: String,

    // -- Plugin views --
    /// Cached list of configured plugin names
    pub plugins: Vec<String>,
    /// Fetched plugin output keyed by plugin name
    pub plugin_output: HashMap<String, PluginResult>,
    /// Pre-built content string for PluginOutput view
    pub plugin_content: String,
    /// Scroll position for PluginOutput view
    pub plugin_scroll: u16,
    /// Cursor position in PluginSelector view
    pub plugin_cursor: usize,
    /// Which plugin is shown in PluginOutput (None = all)
    pub active_plugin: Option<String>,

    // Status / error bar
    pub status_msg: String,

    // Async PR loading
    pub loading: bool,
    pub pr_load_rx: Option<Receiver<Result<LoadedPR>>>,
}

impl App {
    pub fn new() -> Result<Self> {
        let rpc = Arc::new(Mutex::new(RpcClient::new()?));
        let mut list_state = ListState::default();
        list_state.select(Some(0));
        Ok(Self {
            rpc,
            view: View::Sections,
            sections: Vec::new(),
            list_cursor: 0,
            list_len: 0,
            list_state,
            pr_owner: String::new(),
            pr_repo: String::new(),
            pr_metadata: None,
            pr_diff: String::new(),
            pr_comments: Vec::new(),
            pr_reviews: Vec::new(),
            pr_scroll: 0,
            pr_content: String::new(),
            plugins: Vec::new(),
            plugin_output: HashMap::new(),
            plugin_content: String::new(),
            plugin_scroll: 0,
            plugin_cursor: 0,
            active_plugin: None,
            status_msg: String::from("Loading..."),
            loading: false,
            pr_load_rx: None,
        })
    }

    /// Fetch all review sections from the server.
    pub fn load_reviews(&mut self) -> Result<()> {
        self.status_msg = "Fetching reviews...".into();
        let result = self
            .rpc
            .lock()
            .unwrap()
            .call("GetAllReviews", serde_json::json!({}))?;
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
                    collapsed: false,
                });
            }
        }
        // Sort sections by priority then name
        self.sections
            .sort_by(|a, b| a.priority.cmp(&b.priority).then(a.name.cmp(&b.name)));

        self.recompute_list_len();
        self.list_cursor = 0;
        self.sync_list_state();
    }

    /// Recompute `list_len` from sections, respecting collapsed state.
    fn recompute_list_len(&mut self) {
        self.list_len = self
            .sections
            .iter()
            .map(|s| 1 + if s.collapsed { 0 } else { s.items.len() })
            .sum();
    }

    /// Start an async load of a PR. Returns immediately; check `check_pr_load` for results.
    /// View stays as Sections until the load succeeds.
    fn start_load_pr(&mut self, owner: String, repo: String, number: i32) {
        self.status_msg = format!("Loading PR #{number}...");
        self.loading = true;

        let rpc = Arc::clone(&self.rpc);
        let (tx, rx) = mpsc::channel();
        self.pr_load_rx = Some(rx);

        std::thread::spawn(move || {
            let result = (|| -> Result<LoadedPR> {
                let raw = rpc.lock().unwrap().call(
                    "GetPR",
                    serde_json::json!({
                        "Owner": &owner,
                        "Repo": &repo,
                        "Number": number,
                    }),
                )?;
                let reply: GetPRReply = serde_json::from_value(raw)?;
                Ok(LoadedPR { reply, owner, repo })
            })();
            tx.send(result).ok();
        });
    }

    /// Start an async load of the adjacent (next/previous) PR relative to the current one.
    fn start_load_adjacent_pr(&mut self, previous: bool) {
        let (number, owner, repo) = match &self.pr_metadata {
            Some(m) => (m.number, self.pr_owner.clone(), self.pr_repo.clone()),
            None => return,
        };
        let direction = if previous { "previous" } else { "next" };
        self.status_msg = format!("Loading {direction} PR...");
        self.loading = true;

        let rpc = Arc::clone(&self.rpc);
        let (tx, rx) = mpsc::channel();
        self.pr_load_rx = Some(rx);

        std::thread::spawn(move || {
            let result = (|| -> Result<LoadedPR> {
                let raw = rpc.lock().unwrap().call(
                    "GetAdjacentPR",
                    serde_json::json!({
                        "Owner": &owner,
                        "Repo": &repo,
                        "Number": number,
                        "Previous": previous,
                    }),
                )?;
                let reply: GetAdjacentPRReply = serde_json::from_value(raw)?;
                let adj_owner = reply.adjacent_owner.clone();
                let adj_repo = reply.adjacent_repo.clone();
                let pr_reply = GetPRReply {
                    okay: reply.okay,
                    content: reply.content,
                    metadata: reply.metadata,
                    diff: reply.diff,
                    comments: reply.comments,
                    outdated_comments: reply.outdated_comments,
                    reviews: reply.reviews,
                };
                Ok(LoadedPR {
                    reply: pr_reply,
                    owner: adj_owner,
                    repo: adj_repo,
                })
            })();
            tx.send(result).ok();
        });
    }

    /// Poll for a completed async PR load. Returns true if state changed.
    pub fn check_pr_load(&mut self) -> bool {
        let result = match self.pr_load_rx {
            None => return false,
            Some(ref rx) => match rx.try_recv() {
                Ok(r) => r,
                Err(mpsc::TryRecvError::Empty) => return false,
                Err(mpsc::TryRecvError::Disconnected) => {
                    self.pr_load_rx = None;
                    self.loading = false;
                    return false;
                }
            },
        };
        self.pr_load_rx = None;
        self.loading = false;
        match result {
            Ok(loaded) => self.apply_loaded_pr(loaded),
            Err(e) => {
                self.status_msg = format!("Error loading PR: {e}");
            }
        }
        true
    }

    fn apply_loaded_pr(&mut self, loaded: LoadedPR) {
        let reply = loaded.reply;
        self.pr_owner = loaded.owner;
        self.pr_repo = loaded.repo;
        self.pr_metadata = reply.metadata;
        self.pr_diff = reply.diff;
        self.pr_comments = reply.comments;
        self.pr_reviews = reply.reviews;
        self.pr_content = reply.content;
        self.pr_scroll = 0;
        // Clear stale plugin data for the new PR
        self.plugin_output.clear();
        self.plugin_content.clear();
        self.view = View::PRDetail;
        if let Some(ref m) = self.pr_metadata {
            self.status_msg = format!("PR #{}: {}", m.number, m.title);
        }
    }

    /// Sync `list_state` selection to `list_cursor` so the List widget scrolls correctly.
    fn sync_list_state(&mut self) {
        self.list_state.select(Some(self.list_cursor));
    }

    /// Map the flat cursor position to (section_index, item_index_within_section) or just a section header.
    pub fn cursor_to_entry(&self) -> CursorEntry {
        let mut pos = 0;
        for (si, section) in self.sections.iter().enumerate() {
            if pos == self.list_cursor {
                return CursorEntry::SectionHeader(si);
            }
            pos += 1;
            if !section.collapsed {
                for ii in 0..section.items.len() {
                    if pos == self.list_cursor {
                        return CursorEntry::Item(si, ii);
                    }
                    pos += 1;
                }
            }
        }
        CursorEntry::SectionHeader(0)
    }

    /// Toggle collapse on the section at the given index.
    fn toggle_section(&mut self, si: usize) {
        self.sections[si].collapsed = !self.sections[si].collapsed;
        self.recompute_list_len();
        // Clamp cursor if it's now past the end
        if self.list_len > 0 && self.list_cursor >= self.list_len {
            self.list_cursor = self.list_len - 1;
        }
        self.sync_list_state();
    }

    /// Handle a key event. Returns true if the app should quit.
    pub fn handle_key(&mut self, key: KeyEvent) -> Result<bool> {
        match self.view {
            View::Sections => self.handle_sections_key(key),
            View::PRDetail => self.handle_pr_detail_key(key),
            View::PluginSelector => self.handle_plugin_selector_key(key),
            View::PluginOutput => self.handle_plugin_output_key(key),
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
                    self.sync_list_state();
                }
            }
            KeyCode::Char('k') | KeyCode::Up => {
                if self.list_cursor > 0 {
                    self.list_cursor -= 1;
                    self.sync_list_state();
                }
            }
            KeyCode::Char('g') => {
                self.list_cursor = 0;
                self.sync_list_state();
            }
            KeyCode::Char('G') => {
                if self.list_len > 0 {
                    self.list_cursor = self.list_len - 1;
                    self.sync_list_state();
                }
            }
            KeyCode::Char('d') => {
                // Half-page down
                let jump = 10;
                self.list_cursor = (self.list_cursor + jump).min(self.list_len.saturating_sub(1));
                self.sync_list_state();
            }
            KeyCode::Char('u') => {
                // Half-page up
                let jump = 10;
                self.list_cursor = self.list_cursor.saturating_sub(jump);
                self.sync_list_state();
            }

            // Toggle section collapse
            KeyCode::Tab => {
                if let CursorEntry::SectionHeader(si) = self.cursor_to_entry() {
                    self.toggle_section(si);
                }
            }

            // Open PR detail or expand collapsed section
            KeyCode::Enter | KeyCode::Char('l') => {
                match self.cursor_to_entry() {
                    CursorEntry::SectionHeader(si) => {
                        // Enter on section header toggles collapse
                        self.toggle_section(si);
                    }
                    CursorEntry::Item(si, ii) => {
                        let item = &self.sections[si].items[ii];
                        let owner = item.owner.clone();
                        let repo = item.repo.clone();
                        let number = item.number;
                        self.start_load_pr(owner, repo, number);
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
                    if let Some((o, r, n)) = self.find_pr_owner_repo(m.number) {
                        self.start_load_pr(o, r, n);
                    }
                }
            }

            // Navigate to next/previous PR
            KeyCode::Char(']') => {
                self.start_load_adjacent_pr(false);
            }
            KeyCode::Char('[') => {
                self.start_load_adjacent_pr(true);
            }

            // Plugin selector: pick a single plugin to view
            KeyCode::Char('p') => match self.ensure_plugins_loaded() {
                Ok(()) => {
                    if self.plugins.is_empty() {
                        self.status_msg = "No plugins configured".into();
                    } else {
                        self.plugin_cursor = 0;
                        self.view = View::PluginSelector;
                        self.status_msg = "Select a plugin (Enter: view, q: back)".into();
                    }
                }
                Err(e) => self.status_msg = format!("Error loading plugins: {e}"),
            },

            // All plugin output
            KeyCode::Char('P') => {
                self.status_msg = "Fetching plugin output...".into();
                match self.fetch_plugin_output() {
                    Ok(()) => {
                        self.active_plugin = None;
                        self.build_plugin_content();
                        self.plugin_scroll = 0;
                        self.view = View::PluginOutput;
                        self.status_msg = "All plugin output (q/h: back)".into();
                    }
                    Err(e) => self.status_msg = format!("Error fetching plugin output: {e}"),
                }
            }

            _ => {}
        }
        Ok(false)
    }

    /// Fetch the list of configured plugins, caching after the first call.
    fn ensure_plugins_loaded(&mut self) -> Result<()> {
        if !self.plugins.is_empty() {
            return Ok(());
        }
        let raw = self
            .rpc
            .lock()
            .unwrap()
            .call("ListPlugins", serde_json::json!({}))?;
        let reply: ListPluginsReply = serde_json::from_value(raw)?;
        self.plugins = reply.plugins.into_iter().map(|p| p.name).collect();
        Ok(())
    }

    /// Fetch plugin output for the current PR and store it in `plugin_output`.
    fn fetch_plugin_output(&mut self) -> Result<()> {
        let number = match &self.pr_metadata {
            Some(m) => m.number,
            None => return Ok(()),
        };
        let owner = self.pr_owner.clone();
        let repo = self.pr_repo.clone();
        let raw = self.rpc.lock().unwrap().call(
            "GetPluginOutput",
            serde_json::json!({
                "Owner": owner,
                "Repo": repo,
                "Number": number,
            }),
        )?;
        let reply: GetPluginOutputReply = serde_json::from_value(raw)?;
        self.plugin_output = reply.output;
        Ok(())
    }

    /// Build `plugin_content` from `plugin_output` and `active_plugin`.
    pub fn build_plugin_content(&mut self) {
        if self.plugin_output.is_empty() {
            self.plugin_content =
                "No plugin output available yet. Plugins may still be running.".into();
            return;
        }
        match &self.active_plugin.clone() {
            Some(name) => {
                if let Some(result) = self.plugin_output.get(name) {
                    self.plugin_content = format!("Status: {}\n\n{}", result.status, result.result);
                } else {
                    self.plugin_content = format!("No output found for plugin '{name}'.");
                }
            }
            None => {
                let mut out = String::new();
                let mut keys: Vec<String> = self.plugin_output.keys().cloned().collect();
                keys.sort();
                for name in &keys {
                    let result = &self.plugin_output[name];
                    out.push_str(&format!("## {}\n", name));
                    out.push_str(&format!("Status: {}\n\n", result.status));
                    out.push_str(&result.result);
                    out.push_str("\n\n");
                }
                self.plugin_content = out;
            }
        }
    }

    fn handle_plugin_selector_key(&mut self, key: KeyEvent) -> Result<bool> {
        match key.code {
            KeyCode::Char('q') | KeyCode::Esc | KeyCode::Char('h') | KeyCode::Backspace => {
                self.view = View::PRDetail;
                if let Some(ref m) = self.pr_metadata {
                    self.status_msg = format!("PR #{}: {}", m.number, m.title);
                }
            }

            KeyCode::Char('j') | KeyCode::Down => {
                if !self.plugins.is_empty() && self.plugin_cursor < self.plugins.len() - 1 {
                    self.plugin_cursor += 1;
                }
            }
            KeyCode::Char('k') | KeyCode::Up => {
                if self.plugin_cursor > 0 {
                    self.plugin_cursor -= 1;
                }
            }
            KeyCode::Char('g') => {
                self.plugin_cursor = 0;
            }
            KeyCode::Char('G') => {
                if !self.plugins.is_empty() {
                    self.plugin_cursor = self.plugins.len() - 1;
                }
            }

            KeyCode::Enter | KeyCode::Char('l') => {
                if let Some(name) = self.plugins.get(self.plugin_cursor).cloned() {
                    self.status_msg = format!("Fetching output for '{name}'...");
                    match self.fetch_plugin_output() {
                        Ok(()) => {
                            self.active_plugin = Some(name.clone());
                            self.build_plugin_content();
                            self.plugin_scroll = 0;
                            self.view = View::PluginOutput;
                            self.status_msg = format!("Plugin: {name} (q/h: back)");
                        }
                        Err(e) => self.status_msg = format!("Error: {e}"),
                    }
                }
            }

            _ => {}
        }
        Ok(false)
    }

    fn handle_plugin_output_key(&mut self, key: KeyEvent) -> Result<bool> {
        match key.code {
            KeyCode::Char('q') | KeyCode::Esc | KeyCode::Char('h') | KeyCode::Backspace => {
                self.view = View::PRDetail;
                if let Some(ref m) = self.pr_metadata {
                    self.status_msg = format!("PR #{}: {}", m.number, m.title);
                }
            }

            KeyCode::Char('j') | KeyCode::Down => {
                self.plugin_scroll = self.plugin_scroll.saturating_add(1);
            }
            KeyCode::Char('k') | KeyCode::Up => {
                self.plugin_scroll = self.plugin_scroll.saturating_sub(1);
            }
            KeyCode::Char('d') => {
                self.plugin_scroll = self.plugin_scroll.saturating_add(20);
            }
            KeyCode::Char('u') => {
                self.plugin_scroll = self.plugin_scroll.saturating_sub(20);
            }
            KeyCode::Char('g') => {
                self.plugin_scroll = 0;
            }
            KeyCode::Char('G') => {
                let total = self.plugin_content.lines().count() as u16;
                self.plugin_scroll = total.saturating_sub(10);
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
    SectionHeader(usize),
    Item(usize, usize),
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Test helper to compute total list length from sections
    fn compute_list_len(sections: &[Section]) -> usize {
        sections
            .iter()
            .map(|s| 1 + if s.collapsed { 0 } else { s.items.len() })
            .sum()
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
                    collapsed: false,
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
                collapsed: false,
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
                collapsed: false,
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
    fn test_list_len_with_collapsed_section() {
        let sections = vec![
            Section {
                name: "Section A".to_string(),
                priority: 1,
                collapsed: true, // collapsed!
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
                collapsed: false,
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

        // list_len = 1 (header A, collapsed) + 1 (header B) + 1 (item) = 3
        let len = compute_list_len(&sections);
        assert_eq!(len, 3);
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
