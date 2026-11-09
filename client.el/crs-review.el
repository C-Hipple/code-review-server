;;; crs-review.el --- Single-PR fetch/display, navigation and the main review mode. -*- lexical-binding: t; -*-

;;; Commentary:

;; Single-PR fetch/display, navigation and the main review mode.

;;; Code:

(require 'crs-vars)
(require 'crs-rpc)
(require 'crs-render)
(require 'markdown-mode)

(declare-function crs--get-comment-context "crs-comments")
(declare-function crs--filtered-diff-displayed-p "crs-filtered-diff")
(declare-function crs--full-diff-position "crs-filtered-diff")
(declare-function crs-add-or-edit-comment "crs-comments")
(declare-function crs-delete-local-comment "crs-comments")
(declare-function crs-submit-comment "crs-comments")
(declare-function crs-abort-comment "crs-comments")
(declare-function crs-expand-hunk-before "crs-comments")
(declare-function crs-expand-hunk-after "crs-comments")
(declare-function crs-submit-review "crs-review-actions")
(declare-function crs-set-review-feedback "crs-review-actions")
(declare-function crs-approve-review "crs-review-actions")
(declare-function crs-comment-review "crs-review-actions")
(declare-function crs-request-changes-review "crs-review-actions")
(declare-function crs-get-plugin-output "crs-plugins")
(declare-function crs-get-single-plugin-output "crs-plugins")
(declare-function crs-run-on-demand-plugin "crs-plugins")
(declare-function crs-rerun-plugin "crs-plugins")

(defvar-keymap my-code-review-mode-map
  :doc "Keymap for `my-code-review-mode'.  Bindings are defined in crs-client.el.")

(define-derived-mode my-code-review-mode fundamental-mode "Code Review"
  "Major mode for viewing code reviews."
  (highlight-at-at-lines-blue)
  (highlight-review-comments)
  (add-to-invisibility-spec '(codereview-hide . t))
  ;; Headers of hunks drawn as part of the one above (`crs--show-hunk-expansions').
  (add-to-invisibility-spec 'crs-joined-hunk)
  (add-hook 'post-command-hook #'crs--maybe-show-collapsed-comments nil t))


(defun crs--review-buffer-name (owner repo number)
  "Return the review buffer name for OWNER/REPO PR NUMBER."
  (format "* CRS: #%d - %s - %s *" number repo owner))

(defun crs-get-review (owner repo number)
  "Call the GetPR RPC method for OWNER/REPO PR NUMBER and display the result."
  (interactive "sOwner: \nsRepo: \nnPR Number: ")
  (unless (and crs--process
               (process-live-p crs--process))
    (crs-start-server)
    (sleep-for 0.5))

  (crs--send-request
   "RPCHandler.GetPR"
   (vector (list (cons 'Owner owner)
                 (cons 'Repo repo)
                 (cons 'Number number)))
   (lambda (result)
     (when crs-debug
       (message "DEBUG GetPR result: %s"
                (truncate-string-to-width (format "%S" result)
                                          crs-debug-max-length nil nil t)))
     (let* ((buffer (get-buffer-create (crs--review-buffer-name owner repo number)))
            (project-path (expand-file-name (concat "~/" repo)))
            (error-info (cdr (assq 'error result))))
       (with-current-buffer buffer
         (setq crs--buffer-owner owner)
         (if (file-directory-p project-path)
             (cd project-path)
           (message "Directory not found: %s" project-path)))
       (if error-info
           (with-current-buffer buffer
             (let ((inhibit-read-only t))
               (erase-buffer)
               (insert (format "Error loading review: %s"
                               (if (stringp error-info) error-info (cdr (assq 'message error-info)))))
               (my-code-review-mode)))
         ;; Pass the whole result to crs--render-and-update
         (crs--render-and-update buffer result))
       (pop-to-buffer buffer)
       (message "Review loaded into buffer")))))

(defun crs--navigate-pr (previous)
  "Navigate to the adjacent PR relative to the current review buffer.
If PREVIOUS is non-nil, navigate to the previous PR; otherwise navigate to the next PR."
  (let* ((info (crs--get-current-review-info))
         (owner (nth 0 info))
         (repo (nth 1 info))
         (number (nth 2 info))
         (direction (if previous "previous" "next")))
    (message "Navigating to %s PR from %s/%s #%d..." direction owner repo number)
    (crs--send-request
     "RPCHandler.GetAdjacentPR"
     (vector (list (cons 'Owner owner)
                   (cons 'Repo repo)
                   (cons 'Number number)
                   (cons 'Previous (if previous t :json-false))))
     (lambda (result)
       (let ((err (cdr (assq 'error result))))
         (if err
             (message "No %s PR: %s" direction
                      (if (stringp err) err (cdr (assq 'message err))))
           (let* ((metadata (cdr (assq 'metadata result)))
                  (url (cdr (assq 'url metadata)))
                  (pr-info (when (and url (string-match

                                           "github\\.com/\\([^/]+\\)/\\([^/]+\\)/pull/\\([0-9]+\\)"
                                           url))
                             (list (match-string 1 url)
                                   (match-string 2 url)
                                   (string-to-number (match-string 3 url)))))
                  (adj-owner (or (nth 0 pr-info) owner))
                  (adj-repo (or (nth 1 pr-info) repo))
                  (adj-number (or (nth 2 pr-info) (cdr (assq 'number metadata))))
                  (buffer (get-buffer-create (crs--review-buffer-name
                                              adj-owner adj-repo adj-number)))
                  (project-path (expand-file-name (concat "~/" adj-repo))))
             (with-current-buffer buffer
               (when (file-directory-p project-path)
                 (cd project-path)))
             (crs--render-and-update buffer result)
             (pop-to-buffer buffer)
             (message "Navigated to %s PR: %s/%s #%d" direction adj-owner adj-repo adj-number))))))))

(defun crs-next-pr ()
  "Navigate to the next PR in the review list."
  (interactive)
  (crs--navigate-pr nil))

(defun crs-prev-pr ()
  "Navigate to the previous PR in the review list."
  (interactive)
  (crs--navigate-pr t))

(defun crs-start-review-at-point ()
  "Parse a GitHub PR URL from the current line and call crs-get-review.
The line should contain a URL in the format https://github.com/OWNER/REPO/pull/NUMBER"
  (interactive)
  (let ((line (buffer-substring-no-properties (line-beginning-position) (line-end-position))))
    (if (string-match "github\\.com/\\([^/]+\\)/\\([^/]+\\)/pull/\\([0-9]+\\)" line)
        (let* ((owner (match-string 1 line))
               (repo (match-string 2 line))
               (number (string-to-number (match-string 3 line)))
               (project-path (expand-file-name (concat "~/" repo))))
          (if (file-directory-p project-path)
              (cd project-path)
            (message "Directory not found: %s" project-path))
          (crs-get-review owner repo number))
      (error "Could not find GitHub PR URL on current line"))))

(defun crs-visit-file ()
  "Visit the file at point in the code review buffer.
If point is on a URL: line, open the URL in the browser instead."
  (interactive)
  (let ((line-text (buffer-substring-no-properties (line-beginning-position) (line-end-position))))
    (if (string-match "^URL:[ \t]+\\(https?://[^ \t\n]+\\)" line-text)
        (browse-url (match-string 1 line-text))
      (let* ((ctx (crs--get-comment-context))
             (filename (nth 3 ctx))
             (line (nth 8 ctx)))
        (if (and filename (not (string= filename "")))
            (if (file-exists-p filename)
                (progn
                  (find-file filename)
                  (when line
                    (goto-char (point-min))
                    (forward-line (1- line))
                    (recenter)))
              (message "File not found: %s" filename))
          (message "No file found at point"))))))

(defun crs--github-diff-link (pr-url filename &optional side line)
  "The GitHub link to LINE on SIDE of FILENAME in the diff of the PR at PR-URL.
PR-URL is the PR's page, https://github.com/OWNER/REPO/pull/N.  SIDE is
\"L\" (the base) or \"R\" (the head), as GitHub's Files changed view writes
them when a line number is clicked; its anchor names the file by the hex
SHA-256 of its path.  Without SIDE and LINE the link goes to the file."
  (let ((base (replace-regexp-in-string
               "/\\(?:files\\|changes\\|commits\\)/?\\'" ""
               (string-trim-right pr-url "/+")))
        (anchor (concat "diff-" (secure-hash 'sha256 (encode-coding-string filename 'utf-8)))))
    (if (and side line)
        (format "%s/files#%s%s%d" base anchor side line)
      (format "%s/files#%s" base anchor))))

(defun crs--diff-base-line-at-position (diff filename position)
  "The base-side line number of the row at POSITION of FILENAME in DIFF.
DIFF is a raw unified diff, POSITION a GitHub diff position (the file's first
hunk header is 0, and every later header and content line counts one).  Nil
when that row is an added line, or DIFF has no such row."
  (with-temp-buffer
    (insert diff)
    (goto-char (point-min))
    (let ((in-target nil)
          (count nil)
          (base-line nil)
          (found nil)
          (done nil))
      (while (and (not done) (not (eobp)))
        (let ((line (buffer-substring-no-properties
                     (line-beginning-position) (line-end-position))))
          (cond
           ((string-match "^diff --git a/\\(.*?\\) b/\\(.*?\\)$" line)
            (setq in-target (string= (if (string= (match-string 2 line) "dev/null")
                                         (match-string 1 line)
                                       (match-string 2 line))
                                     filename)
                  count nil))
           ((not in-target) nil)
           ((string-match "^@@ -\\([0-9]+\\)" line)
            (setq count (if count (1+ count) 0)
                  base-line (string-to-number (match-string 1 line))))
           ((null count) nil)
           ((memq (and (> (length line) 0) (aref line 0)) '(?+ ?- ?\s))
            (setq count (1+ count))
            (when (= count position)
              (setq found (unless (eq (aref line 0) ?+) base-line)
                    done t))
            (unless (eq (aref line 0) ?+)
              (setq base-line (1+ base-line))))))
        (forward-line 1))
      found)))

(defun crs--github-line-target (ctx)
  "Where the GitHub link for the line CTX describes points: (SIDE . LINE).
CTX is a `crs--get-comment-context' list.  A removed line links its base
line (\"L\"), any other its head line (\"R\"), as GitHub's own line numbers
do; nil, to link the file, on a hunk header or a line with no number.

On a filtered diff a removed line's base number counts the baseline's lines,
not the PR base's, so it is read from the same row of the full diff instead;
a row the full diff shows as an addition links its head line, and one it
doesn't show, the file."
  (let ((filename (nth 3 ctx))
        (position (nth 4 ctx))
        (head-line (nth 8 ctx))
        (base-line (nth 10 ctx))
        (marker (nth 11 ctx)))
    (cond
     ((null marker) nil)
     ((not (eq marker ?-)) (and head-line (cons "R" head-line)))
     ((not (crs--filtered-diff-displayed-p)) (and base-line (cons "L" base-line)))
     (t
      (let* ((full-pos (crs--full-diff-position filename position))
             (full-base (and full-pos crs--buffer-full-diff
                             (crs--diff-base-line-at-position
                              crs--buffer-full-diff filename full-pos))))
        (cond (full-base (cons "L" full-base))
              ;; The full diff shows it as an addition, or not at all.
              ((and full-pos head-line) (cons "R" head-line))))))))

(defun crs-copy-github-line-link ()
  "Copy the GitHub link to the diff line at point to the kill ring.
The link opens the PR's Files changed view at that line, the link GitHub
copies when one of its line numbers is clicked: a removed line by its base
line number (L), any other by its head one (R).  On a comment block, the
line the block hangs off; on a file or hunk header, the file."
  (interactive)
  (let* ((ctx (crs--get-comment-context))
         (owner (nth 0 ctx))
         (repo (nth 1 ctx))
         (number (nth 2 ctx))
         (filename (nth 3 ctx)))
    (unless (and filename (not (string-empty-p filename)))
      (user-error "No diff line at point"))
    (let* ((pr-url (or (cdr (assq 'url crs--buffer-metadata))
                       (format "https://github.com/%s/%s/pull/%d" owner repo number)))
           (target (crs--github-line-target ctx))
           (link (crs--github-diff-link pr-url filename (car target) (cdr target))))
      (kill-new link)
      (message "Copied GitHub link to %s%s"
               filename
               (if target (format ":%s%d" (car target) (cdr target)) "")))))

(defun crs-show-outdated-comments ()
  "Show outdated comments for the current PR in a separate buffer."
  (interactive)
  (unless (and (boundp 'crs--buffer-outdated-comments)
               crs--buffer-outdated-comments
               (not (seq-empty-p crs--buffer-outdated-comments)))
    (error "No outdated comments found for this PR"))

  (let* ((owner crs--comment-owner)
         (repo crs--comment-repo)
         (number crs--comment-number)
         (info (crs--get-current-review-info))
         (owner (nth 0 info))
         (repo (nth 1 info))
         (number (nth 2 info))
         (buffer-name (format "* Outdated Comments %s #%d *" repo number))
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
                          (body (or (cdr (assq 'body c)) "(No body)"))
                          (reacted (crs--format-reactions (cdr (assq 'reactions c)))))
                      (insert (format "[%s] %s on %s:\n" author time path))
                      (crs--insert-html body "  ")
                      ;; A comment on code that has since changed is exactly the
                      ;; one worth knowing was acknowledged before it went stale.
                      (when reacted
                        (insert (format "\n  %s" reacted)))
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
  :doc "Keymap for `crs-outdated-comments-mode'.  Bindings are defined in crs-client.el.")

(define-derived-mode crs-outdated-comments-mode markdown-mode "Outdated Comments"
  "Major mode for viewing outdated comments.
  Inherits from markdown-mode and is read-only.
  \\{crs-outdated-comments-mode-map}"
  (setq buffer-read-only t))

(defun crs--get-current-review-info ()
  "Extract (owner repo number) from the current review buffer's name.
Returns a list (owner repo number) or signals an error if not in a
review buffer."
  (let ((name (buffer-name)))
    (if (string-match
         "\\* CRS: #\\([0-9]+\\) - \\([^[:space:]]+\\) - \\([^[:space:]]+\\) \\*"
         name)
        (list (match-string 3 name)
              (match-string 2 name)
              (string-to-number (match-string 1 name)))
      (error "Not in a valid review buffer: %s" name))))


(defun crs-sync-pr ()
  "Sync the PR in the current buffer with the server."
  (interactive)
  (let ((owner nil)
        (repo nil)
        (number nil))
    (let ((info (crs--get-current-review-info)))
      (setq owner (nth 0 info)
            repo (nth 1 info)
            number (nth 2 info)))

    (message "Syncing PR %s/%s #%d..." owner repo number)
    (crs--send-request
     "RPCHandler.SyncPR"
     (vector (list (cons 'Owner owner)
                   (cons 'Repo repo)
                   (cons 'Number number)))
     (lambda (result)
       (let ((err (cdr (assq 'error result))))
         (if err
             (message "Error syncing review: %s" (if (stringp err) err (cdr (assq 'message err))))
           (let ((review-buffer (get-buffer (crs--review-buffer-name owner repo number)))
                 ;; `updated' is t when the sync pulled in a new head SHA or
                 ;; new comments; json.el decodes false as :json-false.
                 (updated (eq (cdr (assq 'updated result)) t)))
             (when review-buffer
               (crs--render-and-update review-buffer result))
             (message (if updated
                          "PR synced: new commits or comments pulled in"
                        "PR synced: already up to date")))))))))


(defun crs--switch-and-fetch (project-name branch-name)
  "Switch to the project directory, fetch, and checkout the branch.
PROJECT-NAME is the name of the project directory in ~/
BRANCH-NAME is the name of the branch to checkout."
  (let ((project-dir (expand-file-name (concat "~/" project-name))))
    (when (file-directory-p project-dir)
      (cd project-dir)
      (shell-command (concat "git fetch && git checkout " branch-name)))
    (unless (file-directory-p project-dir)
      (error "Project directory %s not found" project-dir))))



(defun crs--get-ref-name ()
  "Extract the branch name from the current crs buffer."
  (interactive)
  (save-excursion
    (goto-char (point-min))
    (when (re-search-forward "^Refs:[[:space:]]+\\([^[:space:]]+\\)[[:space:]]+\\.\\.\\.[[:space:]]+\\([^[:space:]\n\r]+\\)" nil t)
      (match-string 2))))

(defun crs-checkout-current-project ()
  (interactive)
  (if-let ((ref-name (crs--get-ref-name)))
      (progn
        (message "Checking out: %s" ref-name)
        (crs--switch-and-fetch (projectile-project-name) ref-name))
    (message "Warning: Could not find branch name in Refs line")))

(provide 'crs-review)
;;; crs-review.el ends here
