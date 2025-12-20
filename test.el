(crs-start-server)
(crs-restart-server)
(crs-get-review "C-Hipple" "gtdbot" 9)
(crs-get-review "C-Hipple" "gtdbot" 10)
(crs-get-review "C-Hipple" "diff-lsp" 6)
(crs-get-review "IAmTomShaw" "f1-race-replay" 18)
https://github.com/IAmTomShaw/f1-race-replay/pull/18
(crs-get-reviews)
(highlight-at-at-lines-blue)


(defun  hurr()
  (interactive)
  (let (
        (line-content (buffer-substring-no-properties (line-beginning-position) (line-end-position)))
        )
    (message line-content)))
