;;; screenshots.el --- Take the Emacs client screenshots in docs/img. -*- lexical-binding: t; -*-

;;; Commentary:

;; Loaded by screenshots_emacs.sh into a graphical `emacs -Q' under Xvfb,
;; with a `crs' on PATH that runs the fake backend over the demo fixtures
;; (e2e/fixtures/demo.ts).  Each step opens a view of PR #315, then grabs the
;; screen with ImageMagick's `import'; the last one exits Emacs.

;;; Code:

(defvar crs-shot-dir (getenv "CRS_SHOT_DIR"))
(defvar crs-shot-client (getenv "CRS_SHOT_CLIENT"))

(setq inhibit-startup-screen t)
(dolist (dir (file-expand-wildcards "/usr/share/emacs/site-lisp/elpa/markdown-mode*"))
  (add-to-list 'load-path dir))
(when (require 'package nil t) (package-initialize))
(add-to-list 'load-path crs-shot-client)
(load-theme 'modus-vivendi t)
(set-face-attribute 'default nil :family "DejaVu Sans Mono" :height 100)
(menu-bar-mode -1)
(tool-bar-mode -1)
(scroll-bar-mode -1)
(blink-cursor-mode -1)
(require 'crs-client)
(setq frame-resize-pixelwise t)
(set-frame-size (selected-frame) 1600 1000 t)

;; Wash the diff through git-delta, as the renderer's `delta-wash' does with
;; magit-delta installed.  This stands in for magit-delta's washer (from MELPA) with the
;; same two steps: run the buffer through `delta', then turn its colour
;; escapes into overlays (`ansi-color' here, `xterm-color' there).  Line
;; numbers and kept +/- markers give the gutter crs-render.el parses.
(require 'ansi-color)
(defun magit-delta-call-delta-and-convert-ansi-escape-sequences ()
  "Wash the current buffer through git-delta."
  (let ((inhibit-read-only t))
    (call-process-region (point-min) (point-max) "delta" t t nil
                         "--dark" "--line-numbers" "--keep-plus-minus-markers"
                         ;; Raw headers: `crs--simplify-diff-headers' rewrites
                         ;; `diff --git' lines into "modified <file>".
                         "--file-style" "raw" "--file-decoration-style" "none"
                         "--hunk-header-style" "raw" "--hunk-header-decoration-style" "none"
                         "--max-line-distance" "0.6"
                         "--true-color" "always" "--paging" "never")
    (ansi-color-apply-on-region (point-min) (point-max))))

(defconst crs-shot-review "* CRS: #315 - code-review-server - acme *")

(defun crs-shot (name &optional crop)
  "Redisplay, then save the screen as NAME.png in `crs-shot-dir'.
CROP, an ImageMagick geometry such as \"1600x400+0+0\", keeps only that part."
  (redisplay t)
  (apply #'call-process "import" nil nil nil "-window" "root"
         (append (and crop (list "-crop" crop))
                 (list (expand-file-name (concat name ".png") crs-shot-dir))))
  (message "took %s" name))

(defun crs-shot-review-only ()
  "Show only the review buffer, its first file's diff at the top."
  (switch-to-buffer crs-shot-review)
  (delete-other-windows)
  (goto-char (point-min))
  (re-search-forward "^modified")
  (recenter 1)
  (re-search-forward "│\\+\\s-*if pr.GetState")
  (beginning-of-line))

(defun crs-shot-select-chat ()
  "Select the chat buffer's window."
  (select-window
   (get-buffer-window
    (seq-find (lambda (b) (eq (buffer-local-value 'major-mode b) 'crs-chat-mode))
              (buffer-list)))))

(defvar crs-shot-steps
  (list
   ;; The review list, Needs Review and its first PR unfolded.
   (cons 0 (lambda () (crs-get-reviews)))
   (cons 3 (lambda ()
             (switch-to-buffer crs-reviews-buffer-name)
             (delete-other-windows)
             (goto-char (point-min))
             (re-search-forward "^\\* TODO Needs Review")
             (crs-list-toggle-heading)
             (re-search-forward "^\\*\\* TODO Report a merged")
             (beginning-of-line)
             (crs-list-toggle-heading)
             (goto-char (point-min))))
   (cons 1 (lambda () (crs-shot "emacs-list" "1600x380+0+0")))
   (cons 0 (lambda () (crs-get-review "acme" "code-review-server" 315)))
   ;; The top of the review: the PR's header and description, the
   ;; conversation folded.
   (cons 3 (lambda ()
             (switch-to-buffer crs-shot-review)
             (delete-other-windows)
             (goto-char (point-min))
             (recenter 0)
             (crs-shot "emacs-review-header")))
   ;; The review, with its discussion beside it.
   (cons 3 (lambda ()
             (crs-shot-review-only)
             (crs-show-discussion)))
   (cons 1 (lambda () (crs-shot "emacs-review")))
   ;; Comment threads drawn inline in the diff.
   (cons 0 (lambda ()
             (crs-shot-review-only)
             (crs-toggle-comments)))
   (cons 1 (lambda ()
             (crs-shot-review-only)
             (forward-line -8)
             (recenter 1)
             (crs-shot "emacs-review-comments")))
   ;; Plugin annotations drawn in the diff.
   (cons 0 (lambda ()
             (crs-shot-review-only)
             (crs-toggle-comments)))
   (cons 1 (lambda ()
             (crs-shot-review-only)
             (crs-toggle-annotations)))
   (cons 1 (lambda ()
             (crs-shot-review-only)
             (forward-line -8)
             (recenter 1)
             (crs-shot "emacs-annotations")))
   ;; Plugin output.
   (cons 0 (lambda ()
             (crs-shot-review-only)
             (crs-toggle-annotations)
             (crs-get-plugin-output)))
   (cons 2 (lambda () (crs-shot "emacs-plugins")))
   ;; An AI feature's report.
   (cons 0 (lambda () (crs-ai-list-features)))
   (cons 1 (lambda ()
             (crs-shot-review-only)
             ;; What `crs-get-ai-output' does once comments-addressed is picked.
             (crs--ai-fetch
              (crs--ai-feature-buffer
               "acme" "code-review-server" 315
               (seq-find (lambda (f) (equal (cdr (assq 'id f)) "comments-addressed"))
                         crs-ai-features))
              t)))
   (cons 4 (lambda () (crs-shot "emacs-ai")))
   ;; The review chat, after one question.
   (cons 0 (lambda ()
             (crs-shot-review-only)
             (setq crs-chat-window-height 0.55)
             (crs-chat "acme" "code-review-server" 315)))
   (cons 2 (lambda ()
             (crs-shot-select-chat)
             (goto-char (point-max))
             (insert "Is anything relying on the state being \"closed\" for merged PRs?")
             (crs-chat-send)))
   (cons 4 (lambda ()
             (crs-shot-select-chat)
             (goto-char (point-min))
             (recenter 0)
             (crs-shot "emacs-chat")))
   ;; A second session, then the PR's sessions listed.
   (cons 0 (lambda ()
             (crs-shot-select-chat)
             (crs-chat-new)))
   (cons 1 (lambda ()
             (crs-shot-select-chat)
             (goto-char (point-max))
             (insert "Does TestPRState cover a closed PR with neither field set?")
             (crs-chat-send)))
   (cons 4 (lambda ()
             (crs-shot-select-chat)
             (call-interactively #'crs-chat-sessions)))
   (cons 2 (lambda () (crs-shot "emacs-chat-sessions")))
   ;; More of the file below a hunk (`o').
   (cons 0 (lambda ()
             (crs-shot-review-only)
             (re-search-backward "^@@ -636")
             (forward-line 4)
             (crs-expand-hunk-after)))
   (cons 2 (lambda ()
             (switch-to-buffer crs-shot-review)
             (delete-other-windows)
             (goto-char (point-min))
             (re-search-forward "^@@ -636")
             (recenter 1)
             (crs-shot "emacs-hunk-expansion")))
   ;; The change diagram's Mermaid source.
   (cons 0 (lambda ()
             (crs-shot-review-only)
             (crs-show-change-diagram "acme" "code-review-server" 315)))
   (cons 4 (lambda () (crs-shot "emacs-change-diagram")))
   (cons 0 (lambda () (kill-emacs 0))))
  "Each step: seconds to wait before it, and what it does.")

(defun crs-shot-run (steps)
  "Run STEPS one after another, each after its delay."
  (when steps
    (run-at-time (car (car steps)) nil
                 (lambda ()
                   (condition-case err
                       (funcall (cdr (car steps)))
                     (error
                      ;; Xvfb's Emacs has no terminal to print to.
                      (append-to-file (format "screenshot step failed: %S\n" err) nil
                                      (expand-file-name "screenshots-error.log" crs-shot-dir))
                      (kill-emacs 1)))
                   (crs-shot-run (cdr steps))))))

(crs-shot-run crs-shot-steps)

;;; screenshots.el ends here
