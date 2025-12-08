(defgroup my-custom-highlights nil
  "Custom highlighting configurations."
  :group 'faces)

;; 1. Define a custom face (color/style)
(defface my-blue-line-face
  '((t (:foreground "white" :background "blue" :weight bold)))
  "Face for highlighting lines starting with @@."
  :group 'my-custom-highlights)

;; 2. Define the function to apply the highlighting
(defun highlight-at-at-lines-blue ()
  "Highlight all lines starting with '@@' in blue for the current buffer."
  (interactive)
  (font-lock-add-keywords nil
                          '(("^@@.*" 0 'magit-diff-hunk-heading-highlight t)))
  ;; Or if I actually want the blue line one
  ;; '(("^@@.*" 0 'my-blue-line-face t)))
  ;; Refresh the highlighting to see changes immediately
  (font-lock-flush))

(defun unhighlight-at-at-lines ()
  "Remove the highlighting for '@@' lines in the current buffer."
  (interactive)
  ;; would need to change this
  (font-lock-remove-keywords nil
                             '(("^@@.*" 0 'my-blue-line-face t)))
  (font-lock-flush))
