;;; crs-plugins.el --- Plugin functionality for crs-client -*- lexical-binding: t; -*-

;; SPDX-License-Identifier: GPL-3.0+

;;; Commentary:

;; Handles plugin listing, output fetching and display for the crs client.
;; Provides `crs-plugin-output-mode' and interactive commands for viewing
;; per-plugin results from the server.

;;; Code:

(require 'crs-utils)
(require 'washer)
(require 'markdown-mode)

;; Forward declarations for functions defined in crs-client.el
(declare-function crs--send-request "crs-client")

;;; Plugin state

(defvar crs-plugins nil
  "List of plugins configured on the server.")

;;; Buffer-local variables for plugin output mode

(defvar-local crs--plugin-owner nil
  "Owner of the PR for plugin output.")
(defvar-local crs--plugin-repo nil
  "Repo of the PR for plugin output.")
(defvar-local crs--plugin-number nil
  "PR number for plugin output.")
(defvar-local crs--plugin-output-map nil
  "Hash table mapping plugin names to their output data.")
(defvar-local crs--plugin-name nil
  "If non-nil, this buffer displays output only for this plugin.")

;;; Plugin list

(defun crs-list-plugins ()
  "Call the ListPlugins RPC method and store the result in `crs-plugins'."
  (interactive)
  (crs--send-request
   "RPCHandler.ListPlugins"
   (vector)
   (lambda (result)
     (let ((plugins-list (append (cdr (assq 'plugins result)) nil)))
       (setq crs-plugins (mapcar (lambda (p) (cdr (assq 'Name p))) plugins-list))
       (message "Plugins updated: %d plugins found" (length crs-plugins))))))

;;; Plugin output mode

(defun crs-refresh-plugin-output ()
  "Refresh the plugin output in the current buffer."
  (interactive)
  (unless (and crs--plugin-owner crs--plugin-repo crs--plugin-number)
    (error "Not in a plugin output buffer or missing PR context"))
  (let ((owner crs--plugin-owner)
        (repo crs--plugin-repo)
        (number crs--plugin-number)
        (target-plugin crs--plugin-name))
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
             (setq crs--plugin-output-map (make-hash-table :test 'equal))
             (if (null output)
                 (insert "No plugin output available.\n")
               (dolist (plugin-entry (append output nil))
                 (let* ((name (symbol-name (car plugin-entry)))
                        (data (cdr plugin-entry))
                        (res (cdr (assq 'result data)))
                        (status (cdr (assq 'status data))))
                   (puthash name data crs--plugin-output-map)
                   (when (or (null target-plugin) (string= name target-plugin))
                     (insert (format "# Plugin: %s (Status: %s)\n" name status))
                     (insert "──────────────────────────────────\n")
                     (insert (or res "No output."))
                     (insert "\n\n")))))
             (goto-char (point-min))))
         (message "Plugin output refreshed."))))))

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
  "r" #'crs-refresh-plugin-output
  "q" #'crs-quit-plugin-output
  "w" #'crs-wash-plugin-output)

(define-derived-mode crs-plugin-output-mode markdown-mode "Plugin Output"
  "Major mode for viewing plugin output.
  Inherits from markdown-mode and is read-only.
  \\{crs-plugin-output-mode-map}"
  (setq buffer-read-only t))

(when (fboundp 'evil-define-key)
  (evil-define-key 'normal crs-plugin-output-mode-map
    "r" #'crs-refresh-plugin-output
    "q" #'crs-quit-plugin-output
    "w" #'crs-wash-plugin-output))

;;; Fetching plugin output

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
               (dolist (plugin-entry (append output nil))
                 (let* ((name (symbol-name (car plugin-entry)))
                        (data (cdr plugin-entry))
                        (res (cdr (assq 'result data)))
                        (status (cdr (assq 'status data))))
                   (puthash name data plugin-map)
                   (insert (format "# Plugin: %s (Status: %s)\n" name status))
                   (insert "──────────────────────────────────\n")
                   (insert (or res "No output."))
                   (insert "\n\n"))))
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
                  (let* ((status (cdr (assq 'status cached-data)))
                         (res (cdr (assq 'result cached-data))))
                    (insert (format "# Plugin: %s (Status: %s)\n" plugin status))
                    (insert "──────────────────────────────────\n")
                    (insert (or res "No output."))
                    (insert "\n\n"))
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
                              (data (cdr plugin-entry))
                              (res (cdr (assq 'result data)))
                              (status (cdr (assq 'status data))))
                         (puthash name data plugin-map)
                         (when (string= name plugin)
                           (insert (format "# Plugin: %s (Status: %s)\n" name status))
                           (insert "──────────────────────────────────\n")
                           (insert (or res "No output."))
                           (insert "\n\n")))))
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

(provide 'crs-plugins)

;;; crs-plugins.el ends here
