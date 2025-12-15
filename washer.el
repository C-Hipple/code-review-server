(defgroup my-custom-highlights nil
  "Custom highlighting configurations."
  :group 'faces)


;; 2. Define the function to apply the highlighting
(defun highlight-at-at-lines-blue ()
  "Highlight all lines starting with '@@' in blue for the current buffer."
  (interactive)
  (font-lock-add-keywords nil
                          '(("^@@.*" 0 'magit-diff-hunk-heading-highlight t)))
  (font-lock-flush))


(defface washer-review-comment-seperator-face
  '((t (:foreground "red" :weight bold)))
  "Face for review comments."
  :group 'my-custom-highlights)

(defface washer-review-comment-face
  '((t (:foreground "#d7af5f" :weight bold)))
  "Face for review comments."
  :group 'my-custom-highlights)

(defun highlight-review-comments ()
  "Highlight review comment blocks."
  (interactive)
  (font-lock-add-keywords nil
                          '((".*[┌└].*" 0 'washer-review-comment-seperator-face t)
                            ("^    │\ .*" 0 'washer-review-comment-comment-face t)))
  (font-lock-flush))

(provide 'washer)
