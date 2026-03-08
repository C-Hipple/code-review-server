use ratatui::{
    layout::{Constraint, Direction, Layout, Rect},
    style::{Color, Modifier, Style},
    text::{Line, Span},
    widgets::{Block, Borders, List, ListItem, Paragraph, Wrap},
    Frame,
};

use crate::app::{App, View};

pub fn draw(f: &mut Frame, app: &mut App) {
    let chunks = Layout::default()
        .direction(Direction::Vertical)
        .constraints([
            Constraint::Length(1), // title bar
            Constraint::Min(1),   // main content
            Constraint::Length(1), // status bar
            Constraint::Length(1), // help bar
        ])
        .split(f.area());

    draw_title_bar(f, chunks[0], app);

    match app.view {
        View::Sections => draw_sections(f, chunks[1], app),
        View::PRDetail => draw_pr_detail(f, chunks[1], app),
    }


    draw_status_bar(f, chunks[2], app);
    draw_help_bar(f, chunks[3], app);
}

fn draw_title_bar(f: &mut Frame, area: Rect, _app: &mut App) {
    let title = Paragraph::new(" Code Review Server — TUI")
        .style(Style::default().fg(Color::White).bg(Color::DarkGray).add_modifier(Modifier::BOLD));
    f.render_widget(title, area);
}

fn draw_status_bar(f: &mut Frame, area: Rect, app: &mut App) {
    let status = Paragraph::new(format!(" {}", app.status_msg))
        .style(Style::default().fg(Color::Yellow).bg(Color::DarkGray));
    f.render_widget(status, area);
}

fn draw_help_bar(f: &mut Frame, area: Rect, app: &mut App) {
    let help_text = match app.view {
        View::Sections => " j/k: navigate  Enter/l: open PR  o: open in browser  r: refresh  g/G: top/bottom  q: quit",
        View::PRDetail => " j/k: scroll  d/u: page down/up  g/G: top/bottom  o: open in browser  r: sync  q/Esc/h: back",
    };
    let help = Paragraph::new(help_text)
        .style(Style::default().fg(Color::Cyan).bg(Color::Black));
    f.render_widget(help, area);
}

fn draw_sections(f: &mut Frame, area: Rect, app: &mut App) {
    let mut items: Vec<ListItem> = Vec::new();
    let mut row_index = 0;

    for section in &app.sections {
        // Section header
        let is_selected = row_index == app.list_cursor;
        let header_style = if is_selected {
            Style::default()
                .fg(Color::White)
                .bg(Color::Blue)
                .add_modifier(Modifier::BOLD)
        } else {
            Style::default()
                .fg(Color::Cyan)
                .add_modifier(Modifier::BOLD)
        };
        let header_text = format!("▸ {} ({})", section.name, section.items.len());
        items.push(ListItem::new(Line::from(Span::styled(header_text, header_style))));
        row_index += 1;

        // PR items
        for item in &section.items {
            let is_selected = row_index == app.list_cursor;

            let status_color = match item.status.as_str() {
                "APPROVED" => Color::Green,
                "CHANGES_REQUESTED" => Color::Red,
                _ => Color::Yellow,
            };

            let line = if is_selected {
                Line::from(vec![
                    Span::styled("  ", Style::default().bg(Color::DarkGray)),
                    Span::styled(
                        format!("#{} ", item.number),
                        Style::default().fg(Color::White).bg(Color::DarkGray).add_modifier(Modifier::BOLD),
                    ),
                    Span::styled(
                        &item.title,
                        Style::default().fg(Color::White).bg(Color::DarkGray),
                    ),
                    Span::styled(
                        format!("  @{}", item.author),
                        Style::default().fg(Color::Gray).bg(Color::DarkGray),
                    ),
                    Span::styled(
                        format!("  {}/{}", item.owner, item.repo),
                        Style::default().fg(Color::Gray).bg(Color::DarkGray),
                    ),
                ])
            } else {
                Line::from(vec![
                    Span::raw("  "),
                    Span::styled(
                        format!("#{} ", item.number),
                        Style::default().fg(status_color).add_modifier(Modifier::BOLD),
                    ),
                    Span::styled(&item.title, Style::default().fg(Color::White)),
                    Span::styled(
                        format!("  @{}", item.author),
                        Style::default().fg(Color::DarkGray),
                    ),
                    Span::styled(
                        format!("  {}/{}", item.owner, item.repo),
                        Style::default().fg(Color::DarkGray),
                    ),
                ])
            };

            items.push(ListItem::new(line));
            row_index += 1;
        }
    }

    if items.is_empty() {
        let empty = Paragraph::new(" No reviews found. Press 'r' to refresh.")
            .style(Style::default().fg(Color::Gray))
            .block(Block::default().borders(Borders::ALL).title(" Reviews "));
        f.render_widget(empty, area);
        return;
    }

    let list = List::new(items)
        .block(Block::default().borders(Borders::ALL).title(" Reviews "))
        .highlight_style(ratatui::style::Style::default());
    f.render_stateful_widget(list, area, &mut app.list_state);
}

