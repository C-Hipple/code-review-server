;;; crs-html.el --- HTML/shr rendering helpers for the crs client. -*- lexical-binding: t; -*-

;;; Commentary:

;; HTML/shr rendering helpers for the crs client.

;;; Code:

(require 'seq)
(require 'shr)
(require 'crs-vars)

(defconst crs--image-tag-regexp "<\\(?:img\\|source\\)\\b[^>]*>"
  "Matches the image tags GitHub embeds in a body.
A pasted screenshot arrives as a raw <img> tag — that is what GitHub's
upload widget writes — rather than as Markdown image syntax.")

(defconst crs--img-src-regexp "\\(<img[^>]*\\bsrc=\\)\\([\"']\\)\\([^\"']*\\)\\2"
  "Matches the src attribute of an <img> tag, capturing the URL in group 3.")

(defun crs--image-cache-path (url)
  "Return the local file the server cached URL at, or nil if there isn't one.
Reads `crs--buffer-images', which the review buffer fills from the
GetPR reply."
  (let ((wanted (replace-regexp-in-string "&amp;" "&" url t t)))
    (seq-some (lambda (image)
                (let ((image-url (cdr (assq 'url image)))
                      (path (cdr (assq 'path image))))
                  (when (and (stringp image-url) (stringp path)
                             (string= image-url wanted)
                             (not (string-empty-p path))
                             (file-readable-p path))
                    path)))
              (or crs--buffer-images []))))

(defun crs--localize-image-srcs (html)
  "Point each <img> in HTML at the copy of the image the server cached.
An attachment on a private repository is served only to a request
carrying a GitHub token, which shr has no way to send — so without this
the screenshot in a review comment cannot render at all.  Images the
server did not cache are left alone for shr to fetch itself."
  (if (seq-empty-p (or crs--buffer-images []))
      html
    (replace-regexp-in-string
     crs--img-src-regexp
     (lambda (match)
       (let ((path (crs--image-cache-path (match-string 3 match))))
         (if path
             (concat (match-string 1 match) (match-string 2 match)
                     "file://" path (match-string 2 match))
           match)))
     html t t)))

(defun crs--ensure-html (text)
  "Return TEXT as HTML suitable for shr rendering.
If TEXT already looks like HTML (starts with a tag), bare newlines are
replaced with <br> elements so shr preserves the visual line structure.
Otherwise convert plain text/Markdown line breaks to HTML paragraphs and
br elements so that shr preserves the visual line structure.

Image tags survive that escaping.  A body is mostly prose, so it takes
the escaping branch, and escaping a screenshot's <img> tag would render
the tag itself as text instead of the picture."
  (crs--localize-image-srcs
   (if (string-match-p "\\`[[:space:]]*<" text)
       (replace-regexp-in-string "\n" "<br>" text)
     (let* ((tags nil)
            (protected (replace-regexp-in-string
                        crs--image-tag-regexp
                        (lambda (tag)
                          (push tag tags)
                          (format "\0crs-image-%d\0" (1- (length tags))))
                        text t t))
            (escaped (replace-regexp-in-string "&" "&amp;" protected))
            (escaped (replace-regexp-in-string "<" "&lt;" escaped))
            (escaped (replace-regexp-in-string ">" "&gt;" escaped))
            (with-paras (replace-regexp-in-string "\n\n+" "</p><p>" escaped))
            (with-brs (replace-regexp-in-string "\n" "<br>" with-paras))
            (restored (replace-regexp-in-string
                       "\0crs-image-\\([0-9]+\\)\0"
                       (lambda (marker)
                         (nth (- (length tags) 1
                                 (string-to-number (match-string 1 marker)))
                              tags))
                       with-brs t t)))
       (concat "<p>" restored "</p>")))))

(defun crs--shr-render (html-string start)
  "Render HTML-STRING with shr, starting from buffer position START.
On timeout or error, remove any partial output and insert HTML-STRING as plain text."
  (let ((do-render
         (lambda ()
           (condition-case err
               (let ((dom (libxml-parse-html-region start (point))))
                 (delete-region start (point))
                 (shr-insert-document dom))
             (error
              (message "crs: shr rendering error: %s" (error-message-string err))
              (delete-region start (point))
              (insert html-string))))))
    (if crs-shr-render-timeout
        (with-timeout (crs-shr-render-timeout
                       (message "crs: shr rendering timed out, falling back to plain text")
                       (delete-region start (point))
                       (insert html-string))
          (funcall do-render))
      (funcall do-render))))

(defun crs--insert-html (html-string &optional prefix)
  "Insert HTML-STRING at point, rendering it with shr.
If PREFIX is provided, it is prepended to each line of the rendered content.
Images will be displayed inline if running in graphical Emacs.
Requires Emacs to be compiled with libxml support.
Falls back to plain text if rendering times out (see `crs-shr-render-timeout')."
  (if (and html-string (not (string-empty-p html-string)))
      (let ((start (point)))
        (insert (crs--ensure-html html-string))
        (when (fboundp 'libxml-parse-html-region)
          (crs--shr-render html-string start))
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

(defun crs--strip-comments-tree (content)
  "Remove *** Comments sub-trees from CONTENT org string.
The server always includes comment sub-trees in the rendered org output.
This strips them so they are not rendered in the list buffer,
which avoids the performance cost when `crs-include-comments-tree' is nil."
  (when content
    (let ((lines (split-string content "\n"))
          (result '())
          (in-comments nil))
      (dolist (line lines)
        (cond
         ;; Entering a *** Comments sub-tree — skip it
         ((string-match-p "^\\*\\*\\* Comments" line)
          (setq in-comments t))
         ;; Back to a ** item or * section heading — resume keeping lines
         ((and in-comments (string-match-p "^\\*\\{1,2\\}[^*]" line))
          (setq in-comments nil)
          (push line result))
         ((not in-comments)
          (push line result))))
      (string-join (nreverse result) "\n"))))

(provide 'crs-html)
;;; crs-html.el ends here
