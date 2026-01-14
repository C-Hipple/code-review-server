;;; crs-client.el --- JSON-RPC client for codereviewserver -*- lexical-binding: t; -*-

;; Author: Chris Hipple
;; Version: 0.1.0
;; Package-Requires: ((emacs "25.1"))

;; SPDX-License-Identifier: GPL-3.0+

;;; Commentary:

;; This package provides JSON-RPC client functionality for crs (codereviewserver).
;; It allows starting the server process and making RPC calls to it.

;;; Code:

(require 'json)
(require 'washer)
(require 'markdown-mode)
(require 'seq)
(require 'subr-x)

(defvar crs--process nil
  "The process handle for the crs JSON-RPC server.")

(defvar crs--request-id 1
  "Counter for JSON-RPC request IDs.")

(defvar crs--pending-requests (make-hash-table :test 'equal)
  "Hash table mapping request IDs to callback functions.")

(defvar crs--response-buffer ""
  "Buffer for accumulating partial JSON-RPC responses.")

(defvar crs-plugins nil
  "List of plugins configured on the server.")

(defvar crs--section-header-regexp
  "^\\(?:[^[:space:]].*?[[:space:]]\\)?\\(?:\\(?:\\.\\.\\.\\)?\\(?:modified\\|deleted\\|new file\\)[[:space:]:]+.*\\|Commits .*\\|Description\\|Conversation\\|Your Review Feedback\\|Files changed .*\\)$"
  "Regexp to match section headers in the code review buffer.")

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
              (message "JSON-RPC Error: %s" (if (stringp error) error (cdr (assq 'message error))))
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

(defun crs-list-plugins ()
  "Call the ListPlugins RPC method and store the result in `crs-plugins`."
  (interactive)
  (crs--send-request
   "RPCHandler.ListPlugins"
   (vector)
   (lambda (result)
     (setq crs-plugins (cdr (assq 'plugins result)))
     (message "Plugins updated: %d plugins found" (length crs-plugins)))))

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
         (goto-char (point-min))
         (org-mode))
       (display-buffer buffer)
       (message "Reviews loaded into '* Reviews *' buffer")))))

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
  "H" #'crs-toggle-comments
  "f" #'crs-set-review-feedback
  "RET" #'crs-visit-file
  "<return>" #'crs-visit-file
  "q" #'quit-window
  )

(define-derived-mode my-code-review-mode fundamental-mode "Code Review"
  "Major mode for viewing code reviews."
  (highlight-at-at-lines-blue)
  (highlight-review-comments)
  (add-to-invisibility-spec '(codereview-hide . t))
  (add-hook 'post-command-hook #'crs--maybe-show-collapsed-comments nil t))

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
    "H" #'crs-toggle-comments
    "f" #'crs-set-review-feedback
    "RET" #'crs-visit-file
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
    "H" #'crs-toggle-comments
    "f" #'crs-set-review-feedback
    "RET" #'crs-visit-file
    "q" #'quit-window)
  ;; Define keys for insert state
  (evil-define-key 'insert my-code-review-mode-map
    "C-c C-c" #'crs-submit-review))

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
      (dolist (line (split-string (or (cdr (assq 'body root)) "") "\n"))
        (push (format "    │   %s" line) lines))

      ;; Render replies
      (dolist (reply replies)
        (push "    │" lines)
        (let ((r-author (cdr (assq 'author reply)))
              (r-id (cdr (assq 'id reply))))
          (push (format "    │ Reply by [%s]:[%s]" (or r-author "local") (or r-id "")) lines)
          (dolist (line (split-string (or (cdr (assq 'body reply)) "") "\n"))
            (push (format "    │   %s" line) lines))))

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
           ((string-match "^\\(modified\\|deleted\\|new file\\)[[:space:]]+\\(.*\\)$" line)
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
    (while (re-search-forward "^diff --git a/\\(.*\\) b/.*$" nil t)
      (let* ((filename (match-string 1))
             (start (match-beginning 0))
             (type "modified")
             (limit (save-excursion
                      (if (re-search-forward "^diff --git" nil t)
                          (match-beginning 0)
                        (point-max)))))

        (save-excursion
          (when (re-search-forward "^new file mode" limit t)
            (setq type "new file"))
          (goto-char start)
          (when (re-search-forward "^deleted file mode" limit t)
            (setq type "deleted")))

        (let ((end-marker-pos
               (save-excursion
                 (or (re-search-forward "^\\+\\+\\+ .*\n" limit t)
                     (re-search-forward "^Binary files .*\n" limit t)))))

          (if end-marker-pos
              (progn
                (delete-region start end-marker-pos)
                (let ((pad (cond ((string= type "modified") "     ")
                                 ((string= type "new file") "     ")
                                 ((string= type "deleted")  "      ")
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
           ((string-match "^\\(modified\\|deleted\\|new file\\)[[:space:]]+\\(.*\\)$" line)
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
          (sb ""))
      
      (setq sb (concat sb (format "#%s: %s\n" number title)))
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
        
      sb)))

(defun crs--render-conversation-from-data (comments reviews)
  "Render the conversation section from COMMENTS and REVIEWS."
  (let ((items nil)
        (sb "\nConversation\n"))
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
              
            (setq sb (concat sb body "\n\n"))))))
            
    ;; Add Files Changed header (placeholder or parsed?)
    ;; For now just a blank line, maybe we can add a separator
    (setq sb (concat sb "\n"))
    sb))

(defun crs--render-and-update (buffer content &optional target-line)
  "Render CONTENT (which can be a string, JSON-RPC result alist, or nil) into BUFFER.
If CONTENT is nil, re-renders from stored data (useful for toggle operations)."
  (with-current-buffer buffer
    (let ((inhibit-read-only t)
          (old-pos (point))
          ;; Extract data before mode change (which kills local vars)
          (new-diff nil)
          (new-comments nil)
          (new-metadata nil)
          (new-reviews nil)
          (new-preamble nil)
          (new-show-comments (if (local-variable-p 'crs--buffer-show-comments)
                                 crs--buffer-show-comments
                               t))
          ;; Preserve existing data for re-render case
          (existing-diff crs--buffer-diff)
          (existing-comments crs--buffer-comments)
          (existing-metadata crs--buffer-metadata)
          (existing-reviews crs--buffer-reviews)
          (existing-preamble crs--buffer-preamble)
          (existing-review-feedback crs--buffer-review-feedback))

      ;; If content is a JSON result (alist), extract the components
      (when (and content (listp content) (not (stringp content)))
        (let* ((diff (cdr (assq 'diff content)))
               (comments (cdr (assq 'comments content)))
               (metadata (cdr (assq 'metadata content)))
               (reviews (cdr (assq 'reviews content)))
               (raw-content (cdr (assq 'content content)))
               (preamble (if raw-content
                             (if (string-match "Files changed (.*)\n\n" raw-content)
                                 (substring raw-content 0 (match-end 0))
                               raw-content)
                           "")))
          (setq new-diff diff)
          (setq new-comments comments)
          (setq new-metadata metadata)
          (setq new-reviews reviews)
          (setq new-preamble preamble)))

      ;; Temporarily set for rendering (before mode change wipes them)
      (setq crs--buffer-diff (or new-diff existing-diff))
      (setq crs--buffer-comments (or new-comments existing-comments))
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

        ;; 4. Insert Comments
        (crs--insert-comments-into-buffer crs--buffer-comments crs--buffer-show-comments)

        ;; 5. Insert Preamble & Feedback at TOP
        (let* ((header (crs--render-header-from-metadata crs--buffer-metadata))
               (conversation (crs--render-conversation-from-data 
                              crs--buffer-comments
                              crs--buffer-reviews))
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
      (my-code-review-mode)

      ;; Re-set the buffer-local variables AFTER mode change
      (setq crs--buffer-diff (or new-diff existing-diff))
      (setq crs--buffer-comments (or new-comments existing-comments))
      (setq crs--buffer-metadata (or new-metadata existing-metadata))
      (setq crs--buffer-reviews (or new-reviews existing-reviews))
      (setq crs--buffer-preamble (or new-preamble existing-preamble))
      (setq crs--buffer-show-comments new-show-comments)
      (setq crs--buffer-review-feedback existing-review-feedback)

      (let ((final-pos (if target-line
                           (progn
                             (goto-char (point-min))
                             (forward-line (1- target-line))
                             (if (> (point) (point-max))
                                 (point-max)
                               (point)))
                         (min old-pos (point-max)))))
        (goto-char final-pos)
        ;; Also update point in any windows showing this buffer
        (dolist (win (get-buffer-window-list buffer nil t))
          (set-window-point win final-pos)))
      (setq buffer-read-only t))))

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
            (project-path (expand-file-name (concat "~/" repo))))
       (with-current-buffer buffer
         (if (file-directory-p project-path)
             (cd project-path)
           (message "Directory not found: %s" project-path)))
       ;; Pass the whole result to crs--render-and-update
       (crs--render-and-update buffer result)
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

(defun crs--get-current-review-info ()
  "Extract (owner repo number) from the current buffer name.
Returns a list (owner repo number) or signals an error if not in a review buffer."
  (let ((name (buffer-name)))
    (if (string-match "\\* Review \\([^/]+\\)/\\([^[:space:]]+\\) #\\([0-9]+\\) .*\\*" name)
        (list (match-string 1 name)
              (match-string 2 name)
              (string-to-number (match-string 3 name)))
      (error "Not in a valid review buffer: %s" name))))

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

;; Buffer-local variables for storing PR data separately
(defvar-local crs--buffer-diff nil
  "The raw diff content for the current PR.")
(defvar-local crs--buffer-comments nil
  "The comments list for the current PR.")
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
             (let ((review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number))))
               (when review-buffer
                 (crs--render-and-update review-buffer result original-line))
               (message "Comment updated successfully")
               (kill-buffer-and-window))))
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
           (let ((review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number))))
             (when review-buffer
               (crs--render-and-update review-buffer result original-line))
             (message "Comment added successfully")
             (kill-buffer-and-window))))))))

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
    "C-c C-k" #'crs-abort-comment
    ", c" #'crs-submit-comment
    ", k" #'crs-abort-comment)
  (evil-define-key 'visual comment-edit-mode-map
    "C-c C-c" #'crs-submit-comment
    "C-c C-k" #'crs-abort-comment
    ", c" #'crs-submit-comment
    ", k" #'crs-abort-comment))

;; Buffer-local variables for plugin output mode
(defvar-local crs--plugin-owner nil
  "Owner of the PR for plugin output.")
(defvar-local crs--plugin-repo nil
  "Repo of the PR for plugin output.")
(defvar-local crs--plugin-number nil
  "PR number for plugin output.")

(defun crs-refresh-plugin-output ()
  "Refresh the plugin output in the current buffer."
  (interactive)
  (unless (and crs--plugin-owner crs--plugin-repo crs--plugin-number)
    (error "Not in a plugin output buffer or missing PR context"))
  (let ((owner crs--plugin-owner)
        (repo crs--plugin-repo)
        (number crs--plugin-number))
    (message "Refreshing plugin output for %s/%s #%d..." owner repo number)
    (crs--send-request
     "RPCHandler.GetPluginOutput"
     (vector (list (cons 'Owner owner)
                   (cons 'Repo repo)
                   (cons 'Number number)))
     (lambda (result)
       (let ((output (cdr (assq 'output result)))
             (buffer (current-buffer)))
         (with-current-buffer buffer
           (let ((inhibit-read-only t))
             (erase-buffer)
             (if (null output)
                 (insert "No plugin output available.\n")
               (dolist (plugin-entry (append output nil))
                 (let* ((name (symbol-name (car plugin-entry)))
                        (data (cdr plugin-entry))
                        (res (cdr (assq 'result data)))
                        (status (cdr (assq 'status data))))
                   (insert (format "# Plugin: %s (Status: %s)\n" name status))
                   (insert "──────────────────────────────────\n")
                   (insert (or res "No output."))
                   (insert "\n\n"))))
             (goto-char (point-min))))
         (message "Plugin output refreshed."))))))

(defun crs-quit-plugin-output ()
  "Quit the plugin output window and kill the buffer."
  (interactive)
  (quit-window t))

(defvar-keymap crs-plugin-output-mode-map
  "r" #'crs-refresh-plugin-output
  "q" #'crs-quit-plugin-output)

(define-derived-mode crs-plugin-output-mode markdown-mode "Plugin Output"
  "Major mode for viewing plugin output.
  Inherits from markdown-mode and is read-only.
  \\{crs-plugin-output-mode-map}"
  (setq buffer-read-only t))

(when (fboundp 'evil-define-key)
  (evil-define-key 'normal crs-plugin-output-mode-map
    "r" #'crs-refresh-plugin-output
    "q" #'crs-quit-plugin-output))

(defun crs--find-first-hunk-line ()
  "Find the line number of the first hunk header after point, bounded by the next file header."
  (save-excursion
    (let* ((start-pos (point))
           (search-bound (save-excursion
                           (forward-line 1)
                           (if (re-search-forward "^\\(?:[^[:space:]].*?[[:space:]]\\)?\\(?:modified\\|deleted\\|new file\\)[[:space:]:]+" nil t)
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
      (if (re-search-backward "^\\(?:[^[:space:]].*?[[:space:]]\\)?\\(modified\\|deleted\\|new file\\)[[:space:]:]+\\([^[:space:]\n].*?\\)[[:space:]]*$" nil t)
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
        (original-line (line-number-at-pos)))
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
             (let ((review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number))))
               (when review-buffer
                 (crs--render-and-update review-buffer result))
               (message "Local comment deleted")))))))))

(defun crs-submit-review (event)
  "Submit a review with EVENT.
The body is taken from `crs--buffer-review-feedback`.
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
       (let ((review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number))))
         (when review-buffer
           (with-current-buffer review-buffer
             (setq crs--buffer-review-feedback nil)
             (crs--render-and-update review-buffer result)))
         (message "Review submitted successfully!"))))))

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
       (let ((review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number))))
         (when review-buffer
           (crs--render-and-update review-buffer result))
         (message "Review synced successfully!"))))))

(defun crs-get-plugin-output ()
  "Fetch and display plugin output for the current PR."
  (interactive)
  (let* ((info (crs--get-current-review-info))
         (owner (nth 0 info))
         (repo (nth 1 info))
         (number (nth 2 info)))
    (message "Fetching plugin output for %s/%s #%d..." owner repo number)
    (crs--send-request
     "RPCHandler.GetPluginOutput"
     (vector (list (cons 'Owner owner)
                   (cons 'Repo repo)
                   (cons 'Number number)))
     (lambda (result)
       (let ((output (cdr (assq 'output result)))
             (buffer (get-buffer-create (format "* Plugin Output %s/%s #%d *" owner repo number))))
         (with-current-buffer buffer
           (let ((inhibit-read-only t))
             (erase-buffer)
             (if (null output)
                 (insert "No plugin output available.\n")
               (dolist (plugin-entry (append output nil)) ;; Ensure it's treated as a list of pairs
                 (let* ((name (symbol-name (car plugin-entry)))
                        (data (cdr plugin-entry))
                        (res (cdr (assq 'result data)))
                        (status (cdr (assq 'status data))))
                   (insert (format "# Plugin: %s (Status: %s)\n" name status))
                   (insert "──────────────────────────────────\n")
                   (insert (or res "No output."))
                   (insert "\n\n"))))
             (goto-char (point-min))
             (crs-plugin-output-mode)
             ;; Store PR context for refresh
             (setq crs--plugin-owner owner)
             (setq crs--plugin-repo repo)
             (setq crs--plugin-number number)))
         (pop-to-buffer buffer)
         (message "Plugin output loaded."))))))

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
  "Extract the branch name from the current crs buffer.
TODO: This doesn't match if the root branch has a special char in it."
  (interactive)
  (save-excursion
    (goto-char (point-min))
    (let ((found-ref ""))
      (when (re-search-forward "Refs:\\s+\\([^[:space:]]+\\)\\s+\\.\\.\\.\\s+\\(.+\\)$" nil t)
        (setq found-ref (match-string 2)))
      found-ref)))

(defun crs-checkout-current-project ()
  (interactive)
  (crs--switch-and-fetch (projectile-project-name) (crs--get-ref-name)))
(provide 'crs-client)

;;; crs-client.el ends here