fn draw_pr_detail(f: &mut Frame, area: Rect, app: &mut App) {
    // Split into metadata panel and content panel
    let chunks = Layout::default()
        .direction(Direction::Vertical)
        .constraints([
            Constraint::Length(metadata_height(app)),
            Constraint::Min(1),
        ])
        .split(area);

    draw_pr_metadata(f, chunks[0], app);
    draw_pr_content(f, chunks[1], app);
}

fn metadata_height(app: &mut App) -> u16 {
    if app.pr_metadata.is_some() { 10 } else { 3 }
}

fn draw_pr_metadata(f: &mut Frame, area: Rect, app: &mut App) {
    let meta = match &app.pr_metadata {
        Some(m) => m,
        None => {
            let p = Paragraph::new(" No metadata available")
                .block(Block::default().borders(Borders::ALL).title(" PR "));
            f.render_widget(p, area);
            return;
        }
    };

    let state_color = match meta.state.as_str() {
        "open" => Color::Green,
        "closed" => Color::Red,
        "merged" => Color::Magenta,
        _ => Color::White,
    };

    let mut lines = vec![
        Line::from(vec![
            Span::styled(
                format!(" #{} ", meta.number),
                Style::default().fg(Color::Cyan).add_modifier(Modifier::BOLD),
            ),
            Span::styled(
                &meta.title,
                Style::default().fg(Color::White).add_modifier(Modifier::BOLD),
            ),
        ]),
        Line::from(vec![
            Span::raw("  State: "),
            Span::styled(&meta.state, Style::default().fg(state_color).add_modifier(Modifier::BOLD)),
            Span::raw(if meta.draft { " [DRAFT]" } else { "" }),
            Span::raw("  Author: "),
            Span::styled(&meta.author, Style::default().fg(Color::Yellow)),
        ]),
        Line::from(vec![
            Span::raw("  Branch: "),
            Span::styled(&meta.head_ref, Style::default().fg(Color::Green)),
            Span::raw(" → "),
            Span::styled(&meta.base_ref, Style::default().fg(Color::Red)),
        ]),
    ];

    if !meta.ci_status.is_empty() {
        let ci_color = if meta.ci_status.contains("success") || meta.ci_status.contains("passing") {
            Color::Green
        } else if meta.ci_status.contains("fail") {
            Color::Red
        } else {
            Color::Yellow
        };
        lines.push(Line::from(vec![
            Span::raw("  CI: "),
            Span::styled(&meta.ci_status, Style::default().fg(ci_color)),
        ]));
    }

    if !meta.approved_by.is_empty() {
        lines.push(Line::from(vec![
            Span::raw("  Approved by: "),
            Span::styled(meta.approved_by.join(", "), Style::default().fg(Color::Green)),
        ]));
    }

    if !meta.changes_requested_by.is_empty() {
        lines.push(Line::from(vec![
            Span::raw("  Changes requested by: "),
            Span::styled(meta.changes_requested_by.join(", "), Style::default().fg(Color::Red)),
        ]));
    }

    if !meta.labels.is_empty() {
        lines.push(Line::from(vec![
            Span::raw("  Labels: "),
            Span::styled(meta.labels.join(", "), Style::default().fg(Color::Magenta)),
        ]));
    }

    if !meta.reviewers.is_empty() {
        lines.push(Line::from(vec![
            Span::raw("  Reviewers: "),
            Span::styled(meta.reviewers.join(", "), Style::default().fg(Color::Cyan)),
        ]));
    }

    let paragraph = Paragraph::new(lines)
        .block(Block::default().borders(Borders::ALL).title(" PR Details "));
    f.render_widget(paragraph, area);
}

fn draw_pr_content(f: &mut Frame, area: Rect, app: &mut App) {
    let content = &app.pr_content;
    let lines: Vec<Line> = content
        .lines()
        .map(|line| {
            let style = if line.starts_with('+') && !line.starts_with("+++") {
                Style::default().fg(Color::Green)
            } else if line.starts_with('-') && !line.starts_with("---") {
                Style::default().fg(Color::Red)
            } else if line.starts_with("@@") {
                Style::default().fg(Color::Cyan)
            } else if line.starts_with("diff ") || line.starts_with("index ") {
                Style::default().fg(Color::Yellow)
            } else if line.starts_with("┌─") || line.starts_with("│") || line.starts_with("└─") {
                Style::default().fg(Color::Magenta)
            } else {
                Style::default()
            };
            Line::from(Span::styled(line, style))
        })
        .collect();

    let paragraph = Paragraph::new(lines)
        .block(Block::default().borders(Borders::ALL).title(" Content "))
        .wrap(Wrap { trim: false })
        .scroll((app.pr_scroll, 0));
    f.render_widget(paragraph, area);
}
