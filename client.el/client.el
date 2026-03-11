;;; crs-client.el --- JSON-RPC client for codereviewserver -*- lexical-binding: t; -*-

;; Author: Chris Hipple
;; Version: 0.1.0
;; Package-Requires: ((emacs "25.1"))

;; SPDX-License-Identifier: GPL-3.0+

;;; Commentary:

;; This package provides JSON-RPC client functionality for crs (codereviewserver).
;; It allows starting the server process and making RPC calls to it.
;;
;; The package is split across multiple files:
;;   crs-utils.el    — HTML helpers, diff utilities, review-info extraction
;;   crs-comments.el — Comment/review CRUD, modes, context extraction
;;   crs-plugins.el  — Plugin listing, output display and mode
;;   client.el       — Server process management, RPC, rendering, main mode

;;; Code:

(require 'json)
(require 'washer)
(require 'markdown-mode)
(require 'seq)
(require 'subr-x)
(require 'crs-utils)
(require 'crs-comments)
(require 'crs-plugins)

;;; Server process state

(defvar crs--process nil
  "The process handle for the crs JSON-RPC server.")

(defvar crs--request-id 1
  "Counter for JSON-RPC request IDs.")

(defvar crs--pending-requests (make-hash-table :test 'equal)
  "Hash table mapping request IDs to callback functions.")

(defvar crs--response-buffer ""
  "Buffer for accumulating partial JSON-RPC responses.")

(defvar crs--section-header-regexp
  "^\\(?:[^[:space:]].*?[[:space:]]\\)?\\(?:\\(?:\\.\\.\\.\\)?\\(?:modified\\|deleted\\|new file\\|renamed\\)[[:space:]:]+.*\\|Commits .*\\|Description\\|Conversation\\|Your Review Feedback\\|Files changed .*\\)$"
  "Regexp to match section headers in the code review buffer.")

;;; Buffer-local variables for storing PR data

(defvar-local crs--buffer-diff nil
  "The raw diff content for the current PR.")
(defvar-local crs--buffer-comments nil
  "The comments list for the current PR.")
(defvar-local crs--buffer-outdated-comments nil
  "The outdated comments list for the current PR.")
(defvar-local crs--buffer-metadata nil
  "The PR metadata for the current PR.")
(defvar-local crs--buffer-reviews nil
  "The reviews list for the current PR.")
(defvar-local crs--buffer-preamble nil
  "The preamble content (header + conversation) for the current PR.")
(defvar-local crs--buffer-show-comments t
  "Whether to show comments in the buffer. Toggle with `crs-toggle-comments'.")
(defvar-local crs--buffer-review-feedback nil
  "The review feedback for the current PR.")

;;; Server lifecycle

;;;###autoload
(defun crs-start-server ()
  "Start the crs JSON-RPC server process.
Returns the process handle."
  (interactive)
  (if (and crs--process
           (process-live-p crs--process))
      (progn
        (message "Server is already running")
        crs--process)
    (progn
      (let ((stderr-buffer (get-buffer-create "*crs-client-stderr*")))
        (setq crs--process
              (make-process :name "crs-client"
                            :buffer "*crs-client*"
                            :command '("crs" "-server")
                            :connection-type 'pipe
                            :coding 'utf-8
                            :stderr stderr-buffer
                            :noquery t))

        (set-process-filter crs--process 'crs--process-filter)
        (set-process-sentinel crs--process 'crs--process-sentinel)

        ;; Give the process a moment to start up
        (sleep-for 0.2)

        ;; Check if process is still alive after startup
        (unless (process-live-p crs--process)
          (let ((stderr-content (with-current-buffer stderr-buffer (buffer-string))))
            (error "Server process died immediately. Stderr: %s" stderr-content)))))

    (message "Started crs JSON-RPC server")
    (crs-list-plugins)
    crs--process))

;;;###autoload
(defun crs-shutdown-server ()
  "Stop the crs JSON-RPC server process if it is running."
  (interactive)
  (if (and crs--process
           (process-live-p crs--process))
      (progn
        (delete-process crs--process)
        (setq crs--process nil)
        (message "Stopped crs JSON-RPC server"))
    (message "Server is not running")))

;;;###autoload
(defun crs-restart-server ()
  "Restart the crs JSON-RPC server process.
Stops the existing server if running, then starts a new one.
Useful after recompiling the Go server binary."
  (interactive)
  (crs-shutdown-server)
  (sleep-for 0.2)  ; Give the process a moment to fully shut down
  (crs-start-server))

;;; Process I/O

(defun crs--process-filter (process output)
  "Filter function for processing JSON-RPC responses from the server."
  (setq crs--response-buffer
        (concat crs--response-buffer output))

  ;; Process complete lines (JSON-RPC uses newline-delimited JSON)
  (while (string-match "\n" crs--response-buffer)
    (let ((line (substring crs--response-buffer 0 (match-beginning 0))))
      (setq crs--response-buffer
            (substring crs--response-buffer (match-end 0)))

      (when (> (length line) 0)
        (crs--handle-response line)))))

(defun crs--handle-response (response-line)
  "Handle a single JSON-RPC response line."
  (message "DEBUG crs response: %s" response-line)
  (condition-case err
      (let ((response (json-read-from-string response-line)))
        (let ((id (cdr (assq 'id response)))
              (result (cdr (assq 'result response)))
              (error (cdr (assq 'error response))))
          (if error
              (let ((callback (gethash id crs--pending-requests)))
                (message "JSON-RPC Error: %s" (if (stringp error) error (cdr (assq 'message error))))
                (when callback
                  (remhash id crs--pending-requests)
                  (funcall callback `((error . ,error)))))
            (let ((callback (gethash id crs--pending-requests)))
              (when callback
                (remhash id crs--pending-requests)
                (funcall callback result))))))
    (error
     (message "Error parsing JSON-RPC response: %s" err))))

(defun crs--process-sentinel (process event)
  "Sentinel function for the crs process."
  (when (memq (process-status process) '(exit signal))
    (let ((buffer (process-buffer process))
          (stderr-buffer (get-buffer "*crs-client-stderr*")))
      (when buffer
        (with-current-buffer buffer
          (let ((output (buffer-string)))
            (when (> (length output) 0)
              (message "crs stdout: %s" output)))))
      (when stderr-buffer
        (with-current-buffer stderr-buffer
          (let ((stderr-output (buffer-string)))
            (when (> (length stderr-output) 0)
              (message "crs stderr: %s" stderr-output))))))
    (setq crs--process nil)
    (message "crs process %s" event)))

(defun crs--send-request (method params callback)
  "Send a JSON-RPC request to the server.
METHOD is the method name (e.g., 'RPCHandler.GetAllReviews').
PARAMS is the parameters array.
CALLBACK is a function to call with the result."
  (unless (and crs--process
               (process-live-p crs--process))
    (error "Server is not running. Call crs-start-server first"))

  (let ((id crs--request-id))
    (setq crs--request-id (1+ crs--request-id))

    (puthash id callback crs--pending-requests)

    (let ((request (json-encode `((jsonrpc . "2.0")
                                  (method . ,method)
                                  (params . ,params)
                                  (id . ,id)))))
      (process-send-string crs--process
                           (concat request "\n")))))

;;; Reviews list

;;;###autoload
(defun crs-get-reviews ()
  "Call the GetAllReviews RPC method and display the result in '* Reviews *' buffer."
  (interactive)
  (unless (and crs--process
               (process-live-p crs--process))
    (crs-start-server)
    ;; Give the server a moment to start
    (sleep-for 0.5))

  (crs--send-request
   "RPCHandler.GetAllReviews"
   (vector)
   (lambda (result)
     (let ((content (cdr (assq 'content result)))
           (buffer (get-buffer-create "* Reviews *")))
       (with-current-buffer buffer
         (erase-buffer)
         (insert (or content ""))
         (crs--process-html-placeholders)
         (goto-char (point-min))
         (org-mode))
       (display-buffer buffer)
       (message "Reviews loaded into '* Reviews *' buffer")))))

;;; Section folding

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

;;; Main review mode

(defvar-keymap my-code-review-mode-map
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
  "H" #'crs-toggle-comments
  "f" #'crs-set-review-feedback
  "RET" #'crs-visit-file
  "<return>" #'crs-visit-file
  "O" #'crs-show-outdated-comments
  "q" #'quit-window
  )

(define-derived-mode my-code-review-mode fundamental-mode "Code Review"
  "Major mode for viewing code reviews."
  (highlight-at-at-lines-blue)
  (highlight-review-comments)
  (add-to-invisibility-spec '(codereview-hide . t))
  (add-hook 'post-command-hook #'crs--maybe-show-collapsed-comments nil t))

;; Override evil-mode keybindings - define keys for normal and visual states
(when (fboundp 'evil-define-key)
  ;; Define keys for normal state
  (evil-define-key 'normal my-code-review-mode-map
    "TAB" #'crs-toggle-section
    "<tab>" #'crs-toggle-section
    "<backtab>" #'crs-collapse-all-sections
    "c" #'crs-add-or-edit-comment
    "d" #'crs-delete-local-comment
    "C-c C-c" #'crs-submit-review
    "ra" #'crs-approve-review
    "rc" #'crs-comment-review
    "rr" #'crs-request-changes-review
    "rg" #'crs-sync-pr
    "rf" #'crs-set-review-feedback
    "p" #'crs-get-plugin-output
    "P" #'crs-get-single-plugin-output
    "H" #'crs-toggle-comments
    "f" #'crs-set-review-feedback
    "RET" #'crs-visit-file
    "O" #'crs-show-outdated-comments
    "q" #'quit-window)
  ;; Define keys for visual state
  (evil-define-key 'visual my-code-review-mode-map
    "TAB" #'crs-toggle-section
    "<tab>" #'crs-toggle-section
    "<backtab>" #'crs-collapse-all-sections
    "c" #'crs-add-or-edit-comment
    "d" #'crs-delete-local-comment
    "C-c C-c" #'crs-submit-review
    "ra" #'crs-approve-review
    "rc" #'crs-comment-review
    "rr" #'crs-request-changes-review
    "rg" #'crs-sync-pr
    "rf" #'crs-set-review-feedback
    "p" #'crs-get-plugin-output
    "P" #'crs-get-single-plugin-output
    "H" #'crs-toggle-comments
    "f" #'crs-set-review-feedback
    "RET" #'crs-visit-file
    "O" #'crs-show-outdated-comments
    "q" #'quit-window)
  ;; Define keys for insert state
  (evil-define-key 'insert my-code-review-mode-map
    "C-c C-c" #'crs-submit-review))

;;; Diff rendering

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
          (body (cdr (assq 'body metadata)))
          (sb ""))

      (setq sb (concat sb (format "Title: #%s: %s\n" number title)))
      (setq sb (concat sb (format "Author: \t@%s\n" author)))
      (setq sb (concat sb (format "Title: \t%s\n" title)))
      (setq sb (concat sb (format "Refs:  %s ... %s\n" base head)))
      (setq sb (concat sb (format "URL:   %s\n" url)))
      (setq sb (concat sb (format "State: \t%s\n" state)))
      (setq sb (concat sb (format "Milestone: \t%s\n" (or milestone "No milestone"))))

      (let ((labels-str (if (> (length labels) 0) (string-join (append labels nil) ", ") "None yet")))
        (setq sb (concat sb (format "Labels: \t%s\n" labels-str))))

      (setq sb (concat sb "Projects: \tNone yet\n"))
      (setq sb (concat sb (format "Draft: \t%s\n" (if (eq draft t) "true" "false"))))

      (let ((assignees-str (if (> (length assignees) 0) (string-join (append assignees nil) ", ") "No one -- Assign yourself")))
        (setq sb (concat sb (format "Assignees: \t%s\n" assignees-str))))

      (setq sb (concat sb "Suggested-Reviewers: No suggestions\n"))

      (let* ((reviewers-list (append reviewers nil))
             (teams-list (mapcar (lambda (t) (concat "team:" t)) (append teams nil)))
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

(defun crs--render-conversation-from-data (comments reviews &optional outdated-comments)
  "Render the conversation section from COMMENTS, REVIEWS and optionally OUTDATED-COMMENTS."
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
                ;; Skip empty COMMENTED reviews?
                (unless (and (string= state "COMMENTED") (string-empty-p (or body "")))
                  (push (list :type 'review
                              :time (cdr (assq 'submitted_at r))
                              :author (cdr (assq 'user r))
                              :state state
                              :body body)
                        items))))
            reviews)

    ;; 3. Sort by Time
    (setq items (sort items (lambda (a b)
                              (string< (plist-get a :time) (plist-get b :time)))))

    ;; 4. Render
    (if (null items)
        (setq sb (concat sb "No conversation found.\n"))
      (let ((first t))
        (dolist (item items)
          (unless first
            (setq sb (concat sb "--------------------------------------------------------------------------------\n")))
          (setq first nil)

          (let ((author (plist-get item :author))
                (time (plist-get item :time))
                (type (plist-get item :type))
                (body (or (plist-get item :body) "(No body)")))

            (if (eq type 'review)
                (setq sb (concat sb (format "From: %s at %s [%s]\n" author time (plist-get item :state))))
              (setq sb (concat sb (format "From: %s at %s\n" author time))))

            (setq sb (concat sb (crs--make-html-placeholder body) "\n\n"))))))

    ;; Add Files Changed header (placeholder or parsed?)
    ;; For now just a blank line, maybe we can add a separator
    (setq sb (concat sb "\n"))
    sb))

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
                  (push (cons line-end (list 'append (crs--format-compact-comment-indicator line-comments))) insertions)))))
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
              (when (not first-hunk-seen)
                (setq first-hunk-seen t)
                (setq current-position 0)))

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
          (existing-review-feedback crs--buffer-review-feedback))

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
          (setq new-preamble preamble)))

      ;; Temporarily set for rendering (before mode change wipes them)
      (setq crs--buffer-diff (or new-diff existing-diff))
      (setq crs--buffer-comments (or new-comments existing-comments))
      (setq crs--buffer-outdated-comments (or new-outdated-comments existing-outdated-comments))
      (setq crs--buffer-metadata (or new-metadata existing-metadata))
      (setq crs--buffer-reviews (or new-reviews existing-reviews))
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
                              crs--buffer-outdated-comments))
               (preamble (concat header "\n" conversation))
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

;;; Entry points

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
     (message "DEBUG GetPR result: %S" result)
     (let* ((buffer (get-buffer-create (format "* Review %s/%s #%d *" owner repo number)))
            (project-path (expand-file-name (concat "~/" repo)))
            (error-info (cdr (assq 'error result))))
       (with-current-buffer buffer
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
  "Visit the file at point in the code review buffer."
  (interactive)
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
      (message "No file found at point"))))

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
           (let ((review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number))))
             (when review-buffer
               (crs--render-and-update review-buffer result))
             (message "Review synced successfully!"))))))))

(provide 'crs-client)

;;; crs-client.el ends here
