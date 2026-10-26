;;; crs-plugins.el --- Plugin-output buffers and on-demand plugin execution. -*- lexical-binding: t; -*-

;;; Commentary:

;; Plugin-output buffers and on-demand plugin execution.
;;
;; Plugins run in the background on the server, which announces each change
;; to a PR's plugin output (Server.PluginOutputChanged): a plugin marked
;; pending, a result landing, a rerun clearing one.  An open plugin output
;; buffer for the PR reads its output again on each, so it follows a run
;; without being refreshed by hand, and a run asked for from here is
;; announced in the echo area when it lands.

;;; Code:

(require 'crs-vars)
(require 'crs-rpc)
(require 'crs-render)
(require 'markdown-mode)

(declare-function crs--render-and-update "crs-render")
(declare-function crs--get-current-review-info "crs-review")

(defun crs--insert-plugin-output-entry (name data)
  "Insert one plugin's entry from a GetPluginOutput reply at point.
NAME is the plugin name and DATA its output alist.  The parsed response
contract body is preferred over the raw result (which is JSON for
contract-emitting plugins); the plugin's annotations, when present, are
listed beneath the body sorted by file and line."
  (let* ((status (cdr (assq 'status data)))
         (body (cdr (assq 'body data)))
         (body-content (cdr (assq 'body_content body)))
         (res (cdr (assq 'result data)))
         (annotations (append (cdr (assq 'annotations data)) nil)))
    (insert (format "# Plugin: %s (Status: %s)\n" name status))
    (insert "──────────────────────────────────\n")
    (insert (or (and body-content
                     (not (string-empty-p body-content))
                     body-content)
                res
                "No output."))
    (insert "\n")
    (when annotations
      (setq annotations
            (sort annotations
                  (lambda (a b)
                    (let ((file-a (or (cdr (assq 'filename a)) ""))
                          (file-b (or (cdr (assq 'filename b)) "")))
                      (if (string= file-a file-b)
                          (< (or (cdr (assq 'line a)) 0)
                             (or (cdr (assq 'line b)) 0))
                        (string< file-a file-b))))))
      (insert (format "\n## Annotations (%d)\n" (length annotations)))
      (dolist (annotation annotations)
        (let ((severity (or (cdr (assq 'severity annotation)) ""))
              (content (or (cdr (assq 'content annotation)) "")))
          (insert (format "- %s:%s%s %s\n"
                          (cdr (assq 'filename annotation))
                          (cdr (assq 'line annotation))
                          (if (string-empty-p severity) "" (format " [%s]" severity))
                          (replace-regexp-in-string "\n" "\n  " content))))))
    (insert "\n")))

(defun crs--refresh-plugin-buffer (buffer &optional quiet)
  "Fetch the plugin output shown in BUFFER again and redraw it.
With QUIET, as for a change the server announced, say nothing and keep
point where the reader left it.  One fetch runs at a time per buffer:
one asked for meanwhile runs once it lands."
  (when (buffer-live-p buffer)
    (with-current-buffer buffer
      (if crs--plugin-refreshing
          (setq crs--plugin-refreshing 'again)
        (setq crs--plugin-refreshing t)
        (let ((owner crs--plugin-owner)
              (repo crs--plugin-repo)
              (number crs--plugin-number))
          (unless quiet
            (message "Refreshing plugin output for %s/%s #%d..." owner repo number))
          (crs--send-request
           "RPCHandler.GetPluginOutput"
           (vector (list (cons 'Owner owner)
                         (cons 'Repo repo)
                         (cons 'Number number)))
           (lambda (result)
             (when (buffer-live-p buffer)
               (with-current-buffer buffer
                 (let ((again (eq crs--plugin-refreshing 'again)))
                   (setq crs--plugin-refreshing nil)
                   (unless (cdr (assq 'error result))
                     (crs--render-plugin-buffer (cdr (assq 'output result)) quiet)
                     (unless quiet (message "Plugin output refreshed.")))
                   (when again (crs--refresh-plugin-buffer buffer t))))))))))))

(defun crs--render-plugin-buffer (output keep-point)
  "Redraw the current plugin output buffer from OUTPUT, a GetPluginOutput map.
A buffer showing one plugin shows only that one.  With KEEP-POINT, point
stays where it was rather than going back to the top."
  (let ((inhibit-read-only t)
        (pos (point))
        (target-plugin crs--plugin-name))
    (erase-buffer)
    (setq crs--plugin-output-map (make-hash-table :test 'equal))
    (if (null output)
        (insert "No plugin output available.\n")
      (dolist (plugin-entry (append output nil))
        (let* ((name (symbol-name (car plugin-entry)))
               (data (cdr plugin-entry)))
          (puthash name data crs--plugin-output-map)
          (when (or (null target-plugin) (string= name target-plugin))
            (crs--insert-plugin-output-entry name data)))))
    (goto-char (if keep-point (min pos (point-max)) (point-min)))))

(defun crs-refresh-plugin-output ()
  "Refresh the plugin output in the current buffer."
  (interactive)
  (unless (and crs--plugin-owner crs--plugin-repo crs--plugin-number)
    (error "Not in a plugin output buffer or missing PR context"))
  (crs--refresh-plugin-buffer (current-buffer)))

(defun crs--plugin-buffers (&optional owner repo number)
  "Live plugin output buffers, or only those showing OWNER/REPO #NUMBER."
  (seq-filter (lambda (buffer)
                (with-current-buffer buffer
                  (and (derived-mode-p 'crs-plugin-output-mode)
                       crs--plugin-number
                       (or (null number)
                           (crs--server-same-pr-p
                            (list (cons 'owner owner) (cons 'repo repo)
                                  (cons 'number number))
                            crs--plugin-owner crs--plugin-repo crs--plugin-number)))))
              (buffer-list)))

(defun crs--plugin-run-key (owner repo number plugin)
  "The `crs--plugin-runs-requested' key of PLUGIN's run on OWNER/REPO #NUMBER."
  (format "%s/%s#%d:%s" (downcase owner) (downcase repo) number plugin))

(defun crs--plugin-on-notification (method data)
  "Follow a Server.PluginOutputChanged notification, METHOD with DATA.
Each open plugin output buffer for the PR reads its output again, and a
run asked for from here is announced once it lands."
  (when (equal method "Server.PluginOutputChanged")
    (let ((owner (cdr (assq 'owner data)))
          (repo (cdr (assq 'repo data)))
          (number (cdr (assq 'number data)))
          (plugin (cdr (assq 'plugin data)))
          (status (cdr (assq 'status data))))
      (when (and (stringp owner) (stringp repo) (integerp number))
        (dolist (buffer (crs--plugin-buffers owner repo number))
          (crs--refresh-plugin-buffer buffer t))
        (when (and (stringp plugin) (member status '("success" "error")))
          (let ((key (crs--plugin-run-key owner repo number plugin)))
            (when (gethash key crs--plugin-runs-requested)
              (remhash key crs--plugin-runs-requested)
              (message "Plugin '%s' %s for %s/%s #%d%s"
                       plugin (if (equal status "success") "finished" "failed")
                       owner repo number
                       (if (crs--plugin-buffers owner repo number)
                           ""
                         (substitute-command-keys
                          "; \\<my-code-review-mode-map>\\[crs-get-plugin-output] in its review shows it"))))))))))

(defun crs--plugin-on-subscribed ()
  "Read every open plugin output buffer's output again, for a new server.
Runs asked for from the old one are gone with it."
  (clrhash crs--plugin-runs-requested)
  (dolist (buffer (crs--plugin-buffers))
    (crs--refresh-plugin-buffer buffer t)))

(add-hook 'crs-server-notification-functions #'crs--plugin-on-notification)
(add-hook 'crs-server-subscribed-hook #'crs--plugin-on-subscribed)


(defun crs-quit-plugin-output ()
  "Quit the plugin output window and kill the buffer."
  (interactive)
  (quit-window t))

(defun crs-wash-plugin-output ()
  "Run the selected washer on the plugin output buffer.
Temporarily disables read-only mode (required when called from
`crs-plugin-output-mode'), calls `delta-wash', then restores read-only."
  (interactive)
  (let ((was-read-only (and (eq major-mode 'crs-plugin-output-mode)
                            buffer-read-only)))
    (when was-read-only
      (setq buffer-read-only nil))
    (unwind-protect
        (delta-wash)
      (when was-read-only
        (setq buffer-read-only t)))))

(defvar-keymap crs-plugin-output-mode-map
  :doc "Keymap for `crs-plugin-output-mode'.  Bindings are defined in crs-client.el.")

(define-derived-mode crs-plugin-output-mode markdown-mode "Plugin Output"
  "Major mode for viewing plugin output.
  Inherits from markdown-mode and is read-only.
  \\{crs-plugin-output-mode-map}"
  (setq buffer-read-only t))


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
             (buffer (get-buffer-create (format "* Plugin Output %s/%s #%d *" owner repo number)))
             (plugin-map (make-hash-table :test 'equal)))
         (with-current-buffer buffer
           (let ((inhibit-read-only t))
             (erase-buffer)
             (if (null output)
                 (insert "No plugin output available.\n")
               (dolist (plugin-entry (append output nil)) ;; Ensure it's treated as a list of pairs
                 (let* ((name (symbol-name (car plugin-entry)))
                        (data (cdr plugin-entry)))
                   (puthash name data plugin-map)
                   (crs--insert-plugin-output-entry name data))))
             (goto-char (point-min))
             (crs-plugin-output-mode)
             (setq crs--plugin-output-map plugin-map)
             ;; Store PR context for refresh
             (setq crs--plugin-owner owner)
             (setq crs--plugin-repo repo)
             (setq crs--plugin-number number)))
         (pop-to-buffer buffer)
         (message "Plugin output loaded."))))))

(defun crs-get-single-plugin-output (&optional plugin-name)
  "Fetch and display output for a single plugin.
If PLUGIN-NAME is nil, prompts the user to select one.
Uses cached data from the general plugin output buffer if available."
  (interactive)
  (let ((owner crs--plugin-owner)
        (repo crs--plugin-repo)
        (number crs--plugin-number))
    ;; If not in a buffer with plugin vars, try to extract from review buffer name
    (unless (and owner repo number)
      (let ((info (crs--get-current-review-info)))
        (setq owner (nth 0 info)
              repo (nth 1 info)
              number (nth 2 info))))

    (if (and (null plugin-name) (null crs-plugins))
        (progn
          (message "No plugins found. Refreshing list... please try again in a moment.")
          (crs-list-plugins))
      ;; Defensive: Ensure crs-plugins is a list of strings
      (let* ((candidates (if (vectorp crs-plugins) (append crs-plugins nil) crs-plugins))
             (candidates (mapcar (lambda (item)
                                   (if (and (listp item) (assq 'Name item))
                                       (cdr (assq 'Name item))
                                     item))
                                 candidates))
             (plugin (or plugin-name
                         (completing-read "Plugin: " candidates nil t)))
             (general-buf-name (format "* Plugin Output %s/%s #%d *" owner repo number))
             (general-buf (get-buffer general-buf-name))
             (cached-map (when (and general-buf (buffer-live-p general-buf))
                           (with-current-buffer general-buf
                             crs--plugin-output-map)))
             (cached-data (when cached-map (gethash plugin cached-map))))

        (if cached-data
            (let ((buffer (get-buffer-create (format "* Plugin Output: %s %s/%s #%d *" plugin owner repo number))))
              (with-current-buffer buffer
                (let ((inhibit-read-only t))
                  (erase-buffer)
                  (crs--insert-plugin-output-entry plugin cached-data)
                  (goto-char (point-min))
                  (crs-plugin-output-mode)
                  ;; Store context
                  (setq crs--plugin-output-map cached-map)
                  (setq crs--plugin-owner owner)
                  (setq crs--plugin-repo repo)
                  (setq crs--plugin-number number)
                  (setq crs--plugin-name plugin)))
              (pop-to-buffer buffer)
              (message "Plugin output loaded from cache."))

          (message "Fetching output for plugin %s..." plugin)
          (crs--send-request
           "RPCHandler.GetPluginOutput"
           (vector (list (cons 'Owner owner)
                         (cons 'Repo repo)
                         (cons 'Number number)))
           (lambda (result)
             (let ((output (cdr (assq 'output result)))
                   (buffer (get-buffer-create (format "* Plugin Output: %s %s/%s #%d *" plugin owner repo number)))
                   (plugin-map (make-hash-table :test 'equal)))
               (with-current-buffer buffer
                 (let ((inhibit-read-only t))
                   (erase-buffer)
                   (if (null output)
                       (insert "No plugin output available.\n")
                     (dolist (plugin-entry (append output nil))
                       (let* ((name (symbol-name (car plugin-entry)))
                              (data (cdr plugin-entry)))
                         (puthash name data plugin-map)
                         (when (string= name plugin)
                           (crs--insert-plugin-output-entry name data)))))
                   (goto-char (point-min))
                   (crs-plugin-output-mode)
                   (setq crs--plugin-output-map plugin-map)
                   ;; Store PR context for refresh
                   (setq crs--plugin-owner owner)
                   (setq crs--plugin-repo repo)
                   (setq crs--plugin-number number)
                   (setq crs--plugin-name plugin)))
               (pop-to-buffer buffer)
               (message "Plugin output loaded.")))))))))

(defun crs-run-on-demand-plugin ()
  "Run an on-demand plugin for the current PR.
Presents a selection of plugins marked as OnlyOnDemand and triggers
async execution via RerunPlugins.  The echo area says when it lands, and
an open plugin output buffer for the PR (\\[crs-get-plugin-output])
shows the result as it does."
  (interactive)
  (if (null crs-plugins-full)
      (progn
        (message "No plugins loaded. Refreshing list... please try again in a moment.")
        (crs-list-plugins))
    (let* ((on-demand (seq-filter
                       (lambda (p) (eq (cdr (assq 'OnlyOnDemand p)) t))
                       crs-plugins-full))
           (candidates (mapcar (lambda (p) (cdr (assq 'Name p))) on-demand)))
      (if (null candidates)
          (message "No on-demand plugins configured.")
        (let* ((plugin (completing-read "Run on-demand plugin: " candidates nil t))
               (info (crs--get-current-review-info))
               (owner (nth 0 info))
               (repo (nth 1 info))
               (number (nth 2 info)))
          (message "Running on-demand plugin '%s' for %s/%s #%d..." plugin owner repo number)
          (puthash (crs--plugin-run-key owner repo number plugin) t crs--plugin-runs-requested)
          (crs--send-request
           "RPCHandler.RerunPlugins"
           (vector (list (cons 'Owner owner)
                         (cons 'Repo repo)
                         (cons 'Number number)
                         (cons 'Plugins (vector plugin))))
           (lambda (result)
             (let ((msg (cdr (assq 'message result))))
               (message "%s" (or msg "On-demand plugin triggered."))))))))))

(defun crs-rerun-plugin ()
  "Rerun any plugin for the current PR.
Presents a selection of all configured plugins and triggers
async execution via RerunPlugins.  The echo area says when it lands, and
an open plugin output buffer for the PR (\\[crs-get-plugin-output])
shows the result as it does."
  (interactive)
  (if (null crs-plugins-full)
      (progn
        (message "No plugins loaded. Refreshing list... please try again in a moment.")
        (crs-list-plugins))
    (let* ((candidates (mapcar (lambda (p) (cdr (assq 'Name p))) crs-plugins-full)))
      (if (null candidates)
          (message "No plugins configured.")
        (let* ((plugin (completing-read "Rerun plugin: " candidates nil t))
               (info (crs--get-current-review-info))
               (owner (nth 0 info))
               (repo (nth 1 info))
               (number (nth 2 info)))
          (message "Rerunning plugin '%s' for %s/%s #%d..." plugin owner repo number)
          (puthash (crs--plugin-run-key owner repo number plugin) t crs--plugin-runs-requested)
          (crs--send-request
           "RPCHandler.RerunPlugins"
           (vector (list (cons 'Owner owner)
                         (cons 'Repo repo)
                         (cons 'Number number)
                         (cons 'Plugins (vector plugin))))
           (lambda (result)
             (let ((msg (cdr (assq 'message result))))
               (message "%s" (or msg "Plugin rerun triggered."))))))))))

(provide 'crs-plugins)
;;; crs-plugins.el ends here
