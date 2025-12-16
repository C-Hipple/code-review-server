(codereviewserver-client-start-server)
(codereviewserver-client-restart-server)
(codereviewserver-client-get-review "C-Hipple" "gtdbot" 9)
(highlight-at-at-lines-blue)


(defun  hurr()
  (interactive)
  (let (
        (line-content (buffer-substring-no-properties (line-beginning-position) (line-end-position)))
        )
    (message line-content)))
