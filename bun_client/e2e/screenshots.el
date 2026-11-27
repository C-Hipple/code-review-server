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

;; Colour the diff the way a delta washer (`delta-wash') would.
(add-hook 'my-code-review-mode-hook
          (lambda ()
            (font-lock-add-keywords nil '(("^\\+.*$" 0 'diff-added prepend)
                                          ("^-.*$" 0 'diff-removed prepend)
                                          ("^@@.*$" 0 'diff-hunk-header prepend)))
            (font-lock-flush)))

(defconst crs-shot-review "* CRS: #315 - code-review-server - acme *")

(defun crs-shot (name)
  "Redisplay, then save the screen as NAME.png in `crs-shot-dir'."
  (redisplay t)
  (call-process "import" nil nil nil "-window" "root"
                (expand-file-name (concat name ".png") crs-shot-dir))
  (message "took %s" name))

(defun crs-shot-review-only ()
  "Show only the review buffer, its first file's diff at the top."
  (switch-to-buffer crs-shot-review)
  (delete-other-windows)
  (goto-char (point-min))
  (re-search-forward "^modified")
  (recenter 1)
  (re-search-forward "^\\+\\s-*if pr.GetState")
  (beginning-of-line))

(defun crs-shot-select-chat ()
  "Select the chat buffer's window."
  (select-window
   (get-buffer-window
    (seq-find (lambda (b) (eq (buffer-local-value 'major-mode b) 'crs-chat-mode))
              (buffer-list)))))

(defvar crs-shot-steps
  (list
   (cons 0 (lambda () (crs-get-review "acme" "code-review-server" 315)))
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
   ;; Plugin output.
   (cons 0 (lambda ()
             (crs-shot-review-only)
             (crs-toggle-comments)
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
