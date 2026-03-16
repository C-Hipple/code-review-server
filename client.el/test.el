(crs-start-server)
(crs-restart-server)
(crs-kill-reviews-and-restart)
(crs-shutdown-server)

;; If I want comments
(setq crs-include-comments-tree 't)

;; https://github.com/C-Hipple/code-review-server/pull/83
(crs-get-review "C-Hipple" "gtdbot" 9)
(crs-get-review "C-Hipple" "gtdbot" 11)
(crs-get-review "C-Hipple" "diff-lsp" 6)
(crs-get-review "C-Hipple" "diff-lsp" 15)
(crs-get-review "C-Hipple" "diff-lsp" 16)
(crs-get-review "C-Hipple" "diff-lsp" 16)
(crs-get-review "C-Hipple" "code-review-server" 31)
(crs-get-review "C-Hipple" "code-review-server" 83)  ;; This one is the "renamed, deleted, renamed with changes test one"
(crs-get-review "IAmTomShaw" "f1-race-replay" 18)
https://github.com/IAmTomShaw/f1-race-replay/pull/18
(crs-get-reviews)
(crs-list-plugins)

https://github.com/C-Hipple/code-review-server/pull/83 ;; the debug PR
https://github.com/C-Hipple/diff-lsp/pull/5
http://localhost:5172/?owner=C-Hipple&repo=code-review-server&number=83  ;; For opening in bun_client

(crs-shutdown-server)


(defun  hurr()
  (interactive)
  (let (
        (line-content (buffer-substring-no-properties (line-beginning-position) (line-end-position)))
        )
    (message line-content)))

(defun crs-kill-reviews-and-restart ()
  "Close all open review buffers and restart the server."
  (interactive)
  (dolist (buffer (buffer-list))
    (when (string-match-p "^\\* Review" (buffer-name buffer))
      (kill-buffer buffer)))
  (crs-restart-server))
