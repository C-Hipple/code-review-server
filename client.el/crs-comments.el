;;; crs-comments.el --- Comment and review functionality for crs-client -*- lexical-binding: t; -*-

;; SPDX-License-Identifier: GPL-3.0+

;;; Commentary:

;; Handles all comment and review interactions for the crs client:
;; adding, editing, deleting, and submitting comments and reviews,
;; the comment edit mode, outdated comments display, and comment
;; rendering helpers (indexing, tree rendering, compact indicators).

;;; Code:

(require 'crs-utils)
(require 'markdown-mode)
(require 'seq)

;; Forward declarations for functions defined in crs-client.el
(declare-function crs--send-request "crs-client")
(declare-function crs--render-and-update "crs-client")
(defvar crs--section-header-regexp)
(defvar crs--buffer-show-comments)
(defvar crs--buffer-diff)
(defvar crs--buffer-comments)
(defvar crs--buffer-review-feedback)

;;; Buffer-local variables for comment editing

(defvar-local crs--comment-owner nil)
(defvar-local crs--comment-repo nil)
(defvar-local crs--comment-number nil)
(defvar-local crs--comment-filename nil)
(defvar-local crs--comment-position nil)
(defvar-local crs--comment-reply-to-id nil)
(defvar-local crs--comment-editing-id nil
  "When non-nil, we're editing an existing local comment with this ID.")
(defvar-local crs--comment-original-line nil
  "The line number in the review buffer where the comment was started.")
(defvar-local crs--comment-original-context nil
  "Context for restoring position: (filename position file-line).")

;;; Comment rendering helpers

(defun crs--index-comments (comments)
  "Convert the list of comment objects into a hash table keyed by 'file:position'."
  (let ((map (make-hash-table :test 'equal)))
    (seq-do (lambda (comment)
              (let* ((file (cdr (assq 'path comment)))
                     (pos (cdr (assq 'position comment))) ;; String or nil
                     (key (if (and pos (not (string= pos "")))
                              (format "%s:%s" file pos)
                            (format "%s:" file))))
                (puthash key (cons comment (gethash key map)) map)))
            comments)
    ;; Reverse lists to keep order
    (maphash (lambda (k v) (puthash k (nreverse v) map)) map)
    map))

(defun crs--render-comment-tree (comments)
  "Render a list of comments (presumably a thread) into a string."
  (if (null comments)
      ""
    (let* ((root (car comments))
           (replies (cdr comments))
           (outdated (eq (cdr (assq 'outdated root)) t))
           (file (cdr (assq 'path root)))
           (id (cdr (assq 'id root)))
           (created (cdr (assq 'created_at root)))
           (author (cdr (assq 'author root)))
           (header (cond (outdated "    ┌─ REVIEW COMMENT [OUTDATED] ──────")
                         ((not (cdr (assq 'position root))) "    ┌─ FILE COMMENT ───────────────────")
                         (t "    ┌─ REVIEW COMMENT ─────────────────")))
           (lines (list "    │"
                        (format "    │ %s : %s" (or created "") (or id "")) ;; Simplification: not merging authors
                        (format "    │ File: %s" file)
                        header)))
      ;; Render root comment
      (push (format "    │ [%s]:" (or author "local")) lines)
      (push (crs--make-html-placeholder (cdr (assq 'body root)) "    │   ") lines)

      ;; Render replies
      (dolist (reply replies)
        (push "    │" lines)
        (let ((r-author (cdr (assq 'author reply)))
              (r-id (cdr (assq 'id reply))))
          (push (format "    │ Reply by [%s]:[%s]" (or r-author "local") (or r-id "")) lines)
          (push (crs--make-html-placeholder (cdr (assq 'body reply)) "    │   ") lines)))

      (push "    └──────────────────────────────────" lines)
      (push "" lines)
      (mapconcat #'identity (nreverse lines) "\n"))))

(defun crs--format-compact-comment-indicator (comments)
  "Format a compact comment indicator for COMMENTS.
Returns a string like <C: user1, user2>."
  (let ((authors (seq-uniq
                  (seq-map (lambda (c)
                             (or (cdr (assq 'author c)) "local"))
                           comments))))
    (format "<C: %s>" (string-join authors ", "))))

;;; Comment context extraction

(defun crs--find-first-hunk-line ()
  "Find the line number of the first hunk header after point, bounded by the next file header."
  (save-excursion
    (let* ((start-pos (point))
           (search-bound (save-excursion
                           (forward-line 1)
                           (if (re-search-forward "^\\(?:[^[:space:]].*?[[:space:]]\\)?\\(?:modified\\|deleted\\|new file\\|renamed\\)[[:space:]:]+" nil t)
                               (match-beginning 0)
                             (point-max)))))
      (message "Searching for hunk between line %d and %d" (line-number-at-pos start-pos) (line-number-at-pos search-bound))
      ;; Diagnostic: Log the start of each line
      (save-excursion
        (goto-char start-pos)
        (while (< (point) search-bound)
          (message "Line %d start: [%S]" (line-number-at-pos) (buffer-substring-no-properties (line-beginning-position) (min (line-end-position) (+ (line-beginning-position) 15))))
          (forward-line 1)))
      (goto-char start-pos)
      (if (search-forward "@@" search-bound t)
          (let ((hunk-line (line-number-at-pos)))
            (message "Found hunk header at line %d" hunk-line)
            hunk-line)
        (progn
          (message "Failed to find hunk header between line %d and %d. Bound pos: %d" (line-number-at-pos start-pos) (line-number-at-pos search-bound) search-bound)
          nil)))))

(defun crs--get-comment-context ()
  (interactive)
  "Extract owner, repo, number, filename, position, reply-to-id, and local comment edit info.
Returns a list: (owner repo number filename position reply-to-id local-comment-id local-comment-body).
If on a local comment, local-comment-id and local-comment-body will be set."
  (let ((owner nil)
        (repo nil)
        (number nil)
        (filename nil)
        (position nil)
        (reply-to-id nil)
        (local-comment-id nil)
        (local-comment-body nil)
        (target-file-line nil)
        (target-line (line-number-at-pos))
        (first-hunk-line-num nil))

    ;; 1. Check if inside a comment block and extract info
    (save-excursion
      (end-of-line)
      (let ((line-content (buffer-substring-no-properties (line-beginning-position) (line-end-position))))
        (when (string-match-p "^    [│┌└]" line-content)
          (save-excursion
            (if (re-search-backward "^    ┌─ REVIEW COMMENT" nil t)
                (let ((block-start (point)))
                  (forward-line 2) ;; ID is on the 3rd line of the block
                  (let ((id-line (buffer-substring-no-properties (point) (line-end-position))))
                    (when (string-match " : \\([0-9]+\\)$" id-line)
                      (setq reply-to-id (string-to-number (match-string 1 id-line)))
                      (message "Found Reply-To ID: %d" reply-to-id)))
                  ;; Check if this is a local comment by looking for [local]:
                  (goto-char block-start)
                  (let ((block-end (save-excursion
                                     (if (re-search-forward "^    └" nil t)
                                         (point)
                                       (point-max)))))
                    (when (re-search-forward "^    │ \\[local\\]:" block-end t)
                      ;; This is a local comment - extract its ID from line 3
                      (goto-char block-start)
                      (forward-line 2)
                      (let ((header-line (buffer-substring-no-properties (line-beginning-position) (line-end-position))))
                        (when (string-match " : \\([0-9]+\\)$" header-line)
                          (setq local-comment-id (string-to-number (match-string 1 header-line)))
                          (message "Found local comment ID: %d" local-comment-id)
                          ;; Clear reply-to-id since we're editing, not replying
                          (setq reply-to-id nil)))
                      ;; Extract the body - lines after [local]: until end of block or next reply
                      (goto-char block-start)
                      (when (re-search-forward "^    │ \\[local\\]:" block-end t)
                        (forward-line 1)
                        (let ((body-lines nil))
                          (while (and (< (point) block-end)
                                      (looking-at "^    │   \\(.*\\)$"))
                            (push (match-string 1) body-lines)
                            (forward-line 1))
                          (when body-lines
                            (setq local-comment-body
                                  (string-join (nreverse body-lines) "\n"))))))))
              (message "Could not find start of comment block"))))))

    ;; 2. Parse Owner, Repo, Number
    (condition-case nil
        (let ((info (crs--get-current-review-info)))
          (setq owner (nth 0 info)
                repo (nth 1 info)
                number (nth 2 info)))
      (error (message "Could not parse review context from buffer name: %S" (buffer-name))))

    ;; 3. Find Filename and Position
    (save-excursion
      (end-of-line)
      ;; Search backward for file header, requiring that it doesn't start with space (to skip comment blocks)
      (if (re-search-backward "^\\(?:[^[:space:]].*?[[:space:]]\\)?\\(modified\\|deleted\\|new file\\|renamed\\)[[:space:]:]+\\([^[:space:]\n].*?\\)[[:space:]]*$" nil t)
          (progn
            (setq filename (match-string 2))
            (setq first-hunk-line-num (crs--find-first-hunk-line))
            (unless first-hunk-line-num
              (message "No hunk header found for file %s" filename)))
        (message "No file header found"))

      (when (and filename first-hunk-line-num)
        (goto-char (point-min))
        (forward-line (1- first-hunk-line-num))
        (let ((count 0)
              (file-line nil))
          (while (<= (line-number-at-pos) target-line)
            (let ((line-content (buffer-substring-no-properties (line-beginning-position) (line-end-position))))
              (cond
               ((string-match "^@@ -[0-9]+,[0-9]+ \\+\\([0-9]+\\),[0-9]+ @@" line-content)
                (setq file-line (string-to-number (match-string 1 line-content)))
                (setq target-file-line file-line))
               ((string-match-p "^[[:cntrl:][:space:]]*[│┌└]" line-content)
                ;; Skip comment blocks
                nil)
               (t
                ;; Content line
                (unless (string-match-p "^@@" line-content)
                  (setq count (1+ count))
                  (setq target-file-line file-line)
                  (when (and file-line (not (string-prefix-p "-" line-content)))
                    (setq file-line (1+ file-line)))))))
            (forward-line 1))
          (setq position count))))

    (let ((ctx (list owner repo number filename position reply-to-id local-comment-id local-comment-body target-file-line)))
      (message "Context extracted: %S" ctx)
      (message (concat "Position:" (prin1-to-string position)))
      ctx)))

;;; Toggle and display helpers

(defun crs-toggle-comments ()
  "Toggle visibility of all comments in the current review buffer.
Re-renders the buffer with or without comments based on the toggle state."
  (interactive)
  (unless crs--buffer-diff
    (error "No stored PR data. Please reload the review first"))
  (setq crs--buffer-show-comments (not crs--buffer-show-comments))
  (let ((current-line (line-number-at-pos)))
    (crs--render-and-update (current-buffer) nil current-line))
  (message "Comments %s" (if crs--buffer-show-comments "shown" "hidden")))

(defun crs--maybe-show-collapsed-comments ()
  "Show collapsed comments in minibuffer if cursor is on a line with compact indicator."
  (when (and (eq major-mode 'my-code-review-mode)
             (not crs--buffer-show-comments)
             crs--buffer-comments)
    (let ((line (buffer-substring-no-properties (line-beginning-position) (line-end-position))))
      (when (string-match "<C: [^>]+>" line)
        ;; Extract file and position from context
        (let* ((ctx (crs--get-comment-context))
               (file (nth 3 ctx))
               (pos (nth 4 ctx)))
          (when (and file pos)
            (let* ((comment-map (crs--index-comments crs--buffer-comments))
                   (key (format "%s:%s" file pos))
                   (comments (gethash key comment-map)))
              (when comments
                (crs--display-comments-in-minibuffer comments)))))))))

(defun crs--display-comments-in-minibuffer (comments)
  "Display COMMENTS in the minibuffer as a one-line summary."
  (let* ((count (length comments))
         (summary
          (mapconcat
           (lambda (c)
             (let ((author (or (cdr (assq 'author c)) "local"))
                   (body (or (cdr (assq 'body c)) "")))
               ;; Truncate body to first line, max 60 chars
               (let ((first-line (car (split-string body "\n"))))
                 (if (> (length first-line) 60)
                     (format "[%s]: %s..." author (substring first-line 0 57))
                   (format "[%s]: %s" author first-line)))))
           comments
           " | ")))
    (message "%d comment%s: %s" count (if (= count 1) "" "s") summary)))

;;; Outdated comments

(defun crs-show-outdated-comments ()
  "Show outdated comments for the current PR in a separate buffer."
  (interactive)
  (unless (and (boundp 'crs--buffer-outdated-comments)
               crs--buffer-outdated-comments
               (not (seq-empty-p crs--buffer-outdated-comments)))
    (error "No outdated comments found for this PR"))

  (let* ((info (crs--get-current-review-info))
         (owner (nth 0 info))
         (repo (nth 1 info))
         (number (nth 2 info))
         (buffer-name (format "* Outdated Comments %s/%s #%d *" owner repo number))
         (buffer (get-buffer-create buffer-name))
         (comments crs--buffer-outdated-comments))
    (with-current-buffer buffer
      (let ((inhibit-read-only t))
        (erase-buffer)
        (insert (format "Outdated Comments for %s/%s #%d\n" owner repo number))
        (insert "──────────────────────────────────\n\n")
        (if (seq-empty-p comments)
            (insert "No outdated comments found.\n")
          (seq-do (lambda (c)
                    (let ((author (or (cdr (assq 'author c)) "unknown"))
                          (time (or (cdr (assq 'created_at c)) ""))
                          (path (or (cdr (assq 'path c)) ""))
                          (body (or (cdr (assq 'body c)) "(No body)")))
                      (insert (format "[%s] %s on %s:\n" author time path))
                      (crs--insert-html body "  ")
                      (insert "\n\n──────────────────────────────────\n\n")))
                  comments))
        (goto-char (point-min))
        (crs-outdated-comments-mode)))
    (display-buffer buffer)
    (message "Outdated comments displayed in %s" buffer-name)))

(defun crs-quit-outdated-comments ()
  "Quit the outdated comments window and kill the buffer."
  (interactive)
  (quit-window t))

(defvar-keymap crs-outdated-comments-mode-map
  "q" #'crs-quit-outdated-comments)

(define-derived-mode crs-outdated-comments-mode markdown-mode "Outdated Comments"
  "Major mode for viewing outdated comments.
  Inherits from markdown-mode and is read-only.
  \\{crs-outdated-comments-mode-map}"
  (setq buffer-read-only t))

(when (fboundp 'evil-define-key)
  (evil-define-key 'normal crs-outdated-comments-mode-map
    "q" #'crs-quit-outdated-comments))

;;; Comment edit mode

(define-derived-mode comment-edit-mode markdown-mode "Code Review Comment"
  "Major mode for editing code review comments."
  (local-set-key (kbd "C-c C-c") 'crs-submit-comment)
  (local-set-key (kbd "C-c C-k") 'crs-abort-comment))

(when (fboundp 'evil-define-key)
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
    "C-c C-k" #'crs-abort-comment))

;;; Adding and editing comments

(defun crs-add-or-edit-comment (owner repo number filename position &optional reply-to-id local-comment-id local-comment-body line)
  "Open a buffer to add or edit a comment on a review.
If on a local comment, opens it for editing with the existing body pre-filled.
If called interactively, attempts to guess parameters from context."
  (interactive
   (let ((ctx (crs--get-comment-context)))
     ;; For editing, we need local-comment-id; for adding, we need position or reply-to-id
     (unless (and (nth 0 ctx) (nth 3 ctx) (or (nth 4 ctx) (nth 5 ctx) (nth 6 ctx)))
       (error "Could not determine context (Owner: %S, Repo: %S, Num: %S, File: %S, Pos: %S, ReplyID: %S, LocalID: %S). Buffer: %S"
              (nth 0 ctx) (nth 1 ctx) (nth 2 ctx) (nth 3 ctx) (nth 4 ctx) (nth 5 ctx) (nth 6 ctx) (buffer-name)))
     ctx))
  (let ((buffer (get-buffer-create (format "*Comment Edit %s/%s #%d*" owner repo number)))
        (editing (not (null local-comment-id)))
        (original-line (line-number-at-pos))
        ;; Store context for position restoration: filename, position, and file-line
        (original-context (when filename
                            (list filename position line))))
    (with-current-buffer buffer
      (comment-edit-mode)
      (erase-buffer)
      (setq crs--comment-owner owner)
      (setq crs--comment-repo repo)
      (setq crs--comment-number number)
      (setq crs--comment-filename filename)
      (setq crs--comment-position position)
      (setq crs--comment-reply-to-id reply-to-id)
      (setq crs--comment-editing-id local-comment-id)
      (setq crs--comment-original-line original-line)
      (setq crs--comment-original-context original-context)
      ;; If editing, pre-populate with existing body
      (when (and editing local-comment-body)
        (insert local-comment-body)))
    (switch-to-buffer-other-window buffer)
    (when editing
      (message "Editing local comment %d" local-comment-id))
    (when (fboundp 'evil-insert-state)
      (evil-insert-state))))

;; Alias for backwards compatibility
(defalias 'crs-add-comment 'crs-add-or-edit-comment)

(defun crs-submit-comment ()
  "Submit the comment in the current buffer."
  (interactive)
  (let ((body (buffer-string))
        (owner crs--comment-owner)
        (repo crs--comment-repo)
        (number crs--comment-number)
        (filename crs--comment-filename)
        (reply-to-id crs--comment-reply-to-id)
        (editing-id crs--comment-editing-id)
        (original-line crs--comment-original-line)
        (original-context crs--comment-original-context)
        (position (if crs--comment-reply-to-id
                      nil
                    crs--comment-position)))
    (if (string-match-p "\\`[[:space:]\n]*\\'" body)
        (message "Comment is empty, not submitting.")
      (if editing-id
          ;; Editing an existing local comment
          (crs--send-request
           "RPCHandler.EditComment"
           (vector (list (cons 'Owner owner)
                         (cons 'Repo repo)
                         (cons 'Number number)
                         (cons 'ID editing-id)
                         (cons 'Body body)))
           (lambda (result)
             (let ((err (cdr (assq 'error result))))
               (if err
                   (message "Error updating comment: %s" (if (stringp err) err (cdr (assq 'message err))))
                 (let ((review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number))))
                   (when review-buffer
                     (crs--render-and-update review-buffer result original-line original-context))
                   (message "Comment updated successfully")
                   (kill-buffer-and-window))))))
        ;; Adding a new comment
        (crs--send-request
         "RPCHandler.AddComment"
         (vector (list (cons 'Owner owner)
                       (cons 'Repo repo)
                       (cons 'Number number)
                       (cons 'Filename filename)
                       (cons 'Position position)
                       (cons 'ReplyToID reply-to-id)
                       (cons 'Body body)))
         (lambda (result)
           (let ((err (cdr (assq 'error result))))
             (if err
                 (message "Error adding comment: %s" (if (stringp err) err (cdr (assq 'message err))))
               (let ((review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number))))
                 (when review-buffer
                   (crs--render-and-update review-buffer result original-line original-context))
                 (message "Comment added successfully")
                 (kill-buffer-and-window))))))))))

(defun crs-abort-comment ()
  "Abort the comment in the current buffer."
  (interactive)
  (kill-buffer-and-window)
  (message "Comment aborted."))

;;; Deleting local comments

(defun crs--get-local-comment-at-point ()
  "Get the local comment ID at point, or nil if not on a local comment.
Returns a plist with :id, :owner, :repo, :number if on a local comment."
  (let ((owner nil)
        (repo nil)
        (number nil)
        (local-comment-id nil))
    ;; Parse Owner, Repo, Number from buffer name
    (condition-case nil
        (let ((info (crs--get-current-review-info)))
          (setq owner (nth 0 info)
                repo (nth 1 info)
                number (nth 2 info)))
      (error nil))
    ;; Check if inside a local comment block
    (save-excursion
      (end-of-line)
      (let ((line-content (buffer-substring-no-properties (line-beginning-position) (line-end-position))))
        (when (string-match-p "^    [│┌└]" line-content)
          (when (re-search-backward "^    ┌─ REVIEW COMMENT" nil t)
            (let ((block-start (point))
                  (block-end (save-excursion
                               (if (re-search-forward "^    └" nil t)
                                   (point)
                                 (point-max)))))
              ;; Check if this block contains [local]:
              (when (save-excursion
                      (re-search-forward "^    │ \\[local\\]:" block-end t))
                ;; Extract the ID from line 3 of the block
                (goto-char block-start)
                (forward-line 2)
                (let ((header-line (buffer-substring-no-properties (line-beginning-position) (line-end-position))))
                  (when (string-match " : \\([0-9]+\\)$" header-line)
                    (setq local-comment-id (string-to-number (match-string 1 header-line)))))))))))
    (when (and owner repo number local-comment-id)
      (list :id local-comment-id :owner owner :repo repo :number number))))

(defun crs-delete-local-comment ()
  "Delete the local comment at point.
If not on a local comment, displays a warning message."
  (interactive)
  (let ((comment-info (crs--get-local-comment-at-point)))
    (if (not comment-info)
        (message "Not on a local comment")
      (let ((id (plist-get comment-info :id))
            (owner (plist-get comment-info :owner))
            (repo (plist-get comment-info :repo))
            (number (plist-get comment-info :number)))
        (when (yes-or-no-p (format "Delete local comment %d? " id))
          (crs--send-request
           "RPCHandler.DeleteComment"
           (vector (list (cons 'Owner owner)
                         (cons 'Repo repo)
                         (cons 'Number number)
                         (cons 'ID id)))
           (lambda (result)
             (let ((err (cdr (assq 'error result))))
               (if err
                   (message "Error deleting comment: %s" (if (stringp err) err (cdr (assq 'message err))))
                 (let ((review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number))))
                   (when review-buffer
                     (crs--render-and-update review-buffer result))
                   (message "Local comment deleted")))))))))))

;;; Review submission

(defun crs-submit-review (event)
  "Submit a review with EVENT.
The body is taken from `crs--buffer-review-feedback'.
If the body is empty, prompts the user."
  (interactive
   (list (completing-read "Event: " '("APPROVE" "REQUEST_CHANGES" "COMMENT") nil t)))
  (let ((body crs--buffer-review-feedback)
        (owner nil)
        (repo nil)
        (number nil))
    (when (or (null body) (string-match-p "\\`[[:space:]\n]*\\'" body))
      (unless (yes-or-no-p "Review feedback is empty. Continue anyway? ")
        (user-error "Aborted")))

    (let ((info (crs--get-current-review-info)))
      (setq owner (nth 0 info)
            repo (nth 1 info)
            number (nth 2 info)))

    (crs--send-request
     "RPCHandler.SubmitReview"
     (vector (list (cons 'Owner owner)
                   (cons 'Repo repo)
                   (cons 'Number number)
                   (cons 'Event event)
                   (cons 'Body (or body ""))))
     (lambda (result)
       (let ((err (cdr (assq 'error result))))
         (if err
             (message "Error submitting review: %s" (if (stringp err) err (cdr (assq 'message err))))
           (let ((review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number))))
             (when review-buffer
               (with-current-buffer review-buffer
                 (setq crs--buffer-review-feedback nil)
                 (crs--render-and-update review-buffer result)))
             (message "Review submitted successfully!"))))))))

(defun crs-approve-review ()
  "Approve the review."
  (interactive)
  (crs-submit-review "APPROVE"))

(defun crs-comment-review ()
  "Comment on the review."
  (interactive)
  (crs-submit-review "COMMENT"))

(defun crs-request-changes-review ()
  "Request changes on the review."
  (interactive)
  (crs-submit-review "REQUEST_CHANGES"))

(defun crs-set-review-feedback ()
  "Set the review feedback for the current PR."
  (interactive)
  (let* ((info (crs--get-current-review-info))
         (owner (nth 0 info))
         (repo (nth 1 info))
         (number (nth 2 info))
         (buffer (get-buffer-create (format "*Review Feedback %s/%s #%d*" owner repo number)))
         (current-feedback crs--buffer-review-feedback)
         (original-review-buffer (current-buffer)))
    (with-current-buffer buffer
      (markdown-mode)
      (erase-buffer)
      (when current-feedback
        (insert current-feedback))
      (setq-local crs--comment-owner owner)
      (setq-local crs--comment-repo repo)
      (setq-local crs--comment-number number)
      (local-set-key (kbd "C-c C-c")
                     (lambda ()
                       (interactive)
                       (let ((feedback (buffer-string)))
                         (with-current-buffer original-review-buffer
                           (setq crs--buffer-review-feedback feedback)
                           (crs--render-and-update (current-buffer) nil))
                         (kill-buffer-and-window)
                         (message "Review feedback set."))))
      (local-set-key (kbd "C-c C-k") (lambda () (interactive) (kill-buffer-and-window) (message "Review feedback aborted."))))
    (switch-to-buffer-other-window buffer)
    (when (fboundp 'evil-insert-state)
      (evil-insert-state))))

(provide 'crs-comments)

;;; crs-comments.el ends here
