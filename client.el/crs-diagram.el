;;; crs-diagram.el --- The change diagram of a PR, in a mermaid-mode buffer. -*- lexical-binding: t; -*-

;;; Commentary:

;; The server's change-diagram AI feature draws what a PR changes as a
;; Mermaid flowchart, and serves the diagram's raw Mermaid source as its
;; report.  `crs-show-change-diagram' shows that source for a PR in a
;; buffer of its own, in `mermaid-mode' when it is installed, so
;; mermaid-mode's commands can preview it: C-c C-b renders the buffer with
;; mmdc, C-c C-o opens it in the Mermaid live editor.
;;
;; The buffer is an AI output buffer like crs-ai.el's, and shares its
;; machinery: opening it asks for a run when the diagram was never drawn
;; for the PR or the PR has changed since, it polls while the run is
;; pending, and `r', `R' and `q' refresh, re-run and quit.  Unlike a report
;; buffer, it holds nothing but the diagram, so mermaid-mode sees valid
;; Mermaid: the status goes in the header line, and when there is no
;; diagram, a Mermaid comment says why.
;;
;; mermaid-mode is optional.  Without it the buffer is in
;; `fundamental-mode', and still shows the source.

;;; Code:

(require 'crs-vars)
(require 'crs-ai)
(require 'mermaid-mode nil t)

(declare-function mermaid-mode "mermaid-mode")
(declare-function crs--get-current-review-info "crs-review")

(defun crs--diagram-buffer-name (owner repo number)
  "Name of the buffer showing the change diagram of OWNER/REPO #NUMBER."
  (format "* Diagram: %s/%s #%d *" owner repo number))

(defun crs--diagram-source (output)
  "The Mermaid source in OUTPUT's report, or nil when it holds none.
OUTPUT is the change-diagram entry of a GetAIOutput reply."
  (let ((source (cdr (assq 'mermaid (cdr (assq 'report output))))))
    (when (and (stringp source) (not (string-empty-p (string-trim source))))
      source)))

(defun crs--diagram-comment (text)
  "TEXT as Mermaid comment lines, each ending in a newline."
  (mapconcat (lambda (line)
               (if (string-empty-p (string-trim line)) "%%\n" (concat "%% " line "\n")))
             (split-string (string-trim text) "\n")
             ""))

(defun crs--diagram-header (output)
  "The header line for the diagram buffer showing OUTPUT."
  (let* ((status (or (cdr (assq 'status output)) "unknown"))
         (updated (cdr (assq 'updated_at output)))
         (pending (crs--ai-pending-p output))
         (parts (delq nil
                      (list
                       (format "Change diagram: %s/%s #%s"
                               crs--ai-owner crs--ai-repo crs--ai-number)
                       (format "Status: %s" status)
                       (when (and updated (not (string-empty-p updated)))
                         (format "Updated %s" updated))
                       (when (and pending (crs--diagram-source output))
                         "Refreshing: this is the previous diagram")
                       (when (and (crs--ai-true-p (cdr (assq 'stale output))) (not pending))
                         "The PR has changed since: R re-runs")
                       (when (crs--ai-true-p (cdr (assq 'truncated output)))
                         "Input was cut to fit the prompt")))))
    ;; `%' is a construct in a mode line; the parts are plain text.
    (replace-regexp-in-string "%" "%%" (mapconcat #'identity parts "  ·  ") t t)))

(defun crs--insert-diagram (output)
  "Insert OUTPUT, the change-diagram entry of a GetAIOutput reply, at point.
Inserts the diagram's Mermaid source alone, or, when there is none, a
Mermaid comment saying why; the status goes in the header line."
  (let* ((source (crs--diagram-source output))
         (status (cdr (assq 'status output)))
         (body (cdr (assq 'body_content (cdr (assq 'body output))))))
    (setq header-line-format (crs--diagram-header output))
    (cond
     (source (insert source)
             (unless (string-suffix-p "\n" source) (insert "\n")))
     ((crs--ai-pending-p output)
      (insert "%% Drawing the diagram... this buffer refreshes when it lands.\n"))
     ((equal status "not-run")
      (insert "%% No diagram for this PR yet.  Press R to draw one.\n"))
     ((and body (not (string-empty-p body)))
      (insert (crs--diagram-comment body)))
     (t (insert "%% No diagram.\n")))))

(defvar-keymap crs-diagram-view-mode-map
  :doc "Keymap for `crs-diagram-view-mode'.  Bindings are defined in crs-client.el.")

(define-minor-mode crs-diagram-view-mode
  "Minor mode for the buffer showing a PR's change diagram.
The buffer is read-only and holds the diagram's Mermaid source, in
`mermaid-mode' when it is installed.
\\{crs-diagram-view-mode-map}"
  :lighter " CRS-Diagram"
  :keymap crs-diagram-view-mode-map)

(defun crs--diagram-open (owner repo number)
  "Show the change-diagram buffer of OWNER/REPO #NUMBER and return it."
  (let ((buffer (get-buffer-create (crs--diagram-buffer-name owner repo number))))
    (with-current-buffer buffer
      ;; Entering a major mode resets the buffer's local state, so only a
      ;; new buffer gets one.
      (unless crs-diagram-view-mode
        (if (fboundp 'mermaid-mode)
            (mermaid-mode)
          (fundamental-mode)
          (message "Install mermaid-mode to highlight and preview the diagram."))
        (setq buffer-read-only t)
        (crs-diagram-view-mode 1)
        (add-hook 'kill-buffer-hook #'crs--ai-cancel-poll nil t))
      (setq crs--ai-owner owner
            crs--ai-repo repo
            crs--ai-number number
            crs--ai-feature `((id . ,crs-change-diagram-feature-id)
                              (name . "Change diagram"))
            crs--ai-insert-function #'crs--insert-diagram)
      (when (= (buffer-size) 0)
        (let ((inhibit-read-only t))
          (insert "%% Loading the change diagram...\n"))))
    (pop-to-buffer buffer)
    buffer))

(defun crs--parse-pr-ref (ref)
  "Parse REF, a GitHub PR URL or OWNER/REPO#NUMBER, into (OWNER REPO NUMBER).
Returns nil when REF is neither."
  (when (and ref
             (or (string-match "github\\.com/\\([^/[:space:]]+\\)/\\([^/[:space:]]+\\)/pull/\\([0-9]+\\)" ref)
                 (string-match "\\`[[:space:]]*\\([^/[:space:]]+\\)/\\([^#[:space:]]+\\)#\\([0-9]+\\)[[:space:]]*\\'" ref)))
    (list (match-string 1 ref) (match-string 2 ref)
          (string-to-number (match-string 3 ref)))))

(defun crs--diagram-read-pr ()
  "The PR to show a change diagram for, as (OWNER REPO NUMBER).
The PR of the current AI or review buffer, else the PR whose URL is on
the current line (as in the reviews list), else one read from the
minibuffer."
  (or (and crs--ai-owner crs--ai-repo crs--ai-number
           (list crs--ai-owner crs--ai-repo crs--ai-number))
      (ignore-errors (crs--get-current-review-info))
      (crs--parse-pr-ref (buffer-substring-no-properties
                          (line-beginning-position) (line-end-position)))
      (let ((ref (read-string "PR (URL or owner/repo#number): ")))
        (or (crs--parse-pr-ref ref)
            (user-error "Not a PR URL or owner/repo#number: %s" ref)))))

;;;###autoload
(defun crs-show-change-diagram (owner repo number)
  "Show the change diagram of PR OWNER/REPO #NUMBER in a mermaid-mode buffer.
The diagram is the server's change-diagram AI feature: a Mermaid
flowchart of what the PR adds, changes and removes, shown as raw
Mermaid source.  Interactively, the PR is the current review buffer's,
or the one on the current line of the reviews list; anywhere else it is
read from the minibuffer.

Draws the diagram when it was never drawn for the PR or the PR has
changed since, and keeps the buffer updated while that runs.  In the
buffer, \\<crs-diagram-view-mode-map>\\[crs-ai-refresh] refreshes, \\[crs-ai-rerun] draws it afresh and \\[crs-quit-ai-output] quits."
  (interactive (crs--diagram-read-pr))
  (crs--ai-fetch (crs--diagram-open owner repo number) t))

(provide 'crs-diagram)
;;; crs-diagram.el ends here
