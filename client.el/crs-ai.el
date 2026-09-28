;;; crs-ai.el --- AI feature reports and on-demand runs. -*- lexical-binding: t; -*-

;;; Commentary:

;; The server's AI features for the PR under review, comments-addressed
;; ("are all review comments addressed, and what is outstanding?") first.
;; Mirrors crs-plugins.el: one output buffer per feature and PR, refreshed on
;; demand.
;;
;; A run happens in the background on the server.  Opening a feature's buffer
;; asks for one when the feature has never run for the PR or its result is
;; stale (the server answers from its cache when nothing changed), and the
;; buffer then polls while the output reads "pending".
;;
;; The body the server sends is a complete markdown report, so this client
;; renders it as-is; a feature without a typed report also gets its line
;; annotations listed underneath, the way plugin output does.

;;; Code:

(require 'crs-vars)
(require 'crs-rpc)
(require 'markdown-mode)

(declare-function crs--get-current-review-info "crs-review")

(defun crs--ai-true-p (value)
  "Non-nil when VALUE is JSON true (false arrives as `:json-false')."
  (eq value t))

(defun crs--ai-pending-p (output)
  "Non-nil when OUTPUT, a GetAIOutput entry, has a run in flight."
  (equal (cdr (assq 'status output)) "pending"))

(defun crs--ai-should-run-p (output)
  "Non-nil when opening OUTPUT's feature should start a run.
That is when the feature has never run for the PR, or its result no longer
describes it, and no run is already in flight."
  (and output
       (not (crs--ai-pending-p output))
       (or (equal (cdr (assq 'status output)) "not-run")
           (crs--ai-true-p (cdr (assq 'stale output))))))

(defun crs--ai-buffer-name (feature-id owner repo number)
  "Name of the buffer showing FEATURE-ID for OWNER/REPO #NUMBER."
  (format "* AI: %s %s/%s #%d *" feature-id owner repo number))

(defun crs--insert-ai-output (output)
  "Insert OUTPUT, one feature's entry from a GetAIOutput reply, at point."
  (let* ((name (or (cdr (assq 'name output)) (cdr (assq 'feature output))))
         (status (or (cdr (assq 'status output)) "unknown"))
         (body (cdr (assq 'body output)))
         (content (cdr (assq 'body_content body)))
         (has-content (and content (not (string-empty-p content))))
         (updated (cdr (assq 'updated_at output)))
         (annotations (append (cdr (assq 'annotations output)) nil)))
    (insert (format "# %s (Status: %s)\n" name status))
    (when (and updated (not (string-empty-p updated)))
      (insert (format "Updated %s\n" updated)))
    (when (and (crs--ai-true-p (cdr (assq 'stale output)))
               (not (crs--ai-pending-p output)))
      (insert "\n> The PR has changed since this report was made (commits, comments, reviews or\n"
              "> resolved threads).  Press R to re-run it.\n"))
    (when (crs--ai-true-p (cdr (assq 'truncated output)))
      (insert "\n> Some input was cut to fit the model's prompt.\n"))
    (when (and (crs--ai-pending-p output) has-content)
      (insert "\n> Refreshing: the report below is the previous one.\n"))
    (insert "──────────────────────────────────\n")
    (cond
     (has-content (insert content))
     ((crs--ai-pending-p output)
      (insert "Running... this buffer refreshes when the result lands."))
     ((equal status "not-run")
      (insert "Not run for this PR yet.  Press R to run it."))
     (t (insert "No output.")))
    (insert "\n")
    ;; A typed report (comments-addressed) already lists every item in its
    ;; body; anything else gets its annotations listed like a plugin's.
    (when (and annotations (null (cdr (assq 'report output))))
      (insert (format "\n## Annotations (%d)\n" (length annotations)))
      (dolist (annotation annotations)
        (let ((severity (or (cdr (assq 'severity annotation)) "")))
          (insert (format "- %s:%s%s %s\n"
                          (cdr (assq 'filename annotation))
                          (cdr (assq 'line annotation))
                          (if (string-empty-p severity) "" (format " [%s]" severity))
                          (replace-regexp-in-string
                           "\n" "\n  " (or (cdr (assq 'content annotation)) "")))))))))

(defun crs--ai-render (buffer output)
  "Render OUTPUT into BUFFER, keeping point where the reader left it."
  (when (buffer-live-p buffer)
    (with-current-buffer buffer
      (let ((inhibit-read-only t)
            (pos (point)))
        (erase-buffer)
        (crs--insert-ai-output output)
        (goto-char (min pos (point-max))))
      (setq crs--ai-output output))))

(defun crs--ai-cancel-poll ()
  "Stop polling for the current AI output buffer."
  (when (timerp crs--ai-poll-timer)
    (cancel-timer crs--ai-poll-timer))
  (setq crs--ai-poll-timer nil))

(defun crs--ai-args (&optional force)
  "RPC arguments naming the current buffer's PR and feature.
FORCE, when non-nil, adds Force for RunAIFeature."
  (vector (append (list (cons 'Owner crs--ai-owner)
                        (cons 'Repo crs--ai-repo)
                        (cons 'Number crs--ai-number)
                        (cons 'Feature (cdr (assq 'id crs--ai-feature))))
                  (when force (list (cons 'Force t))))))

(defun crs--ai-reply-error (result)
  "The error message in RESULT, or nil when it is a normal reply."
  (let ((err (cdr (assq 'error result))))
    (cond ((null err) nil)
          ((stringp err) err)
          (t (format "%s" (or (cdr (assq 'message err)) err))))))

(defun crs--ai-continue (buffer output)
  "Keep polling BUFFER while OUTPUT is pending, within `crs-ai-poll-timeout'."
  (when (buffer-live-p buffer)
    (with-current-buffer buffer
      (crs--ai-cancel-poll)
      (when (crs--ai-pending-p output)
        (unless crs--ai-poll-started
          (setq crs--ai-poll-started (float-time)))
        (if (> (- (float-time) crs--ai-poll-started) crs-ai-poll-timeout)
            (progn
              (setq crs--ai-poll-started nil)
              (message "%s is still running; press r later to refresh."
                       (cdr (assq 'name crs--ai-feature))))
          (setq crs--ai-poll-timer
                (run-with-timer crs-ai-poll-interval nil #'crs--ai-poll buffer))))
      (unless (crs--ai-pending-p output)
        (setq crs--ai-poll-started nil)))))

(defun crs--ai-poll (buffer)
  "Fetch the output shown in BUFFER again, if BUFFER still exists."
  (when (buffer-live-p buffer)
    (with-current-buffer buffer
      (setq crs--ai-poll-timer nil)
      (crs--ai-fetch buffer nil))))

(defun crs--ai-fetch (buffer run-if-needed)
  "Fetch and render the output for BUFFER's PR and feature.
With RUN-IF-NEEDED, start a run when there is no current result."
  (when (buffer-live-p buffer)
    (with-current-buffer buffer
      (let ((feature-id (cdr (assq 'id crs--ai-feature))))
        (crs--send-request
         "RPCHandler.GetAIOutput"
         (crs--ai-args)
         (lambda (result)
           (let ((err (crs--ai-reply-error result))
                 (output (cdr (assq (intern feature-id) (cdr (assq 'output result))))))
             (cond
              (err (message "Could not load the AI report: %s" err))
              ((null output) (message "The server returned no output for %s." feature-id))
              (t
               (crs--ai-render buffer output)
               (if (and run-if-needed (crs--ai-should-run-p output))
                   (crs--ai-run buffer nil)
                 (crs--ai-continue buffer output)))))))))))

(defun crs--ai-run (buffer force)
  "Ask the server to run BUFFER's feature for its PR, then poll.
With FORCE, run even when the stored result covers the PR's inputs."
  (when (buffer-live-p buffer)
    (with-current-buffer buffer
      (crs--send-request
       "RPCHandler.RunAIFeature"
       (crs--ai-args force)
       (lambda (result)
         (let ((err (crs--ai-reply-error result))
               (output (cdr (assq 'output result))))
           (cond
            (err (message "Could not run the AI feature: %s" err))
            (t
             (when output
               (crs--ai-render buffer output))
             (unless (eq (cdr (assq 'okay result)) t)
               (message "%s" (cdr (assq 'message result))))
             (when output
               (crs--ai-continue buffer output))))))))))

(defun crs--ai-with-features (callback)
  "Call CALLBACK once the enabled AI features are known.
Fetches them with ListAIFeatures the first time."
  (if crs-ai-features
      (funcall callback)
    (crs--send-request
     "RPCHandler.ListAIFeatures"
     (vector)
     (lambda (result)
       (setq crs-ai-features
             (seq-filter (lambda (f) (crs--ai-true-p (cdr (assq 'enabled f))))
                         (append (cdr (assq 'features result)) nil)))
       (funcall callback)))))

(defun crs--ai-choose-feature (features)
  "Pick one of FEATURES: the only one, or the one the user names."
  (cond
   ((null features)
    (user-error "No AI features are enabled; add an [[AIFeatures]] entry to the server config"))
   ((null (cdr features)) (car features))
   (t
    (let* ((names (mapcar (lambda (f) (cons (cdr (assq 'name f)) f)) features))
           (choice (completing-read "AI feature: " names nil t)))
      (cdr (assoc choice names))))))

(defun crs--ai-open (owner repo number feature)
  "Show FEATURE's output for OWNER/REPO #NUMBER in its buffer and return it."
  (let ((buffer (get-buffer-create
                 (crs--ai-buffer-name (cdr (assq 'id feature)) owner repo number))))
    (with-current-buffer buffer
      (unless (derived-mode-p 'crs-ai-output-mode)
        (crs-ai-output-mode))
      (setq crs--ai-owner owner
            crs--ai-repo repo
            crs--ai-number number
            crs--ai-feature feature)
      (when (= (buffer-size) 0)
        (let ((inhibit-read-only t))
          (insert (format "# %s\nLoading...\n" (cdr (assq 'name feature)))))))
    (pop-to-buffer buffer)
    buffer))

;;;###autoload
(defun crs-ai-list-features ()
  "Fetch the AI features the server has enabled into `crs-ai-features'."
  (interactive)
  (setq crs-ai-features nil)
  (crs--ai-with-features
   (lambda ()
     (message "AI features enabled: %s"
              (if crs-ai-features
                  (mapconcat (lambda (f) (cdr (assq 'name f))) crs-ai-features ", ")
                "none")))))

;;;###autoload
(defun crs-get-ai-output ()
  "Show an AI feature's report for the PR in the current review buffer.
Starts a run when the feature has never run for the PR or its report is
stale, and keeps the buffer updated while the run is pending."
  (interactive)
  (let* ((info (crs--get-current-review-info))
         (owner (nth 0 info))
         (repo (nth 1 info))
         (number (nth 2 info)))
    (crs--ai-with-features
     (lambda ()
       (let ((buffer (crs--ai-open owner repo number
                                   (crs--ai-choose-feature crs-ai-features))))
         (crs--ai-fetch buffer t))))))

;;;###autoload
(defun crs-run-ai-feature (&optional force)
  "Run an AI feature for the current PR and show its buffer.
From an AI output buffer, runs that buffer's feature.  With a prefix
argument FORCE, run even when the report already covers the PR."
  (interactive "P")
  (if (derived-mode-p 'crs-ai-output-mode)
      (crs--ai-run (current-buffer) force)
    (let* ((info (crs--get-current-review-info))
           (owner (nth 0 info))
           (repo (nth 1 info))
           (number (nth 2 info)))
      (crs--ai-with-features
       (lambda ()
         (crs--ai-run (crs--ai-open owner repo number
                                    (crs--ai-choose-feature crs-ai-features))
                      force))))))

(defun crs-ai-refresh ()
  "Fetch the report in the current AI output buffer again."
  (interactive)
  (unless (and crs--ai-owner crs--ai-repo crs--ai-number crs--ai-feature)
    (user-error "Not in an AI output buffer"))
  (crs--ai-fetch (current-buffer) nil))

(defun crs-ai-rerun ()
  "Run the current AI output buffer's feature again, whatever is cached."
  (interactive)
  (unless (and crs--ai-owner crs--ai-repo crs--ai-number crs--ai-feature)
    (user-error "Not in an AI output buffer"))
  (message "Running %s..." (cdr (assq 'name crs--ai-feature)))
  (crs--ai-run (current-buffer) t))

(defun crs-quit-ai-output ()
  "Quit the AI output window and kill its buffer."
  (interactive)
  (quit-window t))

(defvar-keymap crs-ai-output-mode-map
  :doc "Keymap for `crs-ai-output-mode'.  Bindings are defined in crs-client.el.")

(define-derived-mode crs-ai-output-mode markdown-mode "AI Output"
  "Major mode for an AI feature's report on a PR.
Inherits from `markdown-mode' and is read-only.
\\{crs-ai-output-mode-map}"
  (setq buffer-read-only t)
  (add-hook 'kill-buffer-hook #'crs--ai-cancel-poll nil t))

(provide 'crs-ai)
;;; crs-ai.el ends here
