;;; crs-client.el --- JSON-RPC client for codereviewserver -*- lexical-binding: t; -*-

;; Author: Chris Hipple
;; Version: 0.1.0
;; Package-Requires: ((emacs "25.1"))

;; SPDX-License-Identifier: GPL-3.0+

;;; Commentary:

;; This package provides JSON-RPC client functionality for crs (codereviewserver).
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


;;; Keybindings
;;
;; All keybindings are defined here, after every module has been required, so
;; that every command and mode keymap is loaded before it is bound.  Each mode
;; declares an empty keymap next to its `define-derived-mode'; the bindings are
;; installed below.  Non-evil bindings populate the keymaps directly, while
;; evil bindings are deferred until evil itself is loaded.

(define-keymap :keymap crs-list-mode-map
  "TAB" #'crs-list-toggle-heading
  "<tab>" #'crs-list-toggle-heading
  "<backtab>" #'crs-list-cycle-global
  "RET" #'crs-start-review-at-point
  "<return>" #'crs-start-review-at-point
  "g" #'crs-refresh-reviews
  "q" #'quit-window)

(define-keymap :keymap my-code-review-mode-map
  "TAB" #'crs-toggle-section
  "<tab>" #'crs-toggle-section
  "<backtab>" #'crs-collapse-all-sections
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
  "H" #'crs-toggle-comments
  "A" #'crs-toggle-annotations
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

(define-keymap :keymap comment-edit-mode-map
  "C-c C-c" #'crs-submit-comment
  "C-c C-k" #'crs-abort-comment)

(define-keymap :keymap crs-plugin-output-mode-map
  "r" #'crs-refresh-plugin-output
  "q" #'crs-quit-plugin-output
  "w" #'crs-wash-plugin-output
  "D" #'crs-run-on-demand-plugin
  "R" #'crs-rerun-plugin)

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
    "q" #'quit-window)
  ;; my-code-review-mode: normal state
  (evil-define-key 'normal my-code-review-mode-map
    (kbd "TAB") #'crs-toggle-section
    (kbd "<tab>") #'crs-toggle-section
    (kbd "<backtab>") #'crs-collapse-all-sections
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
    "H" #'crs-toggle-comments
    "A" #'crs-toggle-annotations
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
    "H" #'crs-toggle-comments
    "A" #'crs-toggle-annotations
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
    "w" #'crs-wash-plugin-output))

(provide 'crs-client)

;;; crs-client.el ends here
