;;; crs-utils.el --- Utility functions for crs-client -*- lexical-binding: t; -*-

;; SPDX-License-Identifier: GPL-3.0+

;;; Commentary:

;; Shared utility functions used across the crs client package:
;; HTML rendering helpers, diff header simplification, review buffer
;; info extraction, and git checkout helpers.

;;; Code:

(require 'shr)
(require 'subr-x)

(defconst crs--html-placeholder-regexp
  "<CRS-HTML\\(?: prefix=\"\\(.*?\\)\"\\)?>\\(.*?\\)</CRS-HTML>"
  "Regexp to match HTML placeholders for deferred rendering.")

(defun crs--ensure-html (text)
  "Return TEXT as HTML suitable for shr rendering.
If TEXT already looks like HTML (starts with a tag), return it unchanged.
Otherwise convert plain text/Markdown line breaks to HTML paragraphs and
br elements so that shr preserves the visual line structure."
  (if (string-match-p "\\`[[:space:]]*<" text)
      text
    (let* ((escaped (replace-regexp-in-string "&" "&amp;" text))
           (escaped (replace-regexp-in-string "<" "&lt;" escaped))
           (escaped (replace-regexp-in-string ">" "&gt;" escaped))
           (with-paras (replace-regexp-in-string "\n\n+" "</p><p>" escaped))
           (with-brs (replace-regexp-in-string "\n" "<br>" with-paras)))
      (concat "<p>" with-brs "</p>"))))

(defun crs--insert-html (html-string &optional prefix)
  "Insert HTML-STRING at point, rendering it with shr.
If PREFIX is provided, it is prepended to each line of the rendered content.
Images will be displayed inline if running in graphical Emacs.
Requires Emacs to be compiled with libxml support."
  (if (and html-string (not (string-empty-p html-string)))
      (let ((start (point)))
        (insert (crs--ensure-html html-string))
        (when (fboundp 'libxml-parse-html-region)
          (let ((dom (libxml-parse-html-region start (point))))
            (delete-region start (point))
            (shr-insert-document dom)))
        (when (and prefix (not (string-empty-p prefix)))
          (let ((end (point-marker)))
            (save-excursion
              (goto-char start)
              (while (< (point) end)
                (insert prefix)
                (unless (zerop (forward-line 1))
                  (goto-char end))))
            (set-marker end nil))))
    (insert (or prefix "") "(No content provided)")))

(defun crs--make-html-placeholder (html-string &optional prefix)
  "Create a placeholder string for HTML-STRING to be rendered later.
If PREFIX is provided, it will be used when rendering each line.
The content and prefix are base64 encoded to avoid issues with special characters."
  (let ((html-encoded (if (and html-string (not (string-empty-p html-string)))
                          (base64-encode-string (encode-coding-string html-string 'utf-8) t)
                        "")))
    (if (string-empty-p html-encoded)
        (concat (or prefix "") "(No content provided)")
      (if (and prefix (not (string-empty-p prefix)))
          (format "<CRS-HTML prefix=\"%s\">%s</CRS-HTML>"
                  (base64-encode-string (encode-coding-string prefix 'utf-8) t)
                  html-encoded)
        (format "<CRS-HTML>%s</CRS-HTML>" html-encoded)))))

(defun crs--process-html-placeholders ()
  "Find and replace all HTML placeholders in the current buffer with rendered HTML.
This should be called after inserting content but before setting the buffer to read-only."
  (save-excursion
    (goto-char (point-min))
    (while (re-search-forward crs--html-placeholder-regexp nil t)
      (let* ((prefix-encoded (match-string 1))
             (prefix (when prefix-encoded
                       (decode-coding-string (base64-decode-string prefix-encoded) 'utf-8)))
             (html-encoded (match-string 2))
             (html-string (decode-coding-string (base64-decode-string html-encoded) 'utf-8))
             (start (match-beginning 0)))
        (delete-region (match-beginning 0) (match-end 0))
        (goto-char start)
        (crs--insert-html html-string prefix)))))

(defun crs--append-right-aligned (line indicator min-column)
  "Append INDICATOR to LINE, right-aligned at MIN-COLUMN or further right.
If LINE extends past MIN-COLUMN, place indicator one space after LINE ends."
  (let* ((line-length (length line))
         (indicator-length (length indicator))
         (target-column (max min-column (1+ line-length)))
         (padding (- target-column line-length)))
    (concat line (make-string padding ?\s) indicator)))

(defun crs--simplify-diff-headers ()
  "Replace standard git diff headers with a simplified 'modified filename' format.
This must be called after delta-wash."
  (save-excursion
    (goto-char (point-min))
    (while (re-search-forward "^diff --git a/\\(.*?\\) b/\\(.*?\\)$" nil t)
      (let* ((file-a (match-string 1))
             (file-b (match-string 2))
             ;; Prefer the b/ side unless it's /dev/null (deleted file)
             (filename (if (string= file-b "dev/null") file-a file-b))
             (start (match-beginning 0))
             (type "modified")
             (limit (save-excursion
                      (if (re-search-forward "^diff --git" nil t)
                          (match-beginning 0)
                        (point-max)))))

        (save-excursion
          (cond
           ((string= file-a "dev/null") (setq type "new file"))
           ((string= file-b "dev/null") (setq type "deleted"))
           (t
            (goto-char start)
            (if (re-search-forward "^new file mode" limit t)
                (setq type "new file")
              (goto-char start)
              (if (re-search-forward "^deleted file mode" limit t)
                  (setq type "deleted")
                (goto-char start)
                (if (re-search-forward "^@@ -0,0" limit t)
                    (setq type "new file")
                  (when (not (string= file-a file-b))
                    (setq type "renamed"))))))))

        (let ((end-marker-pos
               (save-excursion
                 (goto-char start)
                 (cond
                  ((re-search-forward "^\\+\\+\\+ .*\n" limit t) (point))
                  ((re-search-forward "^Binary files .*\n" limit t) (point))
                  ((re-search-forward "^@@ .*\n" limit t) (match-beginning 0))
                  (t nil)))))

          (when (and (not end-marker-pos)
                     (or (string= type "new file") (string= type "deleted")
                         (string= type "renamed")))
            (setq end-marker-pos limit))

          (if end-marker-pos
              (progn
                (delete-region start end-marker-pos)
                (let ((pad (cond ((string= type "modified") "     ")
                                 ((string= type "new file") "     ")
                                 ((string= type "deleted")  "      ")
                                 ((string= type "renamed")  "      ")
                                 (t "     "))))
                  (insert (format "%s%s%s\n" type pad filename))))
            (message "Could not find end of header for %s" filename)))))))

(defun crs--get-current-review-info ()
  "Extract (owner repo number) from the current buffer name.
Returns a list (owner repo number) or signals an error if not in a review buffer."
  (let ((name (buffer-name)))
    (if (string-match "\\* Review \\([^/]+\\)/\\([^[:space:]]+\\) #\\([0-9]+\\) .*\\*" name)
        (list (match-string 1 name)
              (match-string 2 name)
              (string-to-number (match-string 3 name)))
      (error "Not in a valid review buffer: %s" name))))

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
  "Fetch and checkout the head branch of the PR in the current review buffer."
  (interactive)
  (if-let ((ref-name (crs--get-ref-name)))
      (progn
        (message "Checking out: %s" ref-name)
        (crs--switch-and-fetch (projectile-project-name) ref-name))
    (message "Warning: Could not find branch name in Refs line")))

(provide 'crs-utils)

;;; crs-utils.el ends here
