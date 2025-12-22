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

(defvar crs--process nil
  "The process handle for the crs JSON-RPC server.")

(defvar crs--request-id 1
  "Counter for JSON-RPC request IDs.")

(defvar crs--pending-requests (make-hash-table :test 'equal)
  "Hash table mapping request IDs to callback functions.")

(defvar crs--response-buffer ""
  "Buffer for accumulating partial JSON-RPC responses.")

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
  (condition-case err
      (let ((response (json-read-from-string response-line)))
        (let ((id (cdr (assq 'id response)))
              (result (cdr (assq 'result response)))
              (error (cdr (assq 'error response))))
          (if error
              (message "JSON-RPC Error: %s" (cdr (assq 'message error)))
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
     (let ((content (cdr (assq 'Content result)))
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
  "q" #'quit-window
  )

(define-derived-mode my-code-review-mode fundamental-mode "Code Review"
  "Major mode for viewing code reviews."
  (highlight-at-at-lines-blue)
  (highlight-review-comments)
  (add-to-invisibility-spec '(codereview-hide . t)))

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
    "q" #'quit-window)
  ;; Define keys for insert state
  (evil-define-key 'insert my-code-review-mode-map
    "C-c C-c" #'crs-submit-review))

(defun crs--render-and-update (buffer content &optional target-line)
  "Render CONTENT into BUFFER and update it, preserving point or moving to TARGET-LINE.
If TARGET-LINE is provided, move to that line number after rendering.
Otherwise, try to preserve the old point position."
  (with-current-buffer buffer
    (let ((inhibit-read-only t)
          (old-pos (point)))
      (erase-buffer)
      (insert (or content ""))
      (delta-wash)
      (my-code-review-mode)
      (if target-line
          ;; Move to the specified line number
          (progn
            (goto-char (point-min))
            (forward-line (1- target-line))
            (when (> (point) (point-max))
              (goto-char (point-max))))
        ;; Fall back to preserving old position
        (goto-char (min old-pos (point-max))))
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
     (let ((content (cdr (assq 'Content result)))
           (buffer (get-buffer-create (format "* Review %s/%s #%d *" owner repo number))))
       (crs--render-and-update buffer content)
       (pop-to-buffer buffer)
       (message "Review loaded into buffer")))))

(defun crs-start-review-at-point ()
  "Parse a GitHub PR URL from the current line and call crs-get-review.
The line should contain a URL in the format https://github.com/OWNER/REPO/pull/NUMBER"
  (interactive)
  (let ((line (buffer-substring-no-properties (line-beginning-position) (line-end-position))))
    (if (string-match "github\\.com/\\([^/]+\\)/\\([^/]+\\)/pull/\\([0-9]+\\)" line)
        (let ((owner (match-string 1 line))
              (repo (match-string 2 line))
              (number (string-to-number (match-string 3 line))))
          (crs-get-review owner repo number))
      (error "Could not find GitHub PR URL on current line"))))


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
             (let ((content (cdr (assq 'Content result)))
                   (review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number)))
                   (original-line (with-current-buffer (current-buffer) crs--comment-original-line)))
               (when review-buffer
                 (crs--render-and-update review-buffer content original-line))
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
           (let ((content (cdr (assq 'Content result)))
                 (review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number)))
                 (original-line (with-current-buffer (current-buffer) crs--comment-original-line)))
             (when review-buffer
               (crs--render-and-update review-buffer content original-line))
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
    (let ((name (buffer-name)))
      (if (string-match "\\* Review \\([^/]+\\)/\\([^[:space:]]+\\) #\\([0-9]+\\) .*\\*" name)
          (progn
            (setq owner (match-string 1 name))
            (setq repo (match-string 2 name))
            (setq number (string-to-number (match-string 3 name))))
        (message "Could not parse review context from buffer name: %S" name)))

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
        (let ((count 0))
          (forward-line 1)
          (while (<= (line-number-at-pos) target-line)
            (let ((line-content (buffer-substring-no-properties (line-beginning-position) (line-end-position))))
              ;; Skip lines that are part of a comment block (allowing for ANSI codes)
              (unless (string-match-p "^[[:cntrl:][:space:]]*[│┌└]" line-content)
                (setq count (1+ count))))
            (forward-line 1))
          (setq position count))))

    (let ((ctx (list owner repo number filename position reply-to-id local-comment-id local-comment-body)))
      (message "Context extracted: %S" ctx)
      ctx)))

(defun crs-add-or-edit-comment (owner repo number filename position &optional reply-to-id local-comment-id local-comment-body)
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
    (let ((name (buffer-name)))
      (when (string-match "\\* Review \\([^/]+\\)/\\([^[:space:]]+\\) #\\([0-9]+\\) .*\\*" name)
        (setq owner (match-string 1 name))
        (setq repo (match-string 2 name))
        (setq number (string-to-number (match-string 3 name)))))
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
             (let ((content (cdr (assq 'Content result)))
                   (review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number))))
               (when review-buffer
                 (crs--render-and-update review-buffer content))
               (message "Local comment deleted")))))))))

(defun crs-submit-review (event body)
  "Submit a review with EVENT and optional BODY.
EVENT must be one of 'APPROVE', 'REQUEST_CHANGES', or 'COMMENT'."
  (interactive
   (list (completing-read "Event: " '("APPROVE" "REQUEST_CHANGES" "COMMENT") nil t)
         (read-string "Body: ")))
  (let ((owner nil)
        (repo nil)
        (number nil))
    (let ((name (buffer-name)))
      (if (string-match "\\* Review \\([^/]+\\)/\\([^ ]+\\) #\\([0-9]+\\) \\*" name)
          (progn
            (setq owner (match-string 1 name))
            (setq repo (match-string 2 name))
            (setq number (string-to-number (match-string 3 name))))
        (error "Not in a valid review buffer")))

    (crs--send-request
     "RPCHandler.SubmitReview"
     (vector (list (cons 'Owner owner)
                   (cons 'Repo repo)
                   (cons 'Number number)
                   (cons 'Event event)
                   (cons 'Body body)))
     (lambda (result)
       (let ((content (cdr (assq 'Content result)))
             (review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number))))
         (when review-buffer
           (crs--render-and-update review-buffer content))
         (message "Review submitted successfully!"))))))

(defun crs-approve-review (body)
  "Approve the review with optional BODY."
  (interactive "sApprove Body: ")
  (crs-submit-review "APPROVE" body))

(defun crs-comment-review (body)
  "Comment on the review with optional BODY."
  (interactive "sComment Body: ")
  (crs-submit-review "COMMENT" body))

(defun crs-request-changes-review (body)
  "Request changes on the review with optional BODY."
  (interactive "sRequest Changes Body: ")
  (crs-submit-review "REQUEST_CHANGES" body))

(defun crs-sync-pr ()
  "Sync the PR in the current buffer with the server."
  (interactive)
  (let ((owner nil)
        (repo nil)
        (number nil))
    (let ((name (buffer-name)))
      (if (string-match "\\* Review \\([^/]+\\)/\\([^ ]+\\) #\\([0-9]+\\) \\*" name)
          (progn
            (setq owner (match-string 1 name))
            (setq repo (match-string 2 name))
            (setq number (string-to-number (match-string 3 name))))
        (error "Not in a valid review buffer")))

    (message "Syncing PR %s/%s #%d..." owner repo number)
    (crs--send-request
     "RPCHandler.SyncPR"
     (vector (list (cons 'Owner owner)
                   (cons 'Repo repo)
                   (cons 'Number number)))
     (lambda (result)
       (let ((content (cdr (assq 'Content result)))
             (review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number))))
         (when review-buffer
           (crs--render-and-update review-buffer content))
         (message "Review synced successfully!"))))))

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
