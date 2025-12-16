(codereviewserver-client-start-server)
(codereviewserver-client-restart-server)
(codereviewserver-client-get-review "C-Hipple" "gtdbot" 9)
(codereviewserver-client-get-review "C-Hipple" "gtdbot" 10)
(codereviewserver-client-get-review "C-Hipple" "diff-lsp" 6)
(codereviewserver-client-get-review "IAmTomShaw" "f1-race-replay" 18)
https://github.com/IAmTomShaw/f1-race-replay/pull/18
(highlight-at-at-lines-blue)


(defun  hurr()
  (interactive)
  (let (
        (line-content (buffer-substring-no-properties (line-beginning-position) (line-end-position)))
        )
    (message line-content)))
