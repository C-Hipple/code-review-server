;;; shot.el --- Screenshot a crs review buffer.  -*- lexical-binding: t; -*-
;;
;; xvfb-run -a -s "-screen 0 1800x1400x24" emacs -Q -l shot.el
;; Env: CRS_DIR (client.el dir), SHOT_OUT (png prefix), SHOT_THEME
;; (modus-vivendi / modus-operandi / ...), SHOT_SCENES (comma list).

(package-initialize)
(dolist (d (file-expand-wildcards "/usr/share/emacs/site-lisp/elpa/*")) (add-to-list 'load-path d))
(run-with-timer 50 nil (lambda () (with-temp-file (concat (or (getenv "SHOT_OUT") "shot") "-error.txt") (insert "timeout\n" (with-current-buffer "*Messages*" (buffer-string)))) (kill-emacs 3)))
(setq inhibit-startup-screen t
      ring-bell-function #'ignore
      initial-scratch-message nil)
(menu-bar-mode -1) (tool-bar-mode -1) (scroll-bar-mode -1)
(blink-cursor-mode -1)
(set-face-attribute 'default nil :family "JetBrains Mono" :height 105)
(set-fontset-font t 'emoji (font-spec :family "Noto Color Emoji") nil 'prepend)
(set-fontset-font t 'symbol (font-spec :family "Noto Color Emoji") nil 'append)

(defvar shot-theme (or (getenv "SHOT_THEME") "modus-vivendi"))
(load-theme (intern shot-theme) t)
(defvar shot-light (string-match-p "operandi\\|light\\|leuven\\|adwaita" shot-theme))

;; Stand-in for magit-delta: run the diff through delta the way magit-delta
;; does (--color-only) and colour it with overlays.
(require (quote ansi-color))
(defun magit-delta-call-delta-and-convert-ansi-escape-sequences ()
  (let ((buffer-read-only nil))
    (apply #'call-process-region (point-min) (point-max) "delta" t t nil
           (append '("--max-line-distance" "0.6" "--true-color" "always" "--color-only")
                   (when shot-light '("--light"))))
    (let ((ansi-color-apply-face-function #'ansi-color-apply-overlay-face))
      (ansi-color-apply-on-region (point-min) (point-max)))))

(add-to-list 'load-path (or (getenv "CRS_DIR") "."))
(require 'crs-client)

(defconst shot-diff "diff --git a/workflows/retry.go b/workflows/retry.go
index 3b1f0a2..9c4d7e1 100644
--- a/workflows/retry.go
+++ b/workflows/retry.go
@@ -12,9 +12,18 @@ import (
 \t\"time\"
 )
 
-// fetchWithRetry fetches the PR list, retrying once on failure.
-func fetchWithRetry(ctx context.Context, repo string) ([]*PR, error) {
-\treturn fetch(ctx, repo)
+// RetryFetch fetches the PR list, retrying transient failures.
+func RetryFetch(ctx context.Context, repo string, attempts int) ([]*PR, error) {
+\tvar lastErr error
+\tfor i := 0; i < attempts; i++ {
+\t\tprs, err := fetch(ctx, repo)
+\t\tif err == nil {
+\t\t\treturn prs, nil
+\t\t}
+\t\tlastErr = err
+\t\ttime.Sleep(2 * time.Second)
+\t}
+\treturn nil, fmt.Errorf(\"fetch %s: %w\", repo, lastErr)
 }
 
 func fetch(ctx context.Context, repo string) ([]*PR, error) {
diff --git a/server/handlers.go b/server/handlers.go
index 77aa012..81bc3f9 100644
--- a/server/handlers.go
+++ b/server/handlers.go
@@ -40,8 +40,9 @@ func (h *RPCHandler) Refresh(args *RefreshArgs, reply *RefreshReply) error {
 \tif args.Repo == \"\" {
 \t\treturn errors.New(\"missing repo\")
 \t}
-\tprs, err := fetchWithRetry(h.ctx, args.Repo)
+\tprs, err := RetryFetch(h.ctx, args.Repo, 3)
 \tif err != nil {
-\t\treturn err
+\t\tslog.Error(\"refresh failed\", \"err\", err)
+\t\treturn errors.New(\"refresh failed\")
 \t}
 \treply.Count = len(prs)
")

(defconst shot-payload
  `((okay . t)
    (metadata . ((number . 341) (title . "Retry transient fetch failures")
                 (author . "alice") (state . "open")
                 (url . "https://github.com/acme/widgets/pull/341")
                 (base_ref . "main") (head_ref . "alice/retry-fetch")
                 (labels . ["enhancement"]) (draft . :json-false)
                 (reviewers . ["bob" "carol"]) (requested_teams . [])
                 (approved_by . []) (changes_requested_by . []) (commented_by . ["bob"])
                 (ci_status . "success") (ci_failures . [])
                 (changed_files . 2) (additions . 15) (deletions . 4)
                 (body . "Retries the PR list fetch a few times before giving up, so one flaky response doesn't empty a section.")))
    (diff . ,shot-diff)
    (comments . [((id . "2190331001") (author . "bob")
                  (body . "Should this back off? A fixed `time.Sleep(2 * time.Second)` will keep hammering the API while GitHub is rate-limiting us.")
                  (path . "workflows/retry.go") (position . "16")
                  (created_at . "2026-11-23T15:04:05Z")
                  (reactions . [((content . "+1") (emoji . "👍") (count . 1) (users . ["alice"]))]))
                 ((id . "2190331544") (author . "alice")
                  (body . "Good call, switching to exponential backoff with jitter.")
                  (path . "workflows/retry.go") (position . "16")
                  (created_at . "2026-11-23T16:40:12Z"))
                 ((id . "17") (author . "local")
                  (body . "Let's also cap the total wait at a minute.")
                  (path . "workflows/retry.go") (position . "16")
                  (created_at . "2026-11-25T09:12:00Z"))
                 ((id . "2190339876") (author . "carol")
                  (body . "nit: the caller loses the cause here; wrap `err` instead of replacing it.")
                  (path . "server/handlers.go") (position . "9")
                  (created_at . "2026-11-24T10:22:31Z"))])
    (reviews . [((user . "bob") (state . "COMMENTED") (body . "A couple of thoughts on the retry loop.")
                 (submitted_at . "2026-11-23T15:05:00Z"))])
    (commits . [((sha . "9c4d7e1a2b") (author . "alice") (date . "2026-11-22T12:00:00Z")
                 (message . "Retry transient fetch failures"))])
    (annotations . [((filename . "workflows/retry.go") (line . 16) (severity . "warning")
                     (plugin . "style_guidelines")
                     (content . "Exported function RetryFetch has no doc comment naming its retry policy."))
                    ((filename . "workflows/retry.go") (line . 26) (severity . "info")
                     (plugin . "summarize_diff")
                     (content . "Retries up to `attempts` times, 2s apart, wrapping the last error."))
                    ((filename . "server/handlers.go") (line . 46) (severity . "error")
                     (plugin . "security_check")
                     (content . "The underlying error is logged but not returned; callers can't tell rate limits from auth failures."))])))

(defun shot-take (file)
  (redisplay t) (sit-for 0.3) (redisplay t)
  (with-temp-file file
    (set-buffer-multibyte nil)
    (insert (x-export-frames nil 'png))))

(defun shot-scroll-to (regexp &optional above)
  (goto-char (point-min))
  (when (re-search-forward regexp nil t)
    (forward-line 0)
    (set-window-start (selected-window)
                      (save-excursion (forward-line (- (or above 0))) (point)))
    (goto-char (point))))

(defun shot-run ()
  (let* ((out (or (getenv "SHOT_OUT") "shot"))
         (scenes (split-string (or (getenv "SHOT_SCENES") "expanded,collapsed") ","))
         (buffer (get-buffer-create "* CRS: #341 - widgets - acme *")))
    (set-frame-size (selected-frame) (string-to-number (or (getenv "SHOT_COLS") "128")) (string-to-number (or (getenv "SHOT_ROWS") "60")))
    (switch-to-buffer buffer)
    (crs--render-and-update buffer shot-payload)
    (dolist (scene scenes)
      (with-current-buffer buffer
        (pcase scene
          ("expanded"
           (unless crs--buffer-show-comments (crs-toggle-comments))
           (unless crs--buffer-show-annotations (crs-toggle-annotations)))
          ("default"
           (unless crs--buffer-show-comments (crs-toggle-comments))
           (when crs--buffer-show-annotations (crs-toggle-annotations)))
          ("collapsed"
           (when crs--buffer-show-comments (crs-toggle-comments))))
        (message nil)
        (shot-scroll-to "^\\(modified\\|new file\\)[[:space:]]+workflows/retry.go" 1)
        (goto-char (point-max))
        (set-window-point (selected-window) (window-start))
        (shot-take (format "%s-%s.png" out scene))))))

(condition-case err
    (shot-run)
  (error (with-temp-file (concat (or (getenv "SHOT_OUT") "shot") "-error.txt")
           (insert (format "%S" err)))))
(kill-emacs 0)
