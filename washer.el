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

(defface washer-outdated-face
  '((t (:foreground "#ffaf00" :weight bold)))
  "Face for outdated markers."
  :group 'my-custom-highlights)

(defface washer-file-header-face
  '((t (:foreground "#268bd2" :weight bold :height 1.3)))
  "Face for file headers."
  :group 'my-custom-highlights)

(defface washer-summary-face
  '((t (:foreground "#b58900" :weight bold :height 1.3)))
  "Face for PR summary items."
  :group 'my-custom-highlights)

(defface washer-section-heading-face
  '((t (:foreground "#cb4b16" :weight bold :height 1.3)))
  "Face for major section headings."
  :group 'my-custom-highlights)

(defface washer-compact-comment-face
  '((t (:foreground "#859900" :weight bold)))
  "Face for compact comment indicators <C: username>."
  :group 'my-custom-highlights)

(defun highlight-review-comments ()
  "Highlight review comment blocks and file headers."
  (interactive)
  (font-lock-add-keywords nil
                          '(
                            (".*[┌└][^[]*" 0 'washer-review-comment-seperator-face t)
                            ;; TODO: fix
                            ("\\[OUTDATED\\]" 0 'washer-outdated-face t)
                            ("\\[OUTDATED\\]\\([─ ]*\\)$" 1 'washer-review-comment-seperator-face t)
                            ("^    │ .*" 0 'washer-review-comment-face t)
                            ("^\\(modified\\|deleted\\|new file\\).*" 0 'washer-file-header-face t)
                            ("^\\(Title\\|Project\\|Author\\|State\\|Reviewers\\|Refs\\|URL\\|Milestone\\|Labels\\|Projects\\|Draft\\|Assignees\\|Suggested-Reviewers\\):.*" 0 'washer-summary-face t)
                            ("^\\(Description\\|Your Review Feedback\\|Conversation\\|Commits .*\\|Files changed .*\\)$" 0 'washer-section-heading-face t)
                            ("<C: [^>]+>" 0 'washer-compact-comment-face t)))
  (goto-address-mode 1)
  (font-lock-flush))

;;;###autoload
(defun delta-wash()
  "interactive of the call to magit delta function if you have magit-delta or code-review installed."
  (interactive)
  (cond ((fboundp 'magit-delta-call-delta-and-convert-ansi-escape-sequences)
         (magit-delta-call-delta-and-convert-ansi-escape-sequences))
        ((fboundp 'code-review-delta-call-delta)
         (code-review-delta-call-delta))
        (t
         (message "You do not have a delta washer installed!"))))

(provide 'washer)
