;;; crs-render.el --- Diff, comment and conversation rendering for review buffers. -*- lexical-binding: t; -*-

;;; Commentary:

;; Diff, comment and conversation rendering for review buffers.

;;; Code:

(require 'crs-vars)
(require 'crs-html)
(require 'washer)

(declare-function crs--get-comment-context "crs-comments")
(declare-function crs--get-current-review-info "crs-review")
(declare-function delta-wash "washer")
(declare-function my-code-review-mode "crs-review")

(defun crs-toggle-section ()
  "Toggle visibility of the section under the current header."
  (interactive)
  (let ((line-content (buffer-substring-no-properties (line-beginning-position) (line-end-position))))
    (if (string-match crs--section-header-regexp line-content)
        (let* ((header-text-end (line-end-position))
               (next-line-start (save-excursion (forward-line 1) (point)))
               (content-end (save-excursion
                              (forward-line 1)
                              (if (re-search-forward crs--section-header-regexp nil t)
                                  (1- (match-beginning 0))
                                (point-max))))
               (overlays (overlays-in header-text-end content-end))
               (found-collapsed nil))
          (dolist (ov overlays)
            (when (overlay-get ov 'codereview-hide)
              (delete-overlay ov)
              (setq found-collapsed t)))
          (unless found-collapsed
            ;; 1. Ellipsis on the newline
            (when (< header-text-end next-line-start)
              (let ((ov (make-overlay header-text-end next-line-start)))
                (overlay-put ov 'codereview-hide t)
                ;; (overlay-put ov 'display "...")
                ))
            ;; 2. Hide content
            (when (< next-line-start content-end)
              (let ((ov (make-overlay next-line-start content-end)))
                (overlay-put ov 'codereview-hide t)
                (overlay-put ov 'invisible 'codereview-hide)))))
      (message "Not on a section header"))))

(defun crs-collapse-all-sections ()
  "Collapse all sections in the current buffer."
  (interactive)
  (save-excursion
    (goto-char (point-min))
    (while (re-search-forward crs--section-header-regexp nil t)
      (let* ((header-text-end (line-end-position))
             (next-line-start (save-excursion (forward-line 1) (point)))
             (content-end (save-excursion
                            (forward-line 1)
                            (if (re-search-forward crs--section-header-regexp nil t)
                                (1- (match-beginning 0))
                              (point-max)))))
        ;; Check if already collapsed
        (let ((overlays (overlays-in header-text-end content-end))
              (already-collapsed nil))
          (dolist (ov overlays)
            (when (overlay-get ov 'codereview-hide)
              (setq already-collapsed t)))

          (unless already-collapsed
            (when (< header-text-end next-line-start)
              (let ((ov (make-overlay header-text-end next-line-start)))
                (overlay-put ov 'codereview-hide t)
                ;; (overlay-put ov 'display "...\n")
                ))
            (when (< next-line-start content-end)
              (let ((ov (make-overlay next-line-start content-end)))
                (overlay-put ov 'codereview-hide t)
                (overlay-put ov 'invisible 'codereview-hide)))))))))

(defun crs-expand-all-sections ()
  "Expand all sections in the current buffer."
  (interactive)
  (save-excursion
    (dolist (ov (overlays-in (point-min) (point-max)))
      (when (overlay-get ov 'codereview-hide)
        (delete-overlay ov)))))

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

;; Override evil-mode keybindings - define keys for normal and visual states

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

(defun crs--append-right-aligned (line indicator min-column)
  "Append INDICATOR to LINE, right-aligned at MIN-COLUMN or further right.
If LINE extends past MIN-COLUMN, place indicator one space after LINE ends."
  (let* ((line-length (length line))
         (indicator-length (length indicator))
         (target-column (max min-column (1+ line-length)))
         (padding (- target-column line-length)))
    (concat line (make-string padding ?\s) indicator)))

(defun crs--render-diff (diff-content comment-map &optional show-full-comments)
  "Render the diff string with interleaved comments.
DIFF-CONTENT is the raw diff string.
COMMENT-MAP is a hash table of comments.
SHOW-FULL-COMMENTS if non-nil shows full comment blocks, otherwise shows compact indicators."
  (with-temp-buffer
    (insert (or diff-content ""))
    (goto-char (point-min))
    (let ((current-file nil)
          (position 0)
          (first-hunk-seen nil)
          (result-buffer (generate-new-buffer " *crs-render-temp*")))
      (save-current-buffer
        (dolist (line (split-string (buffer-string) "\n"))
          (cond
           ;; File Header Start
           ((string-prefix-p "diff " line)
            (setq current-file nil)
            (setq first-hunk-seen nil)
            (with-current-buffer result-buffer (insert line "\n")))

           ;; New Format File Header
           ((string-match "^\\(modified\\|deleted\\|new file\\|renamed\\)[[:space:]]+\\(.*\\)$" line)
            (setq current-file (match-string 2 line))
            (setq first-hunk-seen nil)
            (with-current-buffer result-buffer (insert line "\n")))

           ;; Extract Filename
           ((string-match "^\\+\\+\\+ b/\\(.*\\)" line)
            (setq current-file (match-string 1 line))
            (with-current-buffer result-buffer (insert line "\n")))

           ;; Hunk Header
           ((string-prefix-p "@@ " line)
            (if (not first-hunk-seen)
                (progn
                  (setq position 0)
                  (setq first-hunk-seen t)
                  ;; Check for file comments (position nil/empty)
                  (when current-file
                    (let ((comments (gethash (format "%s:" current-file) comment-map)))
                      (when comments
                        (with-current-buffer result-buffer
                          (if show-full-comments
                              (insert (crs--render-comment-tree comments))
                            ;; For file-level comments, show indicator on hunk header
                            (setq line (crs--append-right-aligned
                                        line
                                        (crs--format-compact-comment-indicator comments)
                                        120))))))))
              (setq position (1+ position)))
            (with-current-buffer result-buffer (insert line "\n")))

           ;; Content Line
           ((and first-hunk-seen
                 (or (string-prefix-p "+" line)
                     (string-prefix-p "-" line)
                     (string-prefix-p " " line)))
            (setq position (1+ position))
            ;; Check for comments at this position
            (let* ((key (when current-file (format "%s:%d" current-file position)))
                   (comments (when key (gethash key comment-map))))
              (if (and comments show-full-comments)
                  ;; Insert line first, then full comments
                  (with-current-buffer result-buffer
                    (insert line "\n")
                    (dolist (c comments)
                      (insert (crs--render-comment-tree (list c)))))
                ;; No full comments - maybe append compact indicator
                (when comments
                  (setq line (crs--append-right-aligned
                              line
                              (crs--format-compact-comment-indicator comments)
                              120)))
                (with-current-buffer result-buffer (insert line "\n")))))

           ;; Other header lines (index, ---, etc)
           (t
            (with-current-buffer result-buffer (insert line "\n"))))))

      (with-current-buffer result-buffer
        (let ((str (buffer-string)))
          (kill-buffer result-buffer)
          str)))))

(defun crs--render-pr-from-json (result)
  "Render the PR content from the JSON result."
  (let* ((metadata (cdr (assq 'metadata result)))
         (diff (cdr (assq 'diff result)))
         (comments (cdr (assq 'comments result)))
         (comment-map (crs--index-comments comments))
         ;; Use the 'content' field for the preamble (header + conversation)
         ;; BUT strip any existing diff part from it
         (raw-content (cdr (assq 'content result)))
         (preamble (if raw-content
                       (if (string-match "Files changed (.*)\n\n" raw-content)
                           (substring raw-content 0 (match-end 0))
                         raw-content)
                     "")))
    (concat
     preamble
     (crs--render-diff diff comment-map))))

(defun crs--simplify-diff-headers ()
  "Replace standard git diff headers with a simplified 'modified filename' format.
This must be called after delta-wash."
  (save-excursion
    (goto-char (point-min))
    (while (re-search-forward "^diff --git a/\\(.*?\\) b/\\(.*?\\)$" nil t)
      (let* ((file-a (match-string 1))
             (file-b (match-string 2))
             ;; Prefer the b/ side unless it's /dev/null (deleted file)
             (filename (if (string= file-b "dev/null") file-a file-b))
             (start (match-beginning 0))
             (type "modified")
             (limit (save-excursion
                      (if (re-search-forward "^diff --git" nil t)
                          (match-beginning 0)
                        (point-max)))))

        (save-excursion
          (cond
           ((string= file-a "dev/null") (setq type "new file"))
           ((string= file-b "dev/null") (setq type "deleted"))
           (t
            (goto-char start)
            (if (re-search-forward "^new file mode" limit t)
                (setq type "new file")
              (goto-char start)
              (if (re-search-forward "^deleted file mode" limit t)
                  (setq type "deleted")
                (goto-char start)
                (if (re-search-forward "^@@ -0,0" limit t)
                    (setq type "new file")
                  (when (not (string= file-a file-b))
                    (setq type "renamed"))))))))

        (let ((end-marker-pos
               (save-excursion
                 (goto-char start)
                 (cond
                  ((re-search-forward "^\\+\\+\\+ .*\n" limit t) (point))
                  ((re-search-forward "^Binary files .*\n" limit t) (point))
                  ((re-search-forward "^@@ .*\n" limit t) (match-beginning 0))
                  (t nil)))))

          (when (and (not end-marker-pos)
                     (or (string= type "new file") (string= type "deleted")
                         (string= type "renamed")))
            (setq end-marker-pos limit))

          (if end-marker-pos
              (progn
                (delete-region start end-marker-pos)
                (let ((pad (cond ((string= type "modified") "     ")
                                 ((string= type "new file") "     ")
                                 ((string= type "deleted")  "      ")
                                 ((string= type "renamed")  "      ")
                                 (t "     "))))
                  (insert (format "%s%s%s\n" type pad filename))))
            (message "Could not find end of header for %s" filename)))))))

(defun crs--render-from-stored-data ()
  "Render the buffer content from stored diff, comments, and preamble.
Uses `crs--buffer-show-comments' to determine whether to show full comments or compact indicators."
  (let* ((comment-map (crs--index-comments crs--buffer-comments))
         (preamble (or crs--buffer-preamble ""))
         (feedback crs--buffer-review-feedback))
    ;; Inject feedback into existing section if it exists
    (if (and feedback (not (string-empty-p feedback)))
        (if (string-match "^Your Review Feedback\n" preamble)
            (let* ((start (match-end 0))
                   (rest (substring preamble start))
                   (next-section (string-match crs--section-header-regexp rest))
                   (content (concat "──────────────────────────────────\n" feedback "\n\n")))
              (if next-section
                  (setq preamble (concat (substring preamble 0 start) content (substring rest next-section)))
                (setq preamble (concat (substring preamble 0 start) content))))
          ;; Fallback: if section not found (shouldn't happen with this server), append it
          (setq preamble (concat preamble "\nYour Review Feedback\n──────────────────────────────────\n" feedback "\n\n"))))
    (concat
     preamble
     (crs--render-diff crs--buffer-diff comment-map crs--buffer-show-comments))))

(defun crs--insert-comments-into-buffer (comments show-full-comments)
  "Insert COMMENTS into the current buffer which contains the diff.
SHOW-FULL-COMMENTS determines whether to show full content or indicators."
  (let ((comment-map (crs--index-comments comments))
        (insertions nil)
        (current-file nil)
        (position 0)
        (first-hunk-seen nil))
    (save-excursion
      (goto-char (point-min))
      (while (not (eobp))
        (let* ((line-start (point))
               (line-end (line-end-position))
               (line (buffer-substring-no-properties line-start line-end)))
          (cond
           ;; File Header - match simplified first as it's more specific if simplification happened
           ((string-match "^\\(modified\\|deleted\\|new file\\|renamed\\)[[:space:]]+\\(.*\\)$" line)
            (setq current-file (match-string 2 line))
            (setq first-hunk-seen nil))

           ;; Fallback to standard diff header
           ((string-prefix-p "diff " line)
            (setq current-file nil)
            (setq first-hunk-seen nil))

           ((string-match "^\\+\\+\\+ b/\\(.*\\)" line)
            (setq current-file (match-string 1 line)))

           ;; Hunk Header
           ((string-prefix-p "@@ " line)
            (if (not first-hunk-seen)
                (progn
                  (setq position 0)
                  (setq first-hunk-seen t)
                  ;; File comments
                  (when current-file
                    (let ((file-comments (gethash (format "%s:" current-file) comment-map)))
                      (when file-comments
                        (if show-full-comments
                            ;; Insert before line
                            (push (cons line-start (crs--render-comment-tree file-comments)) insertions)
                          ;; Compact: append to line
                          (push (cons line-end (list 'append (crs--format-compact-comment-indicator file-comments))) insertions))))))
              (setq position (1+ position))))

           ;; Content Line
           ((and first-hunk-seen
                 (or (string-prefix-p "+" line)
                     (string-prefix-p "-" line)
                     (string-prefix-p " " line)))
            (setq position (1+ position))
            (let* ((key (when current-file (format "%s:%d" current-file position)))
                   (line-comments (when key (gethash key comment-map))))
              (when line-comments
                (if show-full-comments
                    ;; Insert after line
                    (push (cons line-end (concat "\n" (string-trim-right (crs--render-comment-tree line-comments)))) insertions)
                  ;; Compact: append
                  (push (cons line-end (list 'append (crs--format-compact-comment-indicator line-comments))) insertions))))))
          )
        (forward-line 1)))

    ;; Execute insertions (sorted by point descending)
    (setq insertions (sort insertions (lambda (a b) (> (car a) (car b)))))

    (dolist (ins insertions)
      (goto-char (car ins))
      (let ((content (cdr ins)))
        (if (and (listp content) (eq (car content) 'append))
            ;; Handle append with alignment
            (let* ((indicator (cadr content))
                   (current-line (buffer-substring-no-properties (line-beginning-position) (line-end-position)))
                   (new-line (crs--append-right-aligned current-line indicator 120)))
              (delete-region (line-beginning-position) (line-end-position))
              (insert new-line))
          ;; Normal insert
          (insert content))))))

(defun crs--render-header-from-metadata (metadata)
  "Render the PR header from METADATA alist."
  (if (null metadata)
      ""
    (let ((number (cdr (assq 'number metadata)))
          (title (cdr (assq 'title metadata)))
          (author (cdr (assq 'author metadata)))
          (state (cdr (assq 'state metadata)))
          (url (cdr (assq 'url metadata)))
          (base (cdr (assq 'base_ref metadata)))
          (head (cdr (assq 'head_ref metadata)))
          (milestone (cdr (assq 'milestone metadata)))
          (labels (cdr (assq 'labels metadata))) ;; array
          (draft (cdr (assq 'draft metadata)))
          (assignees (cdr (assq 'assignees metadata))) ;; array
          (reviewers (cdr (assq 'reviewers metadata))) ;; array
          (teams (cdr (assq 'requested_teams metadata))) ;; array
          (approved (cdr (assq 'approved_by metadata))) ;; array
          (changes (cdr (assq 'changes_requested_by metadata))) ;; array
          (commented (cdr (assq 'commented_by metadata))) ;; array
          (ci-status (cdr (assq 'ci_status metadata)))
          (ci-failures (cdr (assq 'ci_failures metadata))) ;; array
          (worktree (cdr (assq 'worktree_path metadata)))
          (body (cdr (assq 'body metadata)))
          (sb ""))

      (setq sb (concat sb (format "Title: #%s: %s\n" number title)))
      (setq sb (concat sb (format "Author: \t@%s\n" author)))
      (setq sb (concat sb (format "Title: \t%s\n" title)))
      (setq sb (concat sb (format "Refs:  %s ... %s\n" base head)))
      (when (and worktree (not (string-empty-p worktree)))
        (setq sb (concat sb (format "Worktree: \t%s\n" worktree))))
      (setq sb (concat sb (format "URL:   %s\n" url)))
      (setq sb (concat sb (format "State: \t%s\n" state)))
      (setq sb (concat sb (format "Draft: \t%s\n" (if (eq draft t) "True" "False"))))
      ;; (setq sb (concat sb (format "Milestone: \t%s\n" (or milestone "No milestone"))))

      (let ((labels-str (if (> (length labels) 0) (string-join (append labels nil) ", ") "None yet")))
        (setq sb (concat sb (format "Labels: \t%s\n" labels-str))))

      ;; (setq sb (concat sb "Projects: \tNone yet\n"))

      ;; (let ((assignees-str (if (> (length assignees) 0) (string-join (append assignees nil) ", ") "No one -- Assign yourself")))
      ;; (setq sb (concat sb (format "Assignees: \t%s\n" assignees-str))))

      ;; (setq sb (concat sb "Suggested-Reviewers: No suggestions\n"))

      (let* ((reviewers-list (append reviewers nil))
             (teams-list (mapcar (lambda (team) (concat "team:" team)) (append teams nil)))
             (all-reviewers (append reviewers-list teams-list))
             (rev-str (string-join all-reviewers ", ")))
        (setq sb (concat sb (format "Reviewers: \t%s\n" rev-str))))

      (when (> (length approved) 0)
        (setq sb (concat sb (format "Approved-By: \t%s\n" (string-join (append approved nil) ", ")))))
      (when (> (length changes) 0)
        (setq sb (concat sb (format "Changes-Requested-By: \t%s\n" (string-join (append changes nil) ", ")))))
      (when (> (length commented) 0)
        (setq sb (concat sb (format "Commented-By: \t%s\n" (string-join (append commented nil) ", ")))))

      (if (and ci-status (not (string-empty-p ci-status)))
          (progn
            (setq sb (concat sb (format "CI Status: \t%s\n" ci-status)))
            (when (> (length ci-failures) 0)
              (dolist (fail (append ci-failures nil))
                (setq sb (concat sb (format "  - %s\n" fail))))))
        (setq sb (concat sb "CI Status: \tUnknown\n")))

      ;; Description/Body (rendered as HTML)
      (setq sb (concat sb "\nDescription\n"))
      (setq sb (concat sb (crs--make-html-placeholder body) "\n"))

      sb)))

(defun crs--render-conversation-from-data (comments reviews &optional outdated-comments commits)
  "Render the conversation section from COMMENTS, REVIEWS, OUTDATED-COMMENTS and COMMITS."
  (let ((items nil)
        (sb "\nConversation\n"))
    (when (and outdated-comments (> (length outdated-comments) 0))
      (let ((count (length outdated-comments)))
        (setq sb (concat sb (format "PR Contains %d outdated comment%s\n---------------------\n"
                                    count (if (= count 1) "" "s"))))))
    ;; 1. Collect Issue Comments (where path is empty)
    (seq-do (lambda (c)
              (let ((path (cdr (assq 'path c))))
                (when (or (null path) (string-empty-p path))
                  (push (list :type 'comment
                              :time (cdr (assq 'created_at c))
                              :author (cdr (assq 'author c))
                              :body (cdr (assq 'body c)))
                        items))))
            comments)

    ;; 2. Collect Reviews
    (seq-do (lambda (r)
              (let ((state (cdr (assq 'state r)))
                    (body (cdr (assq 'body r))))
                ;; Skip empty COMMENTED reviews
                (unless (and (string= state "COMMENTED") (string-empty-p (or body "")))
                  (push (list :type 'review
                              :time (cdr (assq 'submitted_at r))
                              :author (cdr (assq 'user r))
                              :state state
                              :body body)
                        items))))
            reviews)

    ;; 3. Collect Commits
    (seq-do (lambda (c)
              (push (list :type 'commit
                          :time (cdr (assq 'date c))
                          :author (cdr (assq 'author c))
                          :sha (cdr (assq 'sha c))
                          :message (cdr (assq 'message c))
                          :url (cdr (assq 'url c)))
                    items))
            (or commits []))

    ;; 4. Sort by Time
    (setq items (sort items (lambda (a b)
                              (string< (or (plist-get a :time) "")
                                       (or (plist-get b :time) "")))))

    ;; 5. Render
    (if (null items)
        (setq sb (concat sb "No conversation found.\n"))
      (let ((first t))
        (dolist (item items)
          (unless first
            (setq sb (concat sb "--------------------------------------------------------------------------------\n")))
          (setq first nil)

          (let ((author (plist-get item :author))
                (time (plist-get item :time))
                (type (plist-get item :type)))
            (cond
             ((eq type 'commit)
              (let* ((sha (plist-get item :sha))
                     (short-sha (if (and sha (> (length sha) 7)) (substring sha 0 7) (or sha "")))
                     (msg (or (plist-get item :message) "(No message)"))
                     (first-line (car (split-string msg "\n"))))
                (setq sb (concat sb (format "Commit by %s at %s\n" (or author "") (or time ""))
                                 (format "  %s  %s\n\n" short-sha first-line)))))
             ((eq type 'review)
              (setq sb (concat sb (format "From: %s at %s [%s]\n" author time (plist-get item :state))))
              (setq sb (concat sb (crs--make-html-placeholder (or (plist-get item :body) "(No body)")) "\n\n")))
             (t
              (setq sb (concat sb (format "From: %s at %s\n" author time)))
              (setq sb (concat sb (crs--make-html-placeholder (or (plist-get item :body) "(No body)")) "\n\n"))))))))

    ;; Add Files Changed header (placeholder or parsed?)
    ;; For now just a blank line, maybe we can add a separator
    (setq sb (concat sb "\n"))
    sb))

(defun crs--find-position-in-diff (filename position)
  "Find the line number in the current buffer for FILENAME at POSITION.
POSITION is the diff position (count of content lines from first hunk).
Returns the line number, or nil if not found."
  (when (and filename position)
    (save-excursion
      (goto-char (point-min))
      (let ((target-file nil)
            (current-position 0)
            (first-hunk-seen nil)
            (found-line nil))
        ;; Find the file header
        (while (and (not found-line) (not (eobp)))
          (let* ((line-start (line-beginning-position))
                 (line-end (line-end-position))
                 (line (buffer-substring-no-properties line-start line-end)))
            (cond
             ;; File header - simplified format
             ((string-match "^\\(modified\\|deleted\\|new file\\|renamed\\)[[:space:]]+\\(.*\\)$" line)
              (setq target-file (match-string 2 line))
              (setq first-hunk-seen nil)
              (setq current-position 0))

             ;; Standard diff header
             ((string-match "^\\+\\+\\+ b/\\(.*\\)" line)
              (setq target-file (match-string 1 line))
              (setq first-hunk-seen nil)
              (setq current-position 0))

             ;; Hunk header
             ((string-prefix-p "@@ " line)
              (if (not first-hunk-seen)
                  (progn
                    (setq first-hunk-seen t)
                    (setq current-position 0))
                ;; Count subsequent hunk headers to match GitHub's position convention
                (setq current-position (1+ current-position))))

             ;; Skip comment blocks
             ((string-match-p "^[[:space:]]*[│┌└]" line)
              nil)

             ;; Content line
             ((and first-hunk-seen
                   (string= target-file filename)
                   (or (string-prefix-p "+" line)
                       (string-prefix-p "-" line)
                       (string-prefix-p " " line)))
              (setq current-position (1+ current-position))
              (when (= current-position position)
                (setq found-line (line-number-at-pos))))))
          (forward-line 1))
        found-line))))

(defun crs--render-and-update (buffer content &optional target-line target-context)
  "Render CONTENT (which can be a string, JSON-RPC result alist, or nil) into BUFFER.
If CONTENT is nil, re-renders from stored data (useful for toggle operations).
TARGET-LINE is a fallback line number. TARGET-CONTEXT is (filename position file-line)
for more robust position restoration."
  (with-current-buffer buffer
    (let ((inhibit-read-only t)
          (old-pos (point))
          ;; Extract data before mode change (which kills local vars)
          (new-diff nil)
          (new-comments nil)
          (new-outdated-comments nil)
          (new-metadata nil)
          (new-reviews nil)
          (new-commits nil)
          (new-preamble nil)
          (new-show-comments (if (local-variable-p 'crs--buffer-show-comments)
                                 crs--buffer-show-comments
                               t))
          ;; Preserve existing data for re-render case
          (existing-diff crs--buffer-diff)
          (existing-comments crs--buffer-comments)
          (existing-outdated-comments crs--buffer-outdated-comments)
          (existing-metadata crs--buffer-metadata)
          (existing-reviews crs--buffer-reviews)
          (existing-preamble crs--buffer-preamble)
          (existing-review-feedback crs--buffer-review-feedback)
          (existing-commits crs--buffer-commits))

      ;; If content is a JSON result (alist), extract the components
      (when (and content (listp content) (not (stringp content)))
        (let* ((diff (cdr (assq 'diff content)))
               (comments (cdr (assq 'comments content)))
               (outdated-comments (or (cdr (assq 'outdated_comments content))
                                      (cdr (assq 'outdated-comments content))
                                      (cdr (assoc "outdated_comments" content))
                                      (cdr (assoc "outdated-comments" content))))
               (metadata (cdr (assq 'metadata content)))
               (reviews (cdr (assq 'reviews content)))
               (commits (cdr (assq 'commits content)))
               (raw-content (cdr (assq 'content content)))
               (preamble (if raw-content
                             (if (string-match "Files changed (.*)\n\n" raw-content)
                                 (substring raw-content 0 (match-end 0))
                               raw-content)
                           "")))
          (setq new-diff diff)
          ;; Explicitly filter out outdated comments from the main comments list
          ;; just in case the server still includes them there.
          (setq new-comments (seq-filter (lambda (c)
                                           (not (eq (cdr (assq 'outdated c)) t)))
                                         comments))
          ;; seq-filter returns nil for empty results, convert to empty vector
          (when (null new-comments)
            (setq new-comments []))
          (setq new-outdated-comments outdated-comments)
          (setq new-metadata metadata)
          (setq new-reviews reviews)
          (setq new-commits commits)
          (setq new-preamble preamble)))

      ;; Temporarily set for rendering (before mode change wipes them)
      (setq crs--buffer-diff (or new-diff existing-diff))
      (setq crs--buffer-comments (or new-comments existing-comments))
      (setq crs--buffer-outdated-comments (or new-outdated-comments existing-outdated-comments))
      (setq crs--buffer-metadata (or new-metadata existing-metadata))
      (setq crs--buffer-reviews (or new-reviews existing-reviews))
      (setq crs--buffer-commits (or new-commits existing-commits))
      (setq crs--buffer-preamble (or new-preamble existing-preamble))
      (setq crs--buffer-show-comments new-show-comments)
      (setq crs--buffer-review-feedback existing-review-feedback)

      (erase-buffer)

      (cond
       ;; String content: insert directly
       ((stringp content)
        (insert content)
        (delta-wash)
        (crs--simplify-diff-headers)
        (my-code-review-mode))

       ;; nil or alist: render from stored data
       (t
        ;; 1. Insert Diff
        (insert (or crs--buffer-diff ""))

        ;; 2. Delta Wash
        (delta-wash)

        ;; 3. Simplify Headers
        (crs--simplify-diff-headers)

        ;; 4. Insert Comments (only regular comments in-line with diff)
        (crs--insert-comments-into-buffer crs--buffer-comments crs--buffer-show-comments)

        ;; 5. Insert Preamble & Feedback at TOP
        (let* ((header (crs--render-header-from-metadata crs--buffer-metadata))
               (conversation (crs--render-conversation-from-data
                              crs--buffer-comments
                              crs--buffer-reviews
                              crs--buffer-outdated-comments
                              crs--buffer-commits))
               (changes-header
                (let* ((meta crs--buffer-metadata)
                       (nfiles (or (cdr (assq 'changed_files meta)) 0))
                       (adds (or (cdr (assq 'additions meta)) 0))
                       (dels (or (cdr (assq 'deletions meta)) 0)))
                  (concat "\nChanges\n"
                          (format "%d file%s changed, +%d addition%s, -%d deletion%s\n"
                                  nfiles (if (= nfiles 1) "" "s")
                                  adds (if (= adds 1) "" "s")
                                  dels (if (= dels 1) "" "s")))))
               (preamble (concat header "\n" conversation changes-header "\n"))
               (feedback existing-review-feedback))

          ;; Inject feedback into preamble (logic from crs--render-from-stored-data)
          (if (and feedback (not (string-empty-p feedback)))
              (if (string-match "^Your Review Feedback\n" preamble)
                  (let* ((start (match-end 0))
                         (rest (substring preamble start))
                         (next-section (string-match crs--section-header-regexp rest))
                         (content-str (concat "──────────────────────────────────\n" feedback "\n\n")))
                    (if next-section
                        (setq preamble (concat (substring preamble 0 start) content-str (substring rest next-section)))
                      (setq preamble (concat (substring preamble 0 start) content-str))))
                (setq preamble (concat preamble "\nYour Review Feedback\n──────────────────────────────────\n" feedback "\n\n"))))

          (goto-char (point-min))
          (insert preamble))

        (my-code-review-mode)))

      (crs--process-html-placeholders)

      ;; Re-set the buffer-local variables AFTER mode change
      (setq crs--buffer-diff (or new-diff existing-diff))
      (setq crs--buffer-comments (or new-comments existing-comments))
      (setq crs--buffer-outdated-comments (or new-outdated-comments existing-outdated-comments))
      (setq crs--buffer-metadata (or new-metadata existing-metadata))
      (setq crs--buffer-reviews (or new-reviews existing-reviews))
      (setq crs--buffer-commits (or new-commits existing-commits))
      (setq crs--buffer-preamble (or new-preamble existing-preamble))
      (setq crs--buffer-show-comments new-show-comments)
      (setq crs--buffer-review-feedback existing-review-feedback)

      (let ((final-pos
             (cond
              ;; First try using context (filename + position) for accurate restoration
              ((and target-context (nth 0 target-context) (nth 1 target-context))
               (let* ((ctx-filename (nth 0 target-context))
                      (ctx-position (nth 1 target-context))
                      (found-line (crs--find-position-in-diff ctx-filename ctx-position)))
                 (if found-line
                     (progn
                       (goto-char (point-min))
                       (forward-line (1- found-line))
                       (point))
                   ;; Fallback to target-line if context-based search fails
                   (when target-line
                     (goto-char (point-min))
                     (forward-line (1- target-line))
                     (if (> (point) (point-max))
                         (point-max)
                       (point))))))
              ;; Fallback to absolute line number
              (target-line
               (progn
                 (goto-char (point-min))
                 (forward-line (1- target-line))
                 (if (> (point) (point-max))
                     (point-max)
                   (point))))
              ;; Default: restore old position
              (t (min old-pos (point-max))))))
        (goto-char final-pos)
        ;; Also update point in any windows showing this buffer
        (dolist (win (get-buffer-window-list buffer nil t))
          (set-window-point win final-pos)))
      (setq buffer-read-only t))))

(provide 'crs-render)
;;; crs-render.el ends here
