;;; codereviewserver-client.el --- JSON-RPC client for codereviewserver -*- lexical-binding: t; -*-

;; Author: Chris Hipple
;; Version: 0.1.0
;; Package-Requires: ((emacs "25.1"))

;; SPDX-License-Identifier: GPL-3.0+

;;; Commentary:

;; This package provides JSON-RPC client functionality for codereviewserver.
;; It allows starting the server process and making RPC calls to it.

;;; Code:

(require 'json)
(require 'washer)
(require 'markdown-mode)

(defvar codereviewserver-client--process nil
  "The process handle for the codereviewserver JSON-RPC server.")

(defvar codereviewserver-client--request-id 1
  "Counter for JSON-RPC request IDs.")

(defvar codereviewserver-client--pending-requests (make-hash-table :test 'equal)
  "Hash table mapping request IDs to callback functions.")

(defvar codereviewserver-client--response-buffer ""
  "Buffer for accumulating partial JSON-RPC responses.")

;;;###autoload
(defun codereviewserver-client-start-server ()
  "Start the codereviewserver JSON-RPC server process.
Returns the process handle."
  (interactive)
  (if (and codereviewserver-client--process
           (process-live-p codereviewserver-client--process))
      (progn
        (message "Server is already running")
        codereviewserver-client--process)
    (progn
      (let ((stderr-buffer (get-buffer-create "*codereviewserver-client-stderr*")))
        (setq codereviewserver-client--process
              (make-process :name "codereviewserver-client"
                            :buffer "*codereviewserver-client*"
                            :command '("codereviewserver" "-server")
                            :connection-type 'pipe
                            :coding 'utf-8
                            :stderr stderr-buffer
                            :noquery t))

        (set-process-filter codereviewserver-client--process 'codereviewserver-client--process-filter)
        (set-process-sentinel codereviewserver-client--process 'codereviewserver-client--process-sentinel)

        ;; Give the process a moment to start up
        (sleep-for 0.2)

        ;; Check if process is still alive after startup
        (unless (process-live-p codereviewserver-client--process)
          (let ((stderr-content (with-current-buffer stderr-buffer (buffer-string))))
            (error "Server process died immediately. Stderr: %s" stderr-content)))))

    (message "Started codereviewserver JSON-RPC server")
    codereviewserver-client--process))

;;;###autoload
(defun codereviewserver-client-shutdown-server ()
  "Stop the codereviewserver JSON-RPC server process if it is running."
  (interactive)
  (if (and codereviewserver-client--process
           (process-live-p codereviewserver-client--process))
      (progn
        (delete-process codereviewserver-client--process)
        (setq codereviewserver-client--process nil)
        (message "Stopped codereviewserver JSON-RPC server"))
    (message "Server is not running")))

;;;###autoload
(defun codereviewserver-client-restart-server ()
  "Restart the codereviewserver JSON-RPC server process.
Stops the existing server if running, then starts a new one.
Useful after recompiling the Go server binary."
  (interactive)
  (codereviewserver-client-shutdown-server)
  (sleep-for 0.2)  ; Give the process a moment to fully shut down
  (codereviewserver-client-start-server))

(defun codereviewserver-client--process-filter (process output)
  "Filter function for processing JSON-RPC responses from the server."
  (setq codereviewserver-client--response-buffer
        (concat codereviewserver-client--response-buffer output))

  ;; Process complete lines (JSON-RPC uses newline-delimited JSON)
  (while (string-match "\n" codereviewserver-client--response-buffer)
    (let ((line (substring codereviewserver-client--response-buffer 0 (match-beginning 0))))
      (setq codereviewserver-client--response-buffer
            (substring codereviewserver-client--response-buffer (match-end 0)))

      (when (> (length line) 0)
        (codereviewserver-client--handle-response line)))))

(defun codereviewserver-client--handle-response (response-line)
  "Handle a single JSON-RPC response line."
  (condition-case err
      (let ((response (json-read-from-string response-line)))
        (let ((id (cdr (assq 'id response)))
              (result (cdr (assq 'result response)))
              (error (cdr (assq 'error response))))
          (if error
              (message "JSON-RPC Error: %s" (cdr (assq 'message error)))
            (let ((callback (gethash id codereviewserver-client--pending-requests)))
              (when callback
                (remhash id codereviewserver-client--pending-requests)
                (funcall callback result))))))
    (error
     (message "Error parsing JSON-RPC response: %s" err))))

(defun codereviewserver-client--process-sentinel (process event)
  "Sentinel function for the codereviewserver process."
  (when (memq (process-status process) '(exit signal))
    (let ((buffer (process-buffer process))
          (stderr-buffer (get-buffer "*codereviewserver-client-stderr*")))
      (when buffer
        (with-current-buffer buffer
          (let ((output (buffer-string)))
            (when (> (length output) 0)
              (message "codereviewserver stdout: %s" output)))))
      (when stderr-buffer
        (with-current-buffer stderr-buffer
          (let ((stderr-output (buffer-string)))
            (when (> (length stderr-output) 0)
              (message "codereviewserver stderr: %s" stderr-output))))))
    (setq codereviewserver-client--process nil)
    (message "codereviewserver process %s" event)))

(defun codereviewserver-client--send-request (method params callback)
  "Send a JSON-RPC request to the server.
METHOD is the method name (e.g., 'RPCHandler.GetAllReviews').
PARAMS is the parameters array.
CALLBACK is a function to call with the result."
  (unless (and codereviewserver-client--process
               (process-live-p codereviewserver-client--process))
    (error "Server is not running. Call codereviewserver-client-start-server first"))

  (let ((id codereviewserver-client--request-id))
    (setq codereviewserver-client--request-id (1+ codereviewserver-client--request-id))

    (puthash id callback codereviewserver-client--pending-requests)

    (let ((request (json-encode `((jsonrpc . "2.0")
                                  (method . ,method)
                                  (params . ,params)
                                  (id . ,id)))))
      (process-send-string codereviewserver-client--process
                           (concat request "\n")))))

;;;###autoload
(defun codereviewserver-client-get-reviews ()
  "Call the GetAllReviews RPC method and display the result in '* Reviews *' buffer."
  (interactive)
  (unless (and codereviewserver-client--process
               (process-live-p codereviewserver-client--process))
    (codereviewserver-client-start-server)
    ;; Give the server a moment to start
    (sleep-for 0.5))

  (codereviewserver-client--send-request
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

(defun codereviewserver-client-toggle-section ()
  "Toggle visibility of the section under the current file header."
  (interactive)
  (let ((line-content (buffer-substring-no-properties (line-beginning-position) (line-end-position))))
    (if (string-match "^\\(?:\\.\\.\\.\\)?\\(modified\\|deleted\\|new file\\) +.*$" line-content)
        (let* ((header-text-end (line-end-position))
               (next-line-start (save-excursion (forward-line 1) (point)))
               (content-end (save-excursion
                              (forward-line 1)
                              (if (re-search-forward "^\\(?:\\.\\.\\.\\)?\\(modified\\|deleted\\|new file\\) +.*$" nil t)
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

(defun codereviewserver-client-collapse-all-sections ()
  "Collapse all file sections in the current buffer."
  (interactive)
  (save-excursion
    (goto-char (point-min))
    (while (re-search-forward "^\\(?:\\.\\.\\.\\)?\\(modified\\|deleted\\|new file\\) +.*$" nil t)
      (let* ((header-text-end (line-end-position))
             (next-line-start (save-excursion (forward-line 1) (point)))
             (content-end (save-excursion
                            (forward-line 1)
                            (if (re-search-forward "^\\(?:\\.\\.\\.\\)?\\(modified\\|deleted\\|new file\\) +.*$" nil t)
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

(defun codereviewserver-client-expand-all-sections ()
  "Expand all file sections in the current buffer."
  (interactive)
  (save-excursion
    (dolist (ov (overlays-in (point-min) (point-max)))
      (when (overlay-get ov 'codereview-hide)
        (delete-overlay ov)))))

(defvar my-code-review-mode-map
  (let ((map (make-sparse-keymap)))
    (define-key map (kbd "TAB") 'codereviewserver-client-toggle-section)
    (define-key map (kbd "<tab>") 'codereviewserver-client-toggle-section)
    (define-key map (kbd "<backtab>") 'codereviewserver-client-collapse-all-sections)
    (define-key map (kbd "c") 'codereviewserver-client-add-comment)
    (define-key map (kbd "C-c C-c") 'codereviewserver-client-submit-review)
    (define-key map (kbd "g") 'codereviewserver-client-sync-pr)
    (define-key map (kbd "q") 'quit-window)
    map)
  "Keymap for `my-code-review-mode`.")

(define-derived-mode my-code-review-mode fundamental-mode "Code Review"
  "Major mode for viewing code reviews."
  (highlight-at-at-lines-blue)
  (highlight-review-comments)
  (add-to-invisibility-spec '(codereview-hide . t)))

(defun codereviewserver-client-get-review (owner repo number)
  "Call the GetPR RPC method for OWNER/REPO PR NUMBER and display the result."
  (interactive "sOwner: \nsRepo: \nnPR Number: ")
  (unless (and codereviewserver-client--process
               (process-live-p codereviewserver-client--process))
    (codereviewserver-client-start-server)
    (sleep-for 0.5))

  (codereviewserver-client--send-request
   "RPCHandler.GetPR"
   (vector (list (cons 'Owner owner)
                 (cons 'Repo repo)
                 (cons 'Number number)))
   (lambda (result)
     (let ((content (cdr (assq 'Content result)))
           (buffer (get-buffer-create (format "* Review %s/%s #%d *" owner repo number))))
       (with-current-buffer buffer
         (let ((inhibit-read-only t))
           (erase-buffer)
           (insert (or content ""))
           (goto-char (point-min))
           (delta-wash)
           (my-code-review-mode)
           (setq buffer-read-only t)))
       (display-buffer buffer)
       (message "Review loaded into buffer")))))

(defun codereviewserver-client-get-review-from-line ()
  "Parse a GitHub PR URL from the current line and call codereviewserver-client-get-review.
The line should contain a URL in the format https://github.com/OWNER/REPO/pull/NUMBER"
  (interactive)
  (let ((line (buffer-substring-no-properties (line-beginning-position) (line-end-position))))
    (if (string-match "github\\.com/\\([^/]+\\)/\\([^/]+\\)/pull/\\([0-9]+\\)" line)
        (let ((owner (match-string 1 line))
              (repo (match-string 2 line))
              (number (string-to-number (match-string 3 line))))
          (codereviewserver-client-get-review owner repo number))
      (error "Could not find GitHub PR URL on current line"))))


(defvar-local codereviewserver-client--comment-owner nil)
(defvar-local codereviewserver-client--comment-repo nil)
(defvar-local codereviewserver-client--comment-number nil)
(defvar-local codereviewserver-client--comment-filename nil)
(defvar-local codereviewserver-client--comment-position nil)
(defvar-local codereviewserver-client--comment-reply-to-id nil)

(defun codereviewserver-client-submit-comment ()
  "Submit the comment in the current buffer."
  (interactive)
  (let ((body (buffer-string))
        (owner codereviewserver-client--comment-owner)
        (repo codereviewserver-client--comment-repo)
        (number codereviewserver-client--comment-number)
        (filename codereviewserver-client--comment-filename)
        (reply-to-id codereviewserver-client--comment-reply-to-id)
        (position (if codereviewserver-client--comment-reply-to-id
                      nil
                    codereviewserver-client--comment-position)))
    (codereviewserver-client--send-request
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
             (review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number))))
         (when review-buffer
           (with-current-buffer review-buffer
             (let ((inhibit-read-only t))
               (erase-buffer)
               (insert (or content ""))
               (goto-char (point-min))
               (delta-wash)
               (my-code-review-mode)
               (setq buffer-read-only t))))
         (message "Comment added successfully")
         (kill-buffer-and-window))))))

(define-derived-mode comment-edit-mode markdown-mode "Code Review Comment"
  "Major mode for editing code review comments."
  (local-set-key (kbd "C-c C-c") 'codereviewserver-client-submit-comment))

(defun codereviewserver-client--find-first-hunk-line ()
  "Find the line number of the first hunk header after point, bounded by the next file header."
  (save-excursion
    (let ((search-bound (save-excursion
                          (forward-line 1)
                          (if (re-search-forward "^diff --git" nil t)
                              (match-beginning 0)
                            (point-max)))))
      (if (re-search-forward "^@@ .* @@" search-bound t)
          (line-number-at-pos)
        nil))))

(defun codereviewserver-client--get-comment-context ()
  "Extract owner, repo, number, filename, position and reply-to-id from current review buffer."
  (let ((owner nil)
        (repo nil)
        (number nil)
        (filename nil)
        (position nil)
        (reply-to-id nil)
        (target-line (line-number-at-pos))
        (first-hunk-line-num nil))

    ;; 1. Check for Reply ID (must be done before moving point)
    (save-excursion
      (end-of-line) ;; Ensure we catch the header if we are on it
      (let ((line-content (buffer-substring-no-properties (line-beginning-position) (line-end-position))))
        (when (string-match-p "^    [│┌└]" line-content)
          (save-excursion
            (if (re-search-backward "^    ┌─ REVIEW COMMENT" nil t)
                (progn
                  (forward-line 2) ;; ID is on the 3rd line of the block
                  (let ((id-line (buffer-substring-no-properties (point) (line-end-position))))
                    (if (string-match " : \\([0-9]+\\)$" id-line)
                        (progn
                          (setq reply-to-id (string-to-number (match-string 1 id-line)))
                          (message "Found Reply-To ID: %d" reply-to-id))
                      (message "Could not find ID in comment block line: '%s'" id-line))))
              (message "Could not find start of comment block"))))))

    ;; 2. Parse Owner, Repo, Number
    (let ((name (buffer-name)))
      (when (string-match "\\* Review \\([^/]+\\)/\\([^ ]+\\) #\\([0-9]+\\) \\*" name)
        (setq owner (match-string 1 name))
        (setq repo (match-string 2 name))
        (setq number (string-to-number (match-string 3 name)))))

    ;; 3. Find Filename and Position
    (save-excursion
      ;; Search backward for file header
      (if (re-search-backward "^diff --git a/.* b/\\(.*\\)$" nil t)
          (progn
            (setq filename (match-string 1))
            (setq first-hunk-line-num (codereviewserver-client--find-first-hunk-line))
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
              (unless (string-match-p "^    [│┌└]" line-content)
                (setq count (1+ count))))
            (forward-line 1))
          (setq position count))))

    (list owner repo number filename position reply-to-id)))

(defun codereviewserver-client-add-comment (owner repo number filename position &optional reply-to-id)
  "Open a buffer to add a comment to a review.
If called interactively, attempts to guess parameters from context."
  (interactive
   (let ((ctx (codereviewserver-client--get-comment-context)))
     (unless (and (nth 0 ctx) (nth 3 ctx) (nth 4 ctx))
       (error "Could not determine context (Owner: %s, File: %s, Pos: %s). Are you in a review buffer?"
              (nth 0 ctx) (nth 3 ctx) (nth 4 ctx)))
     ctx))
  (let ((buffer (get-buffer-create (format "*Comment Edit %s/%s #%d*" owner repo number))))
    (with-current-buffer buffer
      (comment-edit-mode)
      (erase-buffer)
      (setq codereviewserver-client--comment-owner owner)
      (setq codereviewserver-client--comment-repo repo)
      (setq codereviewserver-client--comment-number number)
      (setq codereviewserver-client--comment-filename filename)
      (setq codereviewserver-client--comment-position position)
      (setq codereviewserver-client--comment-reply-to-id reply-to-id))
    (switch-to-buffer-other-window buffer)
    (when (fboundp 'evil-insert-state)
      (evil-insert-state))))

(defun codereviewserver-client-submit-review (event body)
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

    (codereviewserver-client--send-request
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
           (with-current-buffer review-buffer
             (let ((inhibit-read-only t))
               (erase-buffer)
               (insert (or content ""))
               (goto-char (point-min))
               (delta-wash)
               (my-code-review-mode)
               (setq buffer-read-only t))))
         (message "Review submitted successfully!"))))))

(defun codereviewserver-client-sync-pr ()
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
    (codereviewserver-client--send-request
     "RPCHandler.SyncPR"
     (vector (list (cons 'Owner owner)
                   (cons 'Repo repo)
                   (cons 'Number number)))
     (lambda (result)
       (let ((content (cdr (assq 'Content result)))
             (review-buffer (get-buffer (format "* Review %s/%s #%d *" owner repo number))))
         (when review-buffer
           (with-current-buffer review-buffer
             (let ((inhibit-read-only t))
               (erase-buffer)
               (insert (or content ""))
               (goto-char (point-min))
               (delta-wash)
               (my-code-review-mode)
               (setq buffer-read-only t))))
         (message "Review synced successfully!"))))))

(provide 'codereviewserver-client)

;;; codereviewserver-client.el ends here
