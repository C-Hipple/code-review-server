;;; crs-discussion.el --- A PR's discussion in a buffer beside its code. -*- lexical-binding: t; -*-

;;; Commentary:

;; A review buffer opens on the code: its conversation folded and the inline
;; comment threads drawn as compact <C: author> indicators
;; (`crs-review-collapsed-sections', `crs-review-show-comments').  The
;; discussion is read beside it instead: `crs-show-discussion' (s in a review
;; buffer) opens the PR's discussion in a buffer of its own, in a window to
;; the right of the review's.  It holds
;;
;;   - the PR's description;
;;   - the conversation: comments on the PR, reviews and commits, as the
;;     review buffer's Conversation section draws them;
;;   - every review thread, in the order the diff shows its line, under the
;;     file and line it's about and the diff row there; and
;;   - the outdated threads, whose lines the diff no longer shows.
;;
;; RET on a thread shows its line in the review buffer, and n / p step from
;; thread to thread, moving the review window to each one's line as they go.
;; The buffer is drawn from what the review buffer holds -- nothing is
;; fetched for it -- and drawn again each time the review buffer gets a fresh
;; payload (a sync, a comment added, a review submitted).  g syncs the PR.

;;; Code:

(require 'seq)
(require 'subr-x)
(require 'diff-mode)
(require 'pulse)
(require 'crs-vars)
(require 'crs-html)
(require 'crs-render)
(require 'crs-filtered-diff)

(declare-function crs--get-current-review-info "crs-review")
(declare-function crs--get-comment-context "crs-comments")
(declare-function crs--diff-row-at-position "crs-review")
(declare-function crs-sync-pr "crs-review")

(defvar-keymap crs-discussion-mode-map
  :doc "Keymap for `crs-discussion-mode'.  Bindings are defined in crs-client.el.")

(defconst crs--discussion-font-lock-keywords
  '(("^\\(Description\\|Conversation\\|Review threads .*\\|Outdated threads .*\\)$"
     0 'washer-section-heading-face t)
    ("^Commit by .*$" 0 'washer-commit-face t))
  "What font-lock draws in a discussion buffer.
The rest of it carries its faces as it's drawn.")

(define-derived-mode crs-discussion-mode special-mode "CRS Discussion"
  "Major mode for a PR's discussion, read beside its review buffer.
RET on a review thread shows its line in the review buffer, and \\[crs-discussion-next-thread] / \\[crs-discussion-previous-thread]
step between threads, the review window following.  TAB folds a section.

\\{crs-discussion-mode-map}"
  (add-to-invisibility-spec '(codereview-hide . t))
  (font-lock-add-keywords nil crs--discussion-font-lock-keywords)
  (goto-address-mode 1))

(defun crs--discussion-buffer-name (owner repo number)
  "Return the discussion buffer's name for OWNER/REPO PR NUMBER."
  (format "* CRS Discussion: #%d - %s - %s *" number repo owner))

;;; What the buffer shows

(defun crs--discussion-position (comment)
  "COMMENT's diff position as a number, or nil for one on the whole file.
The server sends it as a string; \"\" and \"0\" mean the file."
  (let ((pos (cdr (assq 'position comment))))
    (cond ((integerp pos) (unless (zerop pos) pos))
          ((and (stringp pos) (string-match-p "\\`[0-9]+\\'" pos))
           (let ((n (string-to-number pos)))
             (unless (zerop n) n))))))

(defun crs--diff-files (diff)
  "The files DIFF changes, in the order it shows them."
  (let ((files nil))
    (dolist (line (split-string (or diff "") "\n"))
      (when (string-match "^diff --git a/\\(.*?\\) b/\\(.*?\\)$" line)
        (push (if (string= (match-string 2 line) "dev/null")
                  (match-string 1 line)
                (match-string 2 line))
              files)))
    (nreverse files)))

(defun crs--discussion-threads (comments diff)
  "COMMENTS gathered into threads, in the order DIFF shows their lines.
A thread is the comments on one line, as the review buffer draws them
\(`crs--index-comments'); each is returned as (KEY . COMMENTS), KEY a
plist naming its `:file' and `:position' (nil on the whole file).  A
file's threads come in the order DIFF shows the file, the one on the
whole file first, and threads on files DIFF doesn't show last.  A comment
on no file is the PR's, in the conversation, and in no thread."
  (let* ((files (crs--diff-files diff))
         (on-files (seq-filter (lambda (comment)
                                 (let ((path (cdr (assq 'path comment))))
                                   (and (stringp path) (not (string-empty-p path)))))
                               (or comments [])))
         (threads nil))
    (maphash (lambda (_ thread)
               (let ((root (car thread)))
                 (push (cons (list :file (cdr (assq 'path root))
                                   :position (crs--discussion-position root))
                             thread)
                       threads)))
             (crs--index-comments on-files))
    (sort threads
          (lambda (a b)
            (let* ((file-a (plist-get (car a) :file))
                   (file-b (plist-get (car b) :file))
                   (rank-a (or (seq-position files file-a) (length files)))
                   (rank-b (or (seq-position files file-b) (length files))))
              (cond ((/= rank-a rank-b) (< rank-a rank-b))
                    ((not (equal file-a file-b)) (string< (or file-a "") (or file-b "")))
                    (t (< (or (plist-get (car a) :position) 0)
                          (or (plist-get (car b) :position) 0)))))))))

(defun crs--discussion-outdated-threads (outdated)
  "OUTDATED comments gathered into threads, each a list of comments.
A thread is the comments sharing a GitHub review thread (`thread_id'), or
else replying to one comment (`in_reply_to'); they come in the order the
server sent their first comments."
  (let ((groups nil)
        (index (make-hash-table :test 'equal)))
    (seq-do (lambda (comment)
              (let* ((thread (cdr (assq 'thread_id comment)))
                     (reply-to (cdr (assq 'in_reply_to comment)))
                     (key (cond ((and (stringp thread) (not (string-empty-p thread))) thread)
                                ((and (numberp reply-to) (> reply-to 0)) (format "%s" reply-to))
                                (t (format "%s" (cdr (assq 'id comment))))))
                     (group (gethash key index)))
                (if group
                    (setcdr group (cons comment (cdr group)))
                  (setq group (list key comment))
                  (puthash key group index)
                  (push group groups))))
            (or outdated []))
    (mapcar (lambda (group) (reverse (cdr group))) (nreverse groups))))

(defun crs--discussion-code-line (marker text)
  "TEXT, a diff row whose MARKER is ?+, ?- or ?\\s, as a thread shows it."
  (propertize (concat "    " text)
              'crs-body t
              'font-lock-face (pcase marker
                                (?+ 'diff-added)
                                (?- 'diff-removed)
                                (_ 'diff-context))))

(defun crs--discussion-location-line (key label notes)
  "The line opening the thread KEY names: LABEL, then NOTES after \" · \".
It carries KEY as its `crs-discussion-location' property."
  (propertize (string-join (cons label notes) " · ")
              'crs-discussion-location key
              'crs-body t
              'font-lock-face 'crs-discussion-location))

(defun crs--discussion-thread-text (thread diff)
  "THREAD, a `crs--discussion-threads' entry, as the buffer shows it.
DIFF is the PR's full diff, which its position counts rows of."
  (let* ((key (car thread))
         (comments (cdr thread))
         (file (plist-get key :file))
         (position (plist-get key :position))
         (row (and position (crs--diff-row-at-position diff file position)))
         (line (and row (or (plist-get row :head) (plist-get row :base))))
         (label (cond ((null position) file)
                      (line (format "%s:%d" file line))
                      (t (format "%s, position %d" file position))))
         (notes (delq nil (list (when (null position) "on the file")
                                (when (eq (cdr (assq 'resolved (car comments))) t)
                                  "resolved")))))
    (concat (crs--discussion-location-line key label notes) "\n"
            (when row
              (concat (crs--discussion-code-line (plist-get row :marker) (plist-get row :text))
                      "\n"))
            (crs--render-comment-tree comments))))

(defun crs--discussion-outdated-text (comments)
  "COMMENTS, an outdated thread, as the buffer shows it.
Under its file comes the line the thread was about, the last of the diff
context it was written against (`diff_hunk')."
  (let* ((root (car comments))
         (file (or (cdr (assq 'path root)) ""))
         (hunk (string-trim-right (or (cdr (assq 'diff_hunk root)) "")))
         (row (unless (string-empty-p hunk)
                (car (last (split-string hunk "\n")))))
         (notes (when (eq (cdr (assq 'resolved root)) t) (list "resolved"))))
    (concat (crs--discussion-location-line (list :file file :outdated t) file notes) "\n"
            (when (and row (not (string-prefix-p "@@" row)))
              (concat (crs--discussion-code-line (and (> (length row) 0) (aref row 0)) row)
                      "\n"))
            (crs--render-comment-tree comments))))

(defun crs--discussion-text (owner repo number metadata comments reviews outdated commits diff)
  "The discussion of OWNER/REPO #NUMBER, as the buffer shows it.
METADATA, COMMENTS, REVIEWS, OUTDATED and COMMITS are the review buffer's,
and DIFF is the PR's full diff.  Bodies are left as HTML placeholders,
for `crs--process-html-placeholders'."
  (let ((threads (crs--discussion-threads comments diff))
        (old (crs--discussion-outdated-threads outdated)))
    (concat
     (propertize (format "#%s: %s" number (or (cdr (assq 'title metadata)) ""))
                 'crs-body t 'font-lock-face 'bold)
     "\n"
     (propertize (string-join (delq nil (list (format "%s/%s" owner repo)
                                              (when-let ((author (cdr (assq 'author metadata))))
                                                (concat "@" author))
                                              (cdr (assq 'url metadata))))
                              " · ")
                 'crs-body t)
     "\n\nDescription\n"
     (crs--make-html-placeholder (cdr (assq 'body metadata)))
     "\n"
     (crs--render-conversation-from-data comments reviews outdated commits)
     (format "Review threads (%d)\n\n" (length threads))
     (if threads
         (mapconcat (lambda (thread) (crs--discussion-thread-text thread diff)) threads "\n")
       "No comments on the code.\n")
     (when old
       (concat (format "\nOutdated threads (%d)\n\n" (length old))
               (mapconcat #'crs--discussion-outdated-text old "\n"))))))

(defun crs--discussion-mark-threads ()
  "Give each thread's lines its key, as a `crs-discussion-thread' property.
A thread runs from its location line (`crs-discussion-location') to the
next one, or to the next section heading."
  (save-excursion
    (goto-char (point-min))
    (let ((key nil)
          (start nil))
      (while (not (eobp))
        (let ((here (get-text-property (point) 'crs-discussion-location)))
          (cond
           (here
            (when key
              (put-text-property start (point) 'crs-discussion-thread key))
            (setq key here
                  start (point)))
           ((and key (crs--section-header-line-p))
            (put-text-property start (point) 'crs-discussion-thread key)
            (setq key nil))))
        (forward-line 1))
      (when key
        (put-text-property start (point-max) 'crs-discussion-thread key)))))

(defun crs--discussion-diff ()
  "The PR's full diff, as the review buffer holds it.
Comment positions count its rows, whichever diff the buffer shows."
  (if crs--buffer-diff-filter
      (or crs--buffer-full-diff crs--buffer-diff)
    crs--buffer-diff))

(defun crs--render-discussion (buffer)
  "Draw the discussion BUFFER from what its review buffer holds."
  (with-current-buffer buffer
    (let ((review crs--discussion-review-buffer))
      (unless (buffer-live-p review)
        (user-error "The review buffer this discussion came from is gone"))
      (pcase-let* ((`(,owner ,repo ,number)
                    (with-current-buffer review (crs--get-current-review-info)))
                   (text (with-current-buffer review
                           (crs--discussion-text owner repo number
                                                 crs--buffer-metadata
                                                 crs--buffer-comments
                                                 crs--buffer-reviews
                                                 crs--buffer-outdated-comments
                                                 crs--buffer-commits
                                                 (crs--discussion-diff))))
                   (window (get-buffer-window buffer t))
                   (inhibit-read-only t))
        ;; Bodies are filled to the window they're shown in, and screenshots
        ;; point at the copies the server cached.
        (setq crs--buffer-images (buffer-local-value 'crs--buffer-images review))
        (remove-overlays (point-min) (point-max) 'codereview-hide t)
        (erase-buffer)
        (insert text)
        (if (window-live-p window)
            (with-selected-window window
              (crs--process-html-placeholders))
          (crs--process-html-placeholders))
        (crs--decorate-blocks)
        (crs--discussion-mark-threads)
        (crs--apply-collapsed-sections)
        (goto-char (point-min))))))

;;; Keeping the view through a redraw

(defun crs--discussion-anchor (pos)
  "Describe where POS is so `crs--discussion-anchor-position' finds it again.
The thread it's in and how many lines into it, or else its line."
  (save-excursion
    (goto-char pos)
    (let ((key (get-text-property (line-beginning-position) 'crs-discussion-thread)))
      (if key
          (let ((offset 0))
            (forward-line 0)
            (while (and (not (get-text-property (point) 'crs-discussion-location))
                        (zerop (forward-line -1)))
              (setq offset (1+ offset)))
            (list :thread key :offset offset))
        (list :line (line-number-at-pos pos))))))

(defun crs--discussion-thread-start (key)
  "Return where the thread KEY names opens, or nil if the buffer has none."
  (save-excursion
    (goto-char (point-min))
    (let ((found nil))
      (while (and (not found) (not (eobp)))
        (if (equal (get-text-property (point) 'crs-discussion-location) key)
            (setq found (point))
          (forward-line 1)))
      found)))

(defun crs--discussion-anchor-position (anchor)
  "Return where the place ANCHOR describes is now."
  (save-excursion
    (let ((start (and (plist-get anchor :thread)
                      (crs--discussion-thread-start (plist-get anchor :thread)))))
      (if start
          (progn (goto-char start)
                 (forward-line (plist-get anchor :offset))
                 (point))
        (goto-char (point-min))
        (forward-line (1- (or (plist-get anchor :line) 1)))
        (point)))))

(defun crs--redraw-discussion (buffer)
  "Draw the discussion BUFFER again, keeping each window's view of it."
  (with-current-buffer buffer
    (let ((anchor (crs--discussion-anchor (point)))
          (views (mapcar (lambda (window)
                           (let* ((pos (window-point window))
                                  (row (crs--window-row window pos)))
                             (list window (crs--discussion-anchor pos)
                                   (when (and (>= row 0) (< row (window-body-height window)))
                                     row))))
                         (get-buffer-window-list buffer nil t))))
      (crs--render-discussion buffer)
      (goto-char (crs--discussion-anchor-position anchor))
      (dolist (view views)
        (pcase-let ((`(,window ,window-anchor ,row) view))
          (when (window-live-p window)
            (set-window-point window (crs--discussion-anchor-position window-anchor))
            (when row
              (with-selected-window window
                (recenter row)))))))))

(defun crs--discussion-buffers (review)
  "The live discussion buffers showing the PR of the review buffer REVIEW."
  (seq-filter (lambda (buffer)
                (and (eq (buffer-local-value 'major-mode buffer) 'crs-discussion-mode)
                     (eq (buffer-local-value 'crs--discussion-review-buffer buffer) review)))
              (buffer-list)))

(defun crs--refresh-discussion (review)
  "Draw again any discussion buffer showing the review buffer REVIEW's PR.
Called as REVIEW gets a fresh payload (`crs--render-and-update')."
  (dolist (buffer (crs--discussion-buffers review))
    (condition-case err
        (crs--redraw-discussion buffer)
      (error (message "Couldn't redraw %s: %s" (buffer-name buffer)
                      (error-message-string err))))))

;;; Opening it, and going from it to the code

(defun crs--review-thread-key-at-point ()
  "The key of the review thread on the review buffer's line at point, or nil.
It names the line by its position in the PR's full diff, as the
discussion buffer's threads do; the line needn't hold a thread."
  (let* ((ctx (let ((inhibit-message t)
                    (message-log-max nil))
                (ignore-errors (crs--get-comment-context))))
         (file (nth 3 ctx))
         (position (nth 4 ctx)))
    (when (and file (integerp position))
      (if (zerop position)
          (list :file file :position nil)
        (when-let ((full (crs--full-diff-position file position)))
          (list :file file :position full))))))

(defun crs--discussion-buffer (review)
  "Return the discussion buffer for the review buffer REVIEW, made if need be."
  (pcase-let* ((`(,owner ,repo ,number)
                (with-current-buffer review (crs--get-current-review-info)))
               (buffer (get-buffer-create (crs--discussion-buffer-name owner repo number))))
    (with-current-buffer buffer
      (unless (derived-mode-p 'crs-discussion-mode)
        (crs-discussion-mode))
      (setq crs--discussion-review-buffer review))
    buffer))

;;;###autoload (autoload 'crs-show-discussion "crs-client" nil t)
(defun crs-show-discussion ()
  "Show the PR's discussion in a buffer beside the review buffer's code.
It holds the description, the conversation, every review thread under
the file and line it's about, and the outdated threads; see
`crs-discussion-mode'.  It opens in a window to the right of the
review's (`crs-discussion-window-width'), and on the thread about the
line at point, if it has one."
  (interactive)
  (crs--get-current-review-info)
  (unless crs--buffer-diff
    (user-error "No stored PR data. Please reload the review first"))
  (let* ((review (current-buffer))
         (key (crs--review-thread-key-at-point))
         (buffer (crs--discussion-buffer review))
         (fresh (zerop (buffer-size buffer))))
    ;; Shown first, so its bodies are filled to its own window.
    (pop-to-buffer buffer
                   `((display-buffer-reuse-window display-buffer-in-direction)
                     (direction . right)
                     (window-width . ,crs-discussion-window-width)))
    (if fresh
        (crs--render-discussion buffer)
      (crs--redraw-discussion buffer))
    (let ((start (and key (crs--discussion-thread-start key))))
      (when start
        (goto-char start)
        (when (eq (window-buffer) buffer)
          (recenter 0))))
    (when fresh
      (message (substitute-command-keys
                "\\<crs-discussion-mode-map>\\[crs-discussion-visit-thread] shows a thread's line in the review, \\[crs-discussion-next-thread] / \\[crs-discussion-previous-thread] step between threads, \\[quit-window] closes")))))

(defun crs--discussion-thread-at-point ()
  "The key of the review thread at point, or nil."
  (get-text-property (line-beginning-position) 'crs-discussion-thread))

(defun crs--discussion-review ()
  "The discussion buffer's review buffer, or a `user-error' if it's gone."
  (let ((review crs--discussion-review-buffer))
    (unless (buffer-live-p review)
      (user-error "The review buffer this discussion came from is gone"))
    review))

(defun crs--review-line-of-thread (key)
  "The review buffer's line for the thread KEY names, or nil if it shows none.
Its line on the diff the buffer shows; on a filtered diff, nil for a line
the filtered diff doesn't show.  A thread on the whole file goes to the
file's first row.  Call it in the review buffer."
  (let* ((file (plist-get key :file))
         (position (plist-get key :position))
         (row (if position (crs--displayed-diff-position file position) 1)))
    (and row (crs--find-position-in-diff file row))))

(defun crs--show-thread-line (key select)
  "Show the line the thread KEY is about in the review buffer.
SELECT non-nil selects the review's window too.  Without SELECT, the
review buffer moves only in a window already showing it."
  (let ((review (crs--discussion-review)))
    (cond
     ((plist-get key :outdated)
      (message "This thread is outdated: the diff no longer shows its line"))
     (t
      (let ((line (with-current-buffer review (crs--review-line-of-thread key)))
            (window (get-buffer-window review)))
        (cond
         ((null line)
          (message (if (with-current-buffer review (crs--filtered-diff-displayed-p))
                       "The filtered diff doesn't show %s; F shows the full diff"
                     "The review buffer doesn't show %s")
                   (plist-get key :file)))
         ((or window select)
          (unless window
            (setq window (display-buffer review
                                         '((display-buffer-reuse-window display-buffer-in-direction)
                                           (direction . left)))))
          (with-selected-window window
            (goto-char (point-min))
            (forward-line (1- line))
            (recenter)
            (pulse-momentary-highlight-one-line (point)))
          (when select
            (select-window window)))))))))

(defun crs-discussion-visit-thread ()
  "Go to the line the review thread at point is about, in the review buffer."
  (interactive)
  (let ((key (crs--discussion-thread-at-point)))
    (unless key
      (user-error "No review thread at point"))
    (crs--show-thread-line key t)))

(defun crs--discussion-thread-starts ()
  "Return where each review thread opens, in the order the buffer shows them."
  (save-excursion
    (goto-char (point-min))
    (let ((starts nil))
      (while (not (eobp))
        (when (get-text-property (point) 'crs-discussion-location)
          (push (point) starts))
        (forward-line 1))
      (nreverse starts))))

(defun crs--discussion-step (direction)
  "Move to the next review thread, DIRECTION 1, or the previous, -1.
The next opens below point's line, the previous above it, as outline
headings go.  Shows its line in a window showing the review buffer."
  (let* ((here (line-beginning-position))
         (starts (crs--discussion-thread-starts))
         (target (if (> direction 0)
                     (seq-find (lambda (start) (> start here)) starts)
                   (seq-find (lambda (start) (< start here)) (reverse starts)))))
    (if (not target)
        (message "No %s review thread" (if (> direction 0) "next" "previous"))
      (goto-char target)
      (crs--show-thread-line (crs--discussion-thread-at-point) nil))))

(defun crs-discussion-next-thread ()
  "Move to the next review thread, showing its line in the review buffer."
  (interactive)
  (crs--discussion-step 1))

(defun crs-discussion-previous-thread ()
  "Move to the previous review thread, showing its line in the review buffer."
  (interactive)
  (crs--discussion-step -1))

(defun crs-discussion-sync ()
  "Sync the PR with the server; the discussion is drawn again when it lands."
  (interactive)
  (with-current-buffer (crs--discussion-review)
    (crs-sync-pr)))

(provide 'crs-discussion)
;;; crs-discussion.el ends here
