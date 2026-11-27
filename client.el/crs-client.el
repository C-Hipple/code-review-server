;;; crs-client.el --- JSON-RPC client for crs -*- lexical-binding: t; -*-

;; Author: Chris Hipple
;; Version: 0.1.0
;; Package-Requires: ((emacs "25.1"))

;; SPDX-License-Identifier: GPL-3.0+

;;; Commentary:

;; This package provides JSON-RPC client functionality for crs, the
;; code-review-server binary (called codereviewserver before the module was
;; renamed).
;; It allows starting the server process and making RPC calls to it.

;;; Code:

(require 'crs-vars)
(require 'crs-html)
(require 'crs-rpc)
(require 'crs-render)
(require 'crs-list-mode)
(require 'crs-review)
(require 'crs-comments)
(require 'crs-review-actions)
(require 'crs-plugins)
(require 'crs-ai)
(require 'crs-diagram)
(require 'crs-filtered-diff)
(require 'crs-chat)
(require 'crs-discussion)


;;; Keybindings
;;
;; All keybindings are defined here, after every module has been required, so
;; that every command and mode keymap is loaded before it is bound.  Each mode
;; declares an empty keymap next to its `define-derived-mode'; the bindings are
;; installed below.  Non-evil bindings populate the keymaps directly, while
;; evil bindings are deferred until evil itself is loaded.
;;
;; Because the bindings live here, every entry point must load this file:
;; the modules' autoload cookies name "crs-client", not their own file, as in
;; `;;;###autoload (autoload 'crs-start-server "crs-client" nil t)'.  A plain
;; cookie would autoload only the defining module (`crs-start-server' would
;; load crs-rpc alone), leaving these keymaps empty and any
;; `with-eval-after-load' on crs-client unrun.  `crs-test-autoloads-load-client'
;; enforces this.

(define-keymap :keymap crs-list-mode-map
  "TAB" #'crs-list-toggle-heading
  "<tab>" #'crs-list-toggle-heading
  "<backtab>" #'crs-list-cycle-global
  "RET" #'crs-start-review-at-point
  "<return>" #'crs-start-review-at-point
  "g" #'crs-refresh-reviews
  "M" #'crs-show-change-diagram
  "q" #'quit-window)

(define-keymap :keymap my-code-review-mode-map
  "TAB" #'crs-toggle-section
  "<tab>" #'crs-toggle-section
  "<backtab>" #'crs-collapse-all-sections
  "a" #'crs-add-annotation-as-comment
  "c" #'crs-add-or-edit-comment
  "d" #'crs-delete-local-comment
  "C-c C-c" #'crs-submit-review
  ;; "ra" #'crs-approve-review
  ;; "rc" #'crs-comment-review
  ;; "rr" #'crs-request-changes-review
  "g" #'crs-sync-pr
  "p" #'crs-get-plugin-output
  "P" #'crs-get-single-plugin-output
  "D" #'crs-run-on-demand-plugin
  "R" #'crs-rerun-plugin
  "C" #'crs-get-ai-output
  "M" #'crs-show-change-diagram
  "t" #'crs-chat
  "H" #'crs-toggle-comments
  "s" #'crs-show-discussion
  "L" #'crs-copy-github-line-link
  "A" #'crs-toggle-annotations
  "F" #'crs-show-filtered-diff
  "f" #'crs-set-review-feedback
  "RET" #'crs-visit-file
  "<return>" #'crs-visit-file
  "O" #'crs-expand-hunk-before
  "o" #'crs-expand-hunk-after
  "q" #'quit-window
  "]" #'crs-next-pr
  "[" #'crs-prev-pr
  "<" #'crs-expand-hunk-before
  ">" #'crs-expand-hunk-after)

(define-keymap :keymap crs-outdated-comments-mode-map
  "q" #'crs-quit-outdated-comments)

;; The discussion buffer is a special-mode buffer, so q quits it as usual.
(define-keymap :keymap crs-discussion-mode-map
  "RET" #'crs-discussion-visit-thread
  "<return>" #'crs-discussion-visit-thread
  "n" #'crs-discussion-next-thread
  "p" #'crs-discussion-previous-thread
  "]" #'crs-discussion-next-thread
  "[" #'crs-discussion-previous-thread
  "TAB" #'crs-toggle-section
  "<tab>" #'crs-toggle-section
  "g" #'crs-discussion-sync)

(define-keymap :keymap comment-edit-mode-map
  "C-c C-c" #'crs-submit-comment
  "C-c C-k" #'crs-abort-comment)

(define-keymap :keymap crs-plugin-output-mode-map
  "r" #'crs-refresh-plugin-output
  "q" #'crs-quit-plugin-output
  "w" #'crs-wash-plugin-output
  "D" #'crs-run-on-demand-plugin
  "R" #'crs-rerun-plugin)

(define-keymap :keymap crs-ai-output-mode-map
  "r" #'crs-ai-refresh
  "R" #'crs-ai-rerun
  "q" #'crs-quit-ai-output)

(define-keymap :keymap crs-diagram-view-mode-map
  "r" #'crs-ai-refresh
  "R" #'crs-ai-rerun
  "q" #'crs-quit-ai-output)

;; The chat buffer is editable below its line, so its keys are all C-c
;; ones, chosen to leave markdown-mode's useful ones alone: C-c C-f / C-c
;; C-b still step between turns, C-c C-o follows a link.  The session keys:
;; C-c C-v views the PR's sessions, C-c C-y forks this one (a Y is a fork),
;; C-c C-t retitles it, and C-c { / C-c } step to the previous / next.
(define-keymap :keymap crs-chat-mode-map
  "C-c C-c" #'crs-chat-send
  "C-c RET" #'crs-chat-send
  "C-c C-k" #'crs-chat-stop
  "C-c C-n" #'crs-chat-new
  "C-c C-y" #'crs-chat-fork
  "C-c C-t" #'crs-chat-rename
  "C-c C-w" #'crs-chat-switch
  "C-c C-v" #'crs-chat-sessions
  "C-c {" #'crs-chat-previous-session
  "C-c }" #'crs-chat-next-session
  "C-c C-e" #'crs-chat-select-model
  "C-c C-r" #'crs-chat-refresh
  "C-c C-d" #'crs-chat-delete
  "C-c C-q" #'crs-chat-quit)

(define-keymap :keymap crs-chat-sessions-mode-map
  "RET" #'crs-chat-sessions-open
  "n" #'crs-chat-sessions-new
  "f" #'crs-chat-sessions-fork
  "R" #'crs-chat-sessions-rename
  "d" #'crs-chat-sessions-delete
  "D" #'crs-chat-sessions-delete)

(with-eval-after-load 'evil
  ;; Global normal-state leader bindings: available in any buffer/mode,
  ;; not only crs-list-mode.  Use `evil-global-set-key' rather than
  ;; `evil-define-key' with the `global' pseudo-keymap: the latter targets
  ;; a specific mode keymap and is unreliable for truly global bindings
  ;; across evil versions, which is why the `, r s' / `, r b' leaders
  ;; silently failed to bind.
  (evil-global-set-key 'normal (kbd ", r s") #'crs-start-review-at-point)
  (evil-global-set-key 'normal (kbd ", r b") #'crs-get-reviews)
  ;; crs-list-mode: normal state.  Without these, evil's normal-state
  ;; bindings shadow the plain `crs-list-mode-map' bindings, so the PR
  ;; list keys silently do nothing for evil users.
  (evil-define-key 'normal crs-list-mode-map
    (kbd "TAB") #'crs-list-toggle-heading
    (kbd "<tab>") #'crs-list-toggle-heading
    (kbd "<backtab>") #'crs-list-cycle-global
    (kbd "RET") #'crs-start-review-at-point
    (kbd "<return>") #'crs-start-review-at-point
    "g" #'crs-refresh-reviews
    "M" #'crs-show-change-diagram
    "q" #'quit-window)
  ;; my-code-review-mode: normal state
  (evil-define-key 'normal my-code-review-mode-map
    (kbd "TAB") #'crs-toggle-section
    (kbd "<tab>") #'crs-toggle-section
    (kbd "<backtab>") #'crs-collapse-all-sections
    "a" #'crs-add-annotation-as-comment
    "c" #'crs-add-or-edit-comment
    "d" #'crs-delete-local-comment
    (kbd "C-c C-c") #'crs-submit-review
    "ra" #'crs-approve-review
    "rc" #'crs-comment-review
    "rr" #'crs-request-changes-review
    "rg" #'crs-sync-pr
    "rf" #'crs-set-review-feedback
    "p" #'crs-get-plugin-output
    "P" #'crs-get-single-plugin-output
    "D" #'crs-run-on-demand-plugin
    "R" #'crs-rerun-plugin
    "C" #'crs-get-ai-output
    "M" #'crs-show-change-diagram
    "t" #'crs-chat
    "H" #'crs-toggle-comments
    "s" #'crs-show-discussion
    "L" #'crs-copy-github-line-link
    "A" #'crs-toggle-annotations
    "F" #'crs-show-filtered-diff
    "f" #'crs-set-review-feedback
    (kbd "RET") #'crs-visit-file
    "O" #'crs-expand-hunk-before
    "o" #'crs-expand-hunk-after
    "q" #'quit-window
    "]" #'crs-next-pr
    "[" #'crs-prev-pr
    (kbd "SPC C-R") #'crs-get-rate-limit-status)
  ;; my-code-review-mode: visual state
  (evil-define-key 'visual my-code-review-mode-map
    (kbd "TAB") #'crs-toggle-section
    (kbd "<tab>") #'crs-toggle-section
    (kbd "<backtab>") #'crs-collapse-all-sections
    "a" #'crs-add-annotation-as-comment
    "c" #'crs-add-or-edit-comment
    "d" #'crs-delete-local-comment
    (kbd "C-c C-c") #'crs-submit-review
    "ra" #'crs-approve-review
    "rc" #'crs-comment-review
    "rr" #'crs-request-changes-review
    "rg" #'crs-sync-pr
    "rf" #'crs-set-review-feedback
    "p" #'crs-get-plugin-output
    "P" #'crs-get-single-plugin-output
    "D" #'crs-run-on-demand-plugin
    "R" #'crs-rerun-plugin
    "C" #'crs-get-ai-output
    "M" #'crs-show-change-diagram
    "t" #'crs-chat
    "H" #'crs-toggle-comments
    "s" #'crs-show-discussion
    "L" #'crs-copy-github-line-link
    "A" #'crs-toggle-annotations
    "F" #'crs-show-filtered-diff
    "f" #'crs-set-review-feedback
    (kbd "RET") #'crs-visit-file
    "O" #'crs-expand-hunk-before
    "o" #'crs-expand-hunk-after
    "q" #'quit-window
    "]" #'crs-next-pr
    "[" #'crs-prev-pr
    (kbd "SPC C-R") #'crs-get-rate-limit-status)
  ;; my-code-review-mode: insert state
  (evil-define-key 'insert my-code-review-mode-map
    (kbd "C-c C-c") #'crs-submit-review)
  ;; crs-outdated-comments-mode
  (evil-define-key 'normal crs-outdated-comments-mode-map
    "q" #'crs-quit-outdated-comments)
  ;; crs-discussion-mode: a special-mode buffer, which evil may open in
  ;; motion state rather than normal.  ] / [ step between threads, as they
  ;; step between PRs in a review buffer.
  (evil-define-key '(normal motion) crs-discussion-mode-map
    (kbd "RET") #'crs-discussion-visit-thread
    (kbd "TAB") #'crs-toggle-section
    (kbd "<tab>") #'crs-toggle-section
    "]" #'crs-discussion-next-thread
    "[" #'crs-discussion-previous-thread
    "gr" #'crs-discussion-sync
    "q" #'quit-window)
  ;; comment-edit-mode
  (evil-define-key 'normal comment-edit-mode-map
    "C-c C-c" #'crs-submit-comment
    "C-c C-k" #'crs-abort-comment
    ", c" #'crs-submit-comment
    ", k" #'crs-abort-comment)
  (evil-define-key 'insert comment-edit-mode-map
    "C-c C-c" #'crs-submit-comment
    "C-c C-k" #'crs-abort-comment)
  (evil-define-key 'visual comment-edit-mode-map
    "C-c C-c" #'crs-submit-comment
    "C-c C-k" #'crs-abort-comment)
  ;; crs-plugin-output-mode
  (evil-define-key 'normal crs-plugin-output-mode-map
    "r" #'crs-refresh-plugin-output
    "q" #'crs-quit-plugin-output
    "D" #'crs-run-on-demand-plugin
    "R" #'crs-rerun-plugin
    "w" #'crs-wash-plugin-output)
  ;; crs-ai-output-mode
  (evil-define-key 'normal crs-ai-output-mode-map
    "r" #'crs-ai-refresh
    "R" #'crs-ai-rerun
    "q" #'crs-quit-ai-output)
  ;; crs-diagram-view-mode: a minor mode, since the diagram buffer's major
  ;; mode is mermaid-mode.
  (evil-define-minor-mode-key 'normal 'crs-diagram-view-mode
    "r" #'crs-ai-refresh
    "R" #'crs-ai-rerun
    "q" #'crs-quit-ai-output)
  ;; crs-chat-mode: write in insert state, as in a comment buffer; the
  ;; normal-state leader keys mirror the C-c ones.
  (evil-define-key 'normal crs-chat-mode-map
    (kbd "C-c C-c") #'crs-chat-send
    (kbd "C-c C-k") #'crs-chat-stop
    ", c" #'crs-chat-send
    ", k" #'crs-chat-stop
    ", n" #'crs-chat-new
    ", f" #'crs-chat-fork
    ", t" #'crs-chat-rename
    ", w" #'crs-chat-switch
    ", s" #'crs-chat-sessions
    ", [" #'crs-chat-previous-session
    ", ]" #'crs-chat-next-session
    ", e" #'crs-chat-select-model
    ", r" #'crs-chat-refresh
    ", d" #'crs-chat-delete
    "q" #'crs-chat-quit)
  (evil-define-key 'insert crs-chat-mode-map
    (kbd "C-c C-c") #'crs-chat-send
    (kbd "C-c C-k") #'crs-chat-stop)
  ;; The session list: a tabulated list, read in normal state.
  (evil-define-key 'normal crs-chat-sessions-mode-map
    (kbd "RET") #'crs-chat-sessions-open
    "n" #'crs-chat-sessions-new
    "f" #'crs-chat-sessions-fork
    "R" #'crs-chat-sessions-rename
    "d" #'crs-chat-sessions-delete
    "D" #'crs-chat-sessions-delete
    "gr" #'revert-buffer
    "q" #'quit-window))

(provide 'crs-client)

;;; crs-client.el ends here
