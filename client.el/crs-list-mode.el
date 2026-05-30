;;; crs-list-mode.el --- Major mode for the crs PR-list buffer. -*- lexical-binding: t; -*-

;;; Commentary:

;; Major mode for the crs PR-list buffer.

;;; Code:

(require 'crs-vars)
(require 'crs-html)
(require 'crs-rpc)

(declare-function crs-start-review-at-point "crs-review")
(declare-function crs-get-review "crs-review")

;;; --- crs-list-mode: lightweight major mode for the PR list buffer ---

(defface crs-list-heading-1-face
  '((t (:foreground "#cb4b16" :weight bold :height 1.4)))
  "Face for level-1 headings (* Section) in the PR list."
  :group 'crs)

(defface crs-list-heading-2-face
  '((t (:foreground "#268bd2" :weight bold :height 1.2)))
  "Face for level-2 headings (** PR) in the PR list."
  :group 'crs)

(defface crs-list-heading-3-face
  '((t (:foreground "#859900" :weight bold :height 1.1)))
  "Face for level-3 headings (*** sub-items) in the PR list."
  :group 'crs)

(defface crs-list-todo-face
  '((t (:foreground "#dc322f" :weight bold)))
  "Face for the TODO keyword in the PR list."
  :group 'crs)

(defface crs-list-done-face
  '((t (:foreground "#859900" :weight bold)))
  "Face for the DONE keyword in the PR list."
  :group 'crs)

(defface crs-list-waiting-face
  '((t (:foreground "#b58900" :weight bold)))
  "Face for the WAITING keyword in the PR list."
  :group 'crs)

(defface crs-list-progress-face
  '((t (:foreground "#6c71c4" :weight bold)))
  "Face for the PROGRESS keyword in the PR list."
  :group 'crs)

(defface crs-list-blocked-face
  '((t (:foreground "#d33682" :weight bold)))
  "Face for the BLOCKED keyword in the PR list."
  :group 'crs)

(defface crs-list-cancelled-face
  '((t (:foreground "#586e75" :weight bold :strike-through t)))
  "Face for the CANCELLED keyword in the PR list."
  :group 'crs)

(defface crs-list-tentative-face
  '((t (:foreground "#2aa198" :weight bold)))
  "Face for the TENTATIVE keyword in the PR list."
  :group 'crs)

(defface crs-list-delegated-face
  '((t (:foreground "#268bd2" :weight bold :slant italic)))
  "Face for the DELEGATED keyword in the PR list."
  :group 'crs)

(defface crs-list-ratio-face
  '((t (:foreground "#93a1a1")))
  "Face for the [n/m] progress ratio in the PR list."
  :group 'crs)

(defface crs-list-tag-face
  '((t (:foreground "#2aa198" :weight bold)))
  "Face for :tag: markers in the PR list."
  :group 'crs)

(defvar crs-list--heading-regexp "^\\(\\*+\\) "
  "Regexp matching org-style heading lines by leading stars.")

(defvar crs-list-mode-font-lock-keywords
  `(;; Heading levels — match the whole line, apply heading face
    ("^\\(\\*\\) .*$" 0 'crs-list-heading-1-face t)
    ("^\\(\\*\\*\\) .*$" 0 'crs-list-heading-2-face t)
    ("^\\(\\*\\*\\*\\) .*$" 0 'crs-list-heading-3-face t)
    ;; Status keywords — override heading face for just the keyword
    ("\\bTODO\\b" 0 'crs-list-todo-face t)
    ("\\bDONE\\b" 0 'crs-list-done-face t)
    ("\\bWAITING\\b" 0 'crs-list-waiting-face t)
    ("\\bPROGRESS\\b" 0 'crs-list-progress-face t)
    ("\\bBLOCKED\\b" 0 'crs-list-blocked-face t)
    ("\\bCANCELLED\\b" 0 'crs-list-cancelled-face t)
    ("\\bTENTATIVE\\b" 0 'crs-list-tentative-face t)
    ("\\bDELEGATED\\b" 0 'crs-list-delegated-face t)
    ;; [n/m] progress ratio
    ("\\[\\([0-9]+/[0-9]+\\)\\]" 0 'crs-list-ratio-face t)
    ;; :tag: markers
    ("\\(:[a-zA-Z0-9_@-]+:\\)+" 0 'crs-list-tag-face t))
  "Font-lock keywords for `crs-list-mode'.")

(defun crs-list--heading-level ()
  "Return the heading level (number of stars) of the current line, or nil."
  (save-excursion
    (beginning-of-line)
    (when (looking-at "^\\(\\*+\\) ")
      (length (match-string 1)))))

(defun crs-list--next-heading-pos (&optional same-or-higher)
  "Return position of next heading, or `point-max'.
If SAME-OR-HIGHER is a number, find next heading with level <= that number."
  (save-excursion
    (forward-line 1)
    (if same-or-higher
        (let ((re (format "^\\*\\{1,%d\\} " same-or-higher)))
          (if (re-search-forward re nil t)
              (line-beginning-position)
            (point-max)))
      (if (re-search-forward crs-list--heading-regexp nil t)
          (line-beginning-position)
        (point-max)))))

(defun crs-list-toggle-heading ()
  "Toggle visibility of the section under the current heading.
When expanding, sub-headings remain individually collapsed."
  (interactive)
  (let ((level (crs-list--heading-level)))
    (if (not level)
        (message "Not on a heading line")
      (let* ((beg (save-excursion (forward-line 1) (point)))
             (end (crs-list--next-heading-pos level))
             (found nil))
        ;; Look for the overlay owned by THIS heading specifically
        (dolist (ov (overlays-at beg))
          (when (and (overlay-get ov 'crs-list-fold)
                     (= (overlay-start ov) beg))
            (delete-overlay ov)
            (setq found t)))
        ;; If we didn't find our own overlay, collapse
        (unless found
          (when (< beg end)
            (let ((ov (make-overlay beg end)))
              (overlay-put ov 'crs-list-fold t)
              (overlay-put ov 'invisible 'crs-list-fold))))))))

(defun crs-list-collapse-all ()
  "Collapse every heading in the buffer individually.
Each heading gets its own overlay so expanding a parent reveals
its children still collapsed."
  (save-excursion
    ;; First remove all existing folds to start clean
    (dolist (ov (overlays-in (point-min) (point-max)))
      (when (overlay-get ov 'crs-list-fold)
        (delete-overlay ov)))
    ;; Collapse from deepest level first so parent overlays contain child overlays
    (goto-char (point-min))
    (let (headings)
      (while (re-search-forward crs-list--heading-regexp nil t)
        (push (cons (crs-list--heading-level) (line-beginning-position)) headings))
      ;; Sort deepest first so inner overlays are created before outer ones
      (setq headings (sort headings (lambda (a b) (> (car a) (car b)))))
      (dolist (h headings)
        (goto-char (cdr h))
        (let* ((level (car h))
               (beg (save-excursion (forward-line 1) (point)))
               (end (crs-list--next-heading-pos level)))
          (when (< beg end)
            (let ((ov (make-overlay beg end)))
              (overlay-put ov 'crs-list-fold t)
              (overlay-put ov 'invisible 'crs-list-fold))))))))

(defun crs-list-cycle-global ()
  "Cycle global visibility: if anything is folded, expand all; otherwise collapse all."
  (interactive)
  (let ((any-folded nil))
    (dolist (ov (overlays-in (point-min) (point-max)))
      (when (overlay-get ov 'crs-list-fold)
        (setq any-folded t)))
    (if any-folded
        (progn
          (dolist (ov (overlays-in (point-min) (point-max)))
            (when (overlay-get ov 'crs-list-fold)
              (delete-overlay ov)))
          (message "All expanded"))
      (crs-list-collapse-all)
      (message "All collapsed"))))

(defvar-keymap crs-list-mode-map
  "TAB" #'crs-list-toggle-heading
  "<tab>" #'crs-list-toggle-heading
  "<backtab>" #'crs-list-cycle-global
  "RET" #'crs-start-review-at-point
  "<return>" #'crs-start-review-at-point
  "g" #'crs-refresh-reviews
  "q" #'quit-window)

(define-derived-mode crs-list-mode fundamental-mode "CRS-List"
  "Major mode for viewing the code review PR list.
Provides org-like collapsible headings and fontified status keywords
without the overhead of full `org-mode'."
  (setq-local font-lock-defaults '(crs-list-mode-font-lock-keywords t))
  (add-to-invisibility-spec 'crs-list-fold)
  (setq buffer-read-only t)
  (goto-address-mode 1))

(defun crs-refresh-reviews ()
  "Refresh the PR list by re-fetching from the server."
  (interactive)
  (let ((buf (get-buffer crs-reviews-buffer-name)))
    (when buf
      (kill-buffer buf))
    (crs-get-reviews)))


;;;###autoload
(defun crs-get-reviews ()
  "Call the GetAllReviews RPC method and display the result in `crs-reviews-buffer-name'.
If the buffer already exists, switch to it instead of making a new RPC call."
  (interactive)
  (let ((existing-buffer (get-buffer crs-reviews-buffer-name)))
    (if existing-buffer
        (progn
          (display-buffer existing-buffer)
          (message "Switched to existing '%s' buffer" crs-reviews-buffer-name))
      (unless (and crs--process
                   (process-live-p crs--process))
        (crs-start-server)
        ;; Give the server a moment to start
        (sleep-for 0.5))

      (crs--send-request
       "RPCHandler.GetAllReviews"
       (vector)
       (lambda (result)
         (let* ((content (cdr (assq 'content result)))
                (rendered (if crs-include-comments-tree
                              content
                            (crs--strip-comments-tree content)))
                (buffer (get-buffer-create crs-reviews-buffer-name)))
           (with-current-buffer buffer
             (let ((inhibit-read-only t))
               (erase-buffer)
               (insert (or rendered ""))
               (crs--process-html-placeholders)
               (goto-char (point-min)))
             (crs-list-mode)
             (crs-list-collapse-all))
           (display-buffer buffer)
           (message "Reviews loaded into '%s' buffer" crs-reviews-buffer-name)))))))

(provide 'crs-list-mode)
;;; crs-list-mode.el ends here
