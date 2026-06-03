;;; crs-comments.el --- Comment authoring, diff-position logic and hunk expansion. -*- lexical-binding: t; -*-

;;; Commentary:

;; Comment authoring, diff-position logic and hunk expansion.

;;; Code:

(require 'crs-vars)
(require 'crs-rpc)
(require 'crs-render)
(require 'markdown-mode)

(declare-function crs--get-current-review-info "crs-review")
(declare-function crs--render-and-update "crs-render")
(declare-function crs--review-buffer-name "crs-review")

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
                 (let ((review-buffer (get-buffer (crs--review-buffer-name owner repo number))))
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
               (let ((review-buffer (get-buffer (crs--review-buffer-name owner repo number))))
                 (when review-buffer
                   (crs--render-and-update review-buffer result original-line original-context))
                 (message "Comment added successfully")
                 (kill-buffer-and-window))))))))))

(defun crs-abort-comment ()
  "Abort the comment in the current buffer."
  (interactive)
  (kill-buffer-and-window)
  (message "Comment aborted."))

(define-derived-mode comment-edit-mode markdown-mode "Code Review Comment"
  "Major mode for editing code review comments."
  (local-set-key (kbd "C-c C-c") 'crs-submit-comment)
  (local-set-key (kbd "C-c C-k") 'crs-abort-comment))

(when (fboundp 'evil-define-key)
  (evil-define-key 'normal comment-edit-mode-map
    "C-c C-c" #'crs-submit-comment
    "C-c C-k" #'crs-abort-comment
    ", c" #'crs-submit-comment
    ", k" #'crs-abort-comment
    )
  (evil-define-key 'insert comment-edit-mode-map
    "C-c C-c" #'crs-submit-comment
    "C-c C-k" #'crs-abort-comment)
  (evil-define-key 'visual comment-edit-mode-map
    "C-c C-c" #'crs-submit-comment
    "C-c C-k" #'crs-abort-comment))

;; Buffer-local variables for plugin output mode

(defun crs--find-first-hunk-line ()
  "Find the line number of the first hunk header after point, bounded by the next file header."
  (save-excursion
    (let* ((start-pos (point))
           (search-bound (save-excursion
                           (forward-line 1)
                           (if (re-search-forward "^\\(?:[^[:space:]].*?[[:space:]]\\)?\\(?:modified\\|deleted\\|new file\\|renamed\\)[[:space:]:]+" nil t)
                               (match-beginning 0)
                             (point-max)))))
      (goto-char start-pos)
      (if (search-forward "@@" search-bound t)
          (line-number-at-pos)
        (progn
          (message "Failed to find hunk header between line %d and %d"
                   (line-number-at-pos start-pos) (line-number-at-pos search-bound))
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
                  ;; Check if this is a local comment or local reply
                  (goto-char block-start)
                  (let ((block-end (save-excursion
                                     (if (re-search-forward "^    └" nil t)
                                         (point)
                                       (point-max)))))
                    (let ((local-reply-match
                           (save-excursion
                             (goto-char block-start)
                             (re-search-forward "^    │ Reply by \\[local\\]:\\([0-9]+\\)" block-end t))))
                      (if local-reply-match
                          ;; Local reply - ID is embedded in the "Reply by [local]:[ID]" line
                          (progn
                            (setq local-comment-id (string-to-number (match-string 1)))
                            (message "Found local reply ID: %d" local-comment-id)
                            (setq reply-to-id nil)
                            ;; Extract the body - lines after "Reply by [local]:[ID]"
                            (goto-char local-reply-match)
                            (forward-line 1)
                            (let ((body-lines nil))
                              (while (and (< (point) block-end)
                                          (looking-at "^    │   \\(.*\\)$"))
                                (push (match-string 1) body-lines)
                                (forward-line 1))
                              (when body-lines
                                (setq local-comment-body
                                      (string-join (nreverse body-lines) "\n")))))
                        ;; Local root comment - ID is on line 3 of the block
                        (when (re-search-forward "^    │ \\[local\\]:" block-end t)
                          (goto-char block-start)
                          (forward-line 2)
                          (let ((header-line (buffer-substring-no-properties (line-beginning-position) (line-end-position))))
                            (when (string-match " : \\([0-9]+\\)$" header-line)
                              (setq local-comment-id (string-to-number (match-string 1 header-line)))
                              (message "Found local comment ID: %d" local-comment-id)
                              (setq reply-to-id nil)))
                          ;; Extract the body - lines after [local]:
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
                                      (string-join (nreverse body-lines) "\n"))))))))))
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
              (file-line nil)
              (first-hunk-counted nil))
          (while (<= (line-number-at-pos) target-line)
            (let ((line-content (buffer-substring-no-properties (line-beginning-position) (line-end-position))))
              ;; The position computed here is sent verbatim to GitHub and is
              ;; also what the renderer uses to place comments, so this MUST
              ;; agree line-for-line with `crs--render-diff' and
              ;; `crs--find-position-in-diff'.
              (cond
               ;; Hunk header.  Lengths are optional for single-line hunks
               ;; (e.g. "@@ -3 +4 @@"), matching `crs--parse-hunk-header'.  The
               ;; first hunk header anchors position 0; every later hunk header
               ;; advances the position by 1, as GitHub counts them.
               ((string-match "^@@ -[0-9]+\\(?:,[0-9]+\\)? \\+\\([0-9]+\\)\\(?:,[0-9]+\\)? @@" line-content)
                (if (not first-hunk-counted)
                    (setq first-hunk-counted t)
                  (setq count (1+ count)))
                (setq file-line (string-to-number (match-string 1 line-content)))
                (setq target-file-line file-line))
               ;; Skip interleaved review-comment blocks.
               ((string-match-p "^[[:cntrl:][:space:]]*[│┌└]" line-content)
                nil)
               ;; Content line.  Only lines carrying a diff prefix (+, -, or
               ;; space) advance the position.  This excludes the blank
               ;; separator lines emitted by formatDiff and markers such as
               ;; "\ No newline at end of file", neither of which GitHub counts.
               ((or (string-prefix-p "+" line-content)
                    (string-prefix-p "-" line-content)
                    (string-prefix-p " " line-content))
                (setq count (1+ count))
                (setq target-file-line file-line)
                (when (and file-line (not (string-prefix-p "-" line-content)))
                  (setq file-line (1+ file-line))))
               ;; Anything else is not part of the diff position; skip it.
               (t nil)))
            (forward-line 1))
          (setq position count))))

    (let ((ctx (list owner repo number filename position reply-to-id local-comment-id local-comment-body target-file-line)))
      (message "Context extracted: %S" ctx)
      (message (concat "Position:" (prin1-to-string position)))
      ctx)))

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
                 (let ((review-buffer (get-buffer (crs--review-buffer-name owner repo number))))
                   (when review-buffer
                     (crs--render-and-update review-buffer result))
                   (message "Local comment deleted")))))))))))


(defun crs--parse-hunk-header (line)
  "Parse a diff \"@@ -a,b +c,d @@\" header LINE.
Returns a plist (:orig-start :orig-length :new-start :new-length :suffix)
or nil if LINE does not match."
  (when (string-match
         "^@@ -\\([0-9]+\\)\\(?:,\\([0-9]+\\)\\)? \\+\\([0-9]+\\)\\(?:,\\([0-9]+\\)\\)? @@\\(.*\\)$"
         line)
    (list :orig-start (string-to-number (match-string 1 line))
          :orig-length (if (match-string 2 line)
                           (string-to-number (match-string 2 line))
                         1)
          :new-start (string-to-number (match-string 3 line))
          :new-length (if (match-string 4 line)
                          (string-to-number (match-string 4 line))
                        1)
          :suffix (or (match-string 5 line) ""))))

(defun crs--find-current-hunk-info ()
  "Return info about the hunk at point in the current review buffer.
The return value is a plist with keys:
  :filename    — file path from the nearest file header above point
  :hunk-index  — 0-based index of the current hunk within that file
  :header-line — buffer line number of the @@ header for this hunk
  :orig-start :orig-length :new-start :new-length :suffix — parsed header fields
Signals a `user-error' when the cursor is not inside a hunk."
  (save-excursion
    (end-of-line)
    (let ((target-line (line-number-at-pos))
          filename hunk-headers)
      ;; 1. Nearest file header above point
      (unless (re-search-backward
               "^\\(?:[^[:space:]].*?[[:space:]]\\)?\\(modified\\|deleted\\|new file\\|renamed\\)[[:space:]:]+\\([^[:space:]\n].*?\\)[[:space:]]*$"
               nil t)
        (user-error "No file header found above point"))
      (setq filename (match-string 2))
      ;; 2. Scan @@ headers between this file header and the next file header (or EOF)
      (forward-line 1)
      (let ((next-file-pos
             (or (save-excursion
                   (when (re-search-forward
                          "^\\(?:[^[:space:]].*?[[:space:]]\\)?\\(?:modified\\|deleted\\|new file\\|renamed\\)[[:space:]:]+"
                          nil t)
                     (line-beginning-position)))
                 (point-max))))
        (while (re-search-forward "^@@ " next-file-pos t)
          (let ((line-start (line-beginning-position))
                (line-end (line-end-position)))
            (push (cons (line-number-at-pos)
                        (buffer-substring-no-properties line-start line-end))
                  hunk-headers))
          (forward-line 1)))
      (setq hunk-headers (nreverse hunk-headers))
      ;; 3. Pick the last hunk header whose line is at or above target-line
      (let ((current nil) (current-idx nil) (idx 0))
        (dolist (h hunk-headers)
          (when (<= (car h) target-line)
            (setq current h)
            (setq current-idx idx))
          (setq idx (1+ idx)))
        (unless current
          (user-error "Not inside a hunk in file %s" filename))
        (let ((parsed (crs--parse-hunk-header (cdr current))))
          (unless parsed
            (error "Could not parse hunk header: %s" (cdr current)))
          (append (list :filename filename
                        :hunk-index current-idx
                        :header-line (car current))
                  parsed))))))

(defun crs--expand-hunk-in-diff (diff filename hunk-index direction new-lines new-header)
  "Return an updated DIFF string with context added to a specific hunk.
FILENAME selects the file, HUNK-INDEX is the 0-based hunk index within that
file, DIRECTION is \"before\" or \"after\", NEW-LINES is a list of raw file
line strings (without leading \" \" prefix), and NEW-HEADER is the updated
\"@@ -a,b +c,d @@ ...\" line to replace the existing one."
  (with-temp-buffer
    (insert diff)
    (goto-char (point-min))
    (let ((current-file nil)
          (in-target nil)
          (hunk-count 0)
          (done nil))
      (while (and (not done) (not (eobp)))
        (let ((line (buffer-substring-no-properties
                     (line-beginning-position) (line-end-position))))
          (cond
           ;; "diff --git a/foo b/foo" header: start of a new file section
           ((string-match "^diff --git a/\\(.*?\\) b/\\(.*?\\)$" line)
            (let ((file-a (match-string 1 line))
                  (file-b (match-string 2 line)))
              (setq current-file (if (string= file-b "dev/null") file-a file-b))
              (setq in-target (string= current-file filename))
              (setq hunk-count 0))
            (forward-line 1))
           ;; "+++ b/foo" header: resets the current file (used when no "diff --git" is present)
           ((string-match "^\\+\\+\\+ b/\\(.*\\)$" line)
            (setq current-file (match-string 1 line))
            (setq in-target (string= current-file filename))
            (forward-line 1))
           ;; Hunk header inside the target file
           ((and in-target (string-prefix-p "@@ " line))
            (if (= hunk-count hunk-index)
                (progn
                  ;; Replace this header with the new one from the server
                  (delete-region (line-beginning-position) (line-end-position))
                  (insert new-header)
                  (forward-line 1)
                  (cond
                   ((string= direction "before")
                    (dolist (nl new-lines)
                      (insert " " nl "\n")))
                   ((string= direction "after")
                    ;; Walk past the body of this hunk, stopping at the next
                    ;; hunk header or file header (or EOF).
                    (while (and (not (eobp))
                                (let ((l (buffer-substring-no-properties
                                          (line-beginning-position)
                                          (line-end-position))))
                                  (not (or (string-prefix-p "@@ " l)
                                           (string-prefix-p "diff --git" l)))))
                      (forward-line 1))
                    (dolist (nl new-lines)
                      (insert " " nl "\n"))))
                  (setq done t))
              (setq hunk-count (1+ hunk-count))
              (forward-line 1)))
           (t
            (forward-line 1))))))
    (buffer-string)))

(defun crs--expand-hunk (direction count)
  "Expand the hunk at point by COUNT lines in DIRECTION.
DIRECTION must be \"before\" or \"after\"."
  (unless crs--buffer-diff
    (user-error "No PR diff loaded in this buffer"))
  (let* ((info (crs--get-current-review-info))
         (owner (nth 0 info))
         (repo (nth 1 info))
         (number (nth 2 info))
         (hunk (crs--find-current-hunk-info))
         (filename (plist-get hunk :filename))
         (hunk-index (plist-get hunk :hunk-index))
         (header-line (plist-get hunk :header-line))
         (new-start (plist-get hunk :new-start))
         (new-length (plist-get hunk :new-length))
         (orig-start (plist-get hunk :orig-start))
         (orig-length (plist-get hunk :orig-length))
         (suffix (string-trim (or (plist-get hunk :suffix) "")))
         (anchor (if (string= direction "before")
                     new-start
                   (+ new-start (max 0 (1- new-length)))))
         (current-line (line-number-at-pos))
         (buffer (current-buffer)))
    (when (< anchor 1)
      (user-error "Cannot expand: hunk anchor out of range"))
    (message "Fetching %d lines %s hunk %d of %s..." count direction (1+ hunk-index) filename)
    (crs--send-request
     "RPCHandler.GetHunkContext"
     (vector (list (cons 'Owner owner)
                   (cons 'Repo repo)
                   (cons 'Number number)
                   (cons 'Filename filename)
                   (cons 'Side "new")
                   (cons 'AnchorLine anchor)
                   (cons 'Direction direction)
                   (cons 'Count count)
                   (cons 'OrigStart orig-start)
                   (cons 'OrigLength orig-length)
                   (cons 'NewStart new-start)
                   (cons 'NewLength new-length)
                   (cons 'HunkHeader suffix)))
     (lambda (result)
       (let* ((err (cdr (assq 'error result)))
              (raw-lines (cdr (assq 'lines result)))
              (lines (if (vectorp raw-lines) (append raw-lines nil) raw-lines))
              (new-range (cdr (assq 'range_header result))))
         (cond
          (err
           (message "Error expanding hunk: %s"
                    (if (stringp err) err (cdr (assq 'message err)))))
          ((or (null lines) (zerop (length lines)))
           (message "No more context %s this hunk" direction))
          (t
           (let* ((added (length lines))
                  (new-diff (crs--expand-hunk-in-diff
                             (buffer-local-value 'crs--buffer-diff buffer)
                             filename hunk-index direction lines new-range))
                  (target-line (if (and (string= direction "before")
                                        (> current-line header-line))
                                   (+ current-line added)
                                 current-line)))
             (with-current-buffer buffer
               (setq crs--buffer-diff new-diff)
               (crs--render-and-update buffer nil target-line))
             (message "Expanded hunk %s by %d line%s"
                      direction added
                      (if (= added 1) "" "s"))))))))))

(defun crs-expand-hunk-before (&optional count)
  "Expand the hunk at point upward with more context lines.
With a numeric prefix, fetch COUNT lines (default 20, max 100)."
  (interactive "P")
  (crs--expand-hunk "before"
                    (if count (prefix-numeric-value count) 20)))

(defun crs-expand-hunk-after (&optional count)
  "Expand the hunk at point downward with more context lines.
With a numeric prefix, fetch COUNT lines (default 20, max 100)."
  (interactive "P")
  (crs--expand-hunk "after"
                    (if count (prefix-numeric-value count) 20)))

(provide 'crs-comments)
;;; crs-comments.el ends here
