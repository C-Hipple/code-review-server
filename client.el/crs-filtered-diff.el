;;; crs-filtered-diff.el --- Narrow the review buffer to a range of commits. -*- lexical-binding: t; -*-

;;; Commentary:

;; The server narrows a PR's diff to the commits after a baseline it picks:
;; the changes since your last review, or since you last reviewed or
;; commented (ListDiffFilters / GetFilteredDiff, docs/protocol.md).  It
;; computes them in the background whenever a PR is fetched or updated, so
;; switching is a cache read.
;;
;; `crs-show-filtered-diff' (F in a review buffer) swaps the buffer's diff for
;; a filtered one, and back.  Comments are anchored by their position in the
;; PR's full diff, which a filtered diff doesn't share, so while one is shown
;; comments are not drawn inline and new ones can't be started; the
;; conversation section still lists them all.  A fresh payload (a sync, a
;; submitted review) keeps the filtered diff on screen and asks the server for
;; it again, since the baseline or the head may have moved.

;;; Code:

(require 'seq)
(require 'crs-vars)
(require 'crs-rpc)

(declare-function crs--get-current-review-info "crs-review")
(declare-function crs--render-and-update "crs-render")

(defconst crs--full-diff-choice "Full diff"
  "The `crs-show-filtered-diff' choice that goes back to the PR's full diff.")

(defun crs--short-sha (sha)
  "The first seven characters of SHA, the way git abbreviates it."
  (if (and sha (> (length sha) 7)) (substring sha 0 7) (or sha "")))

(defun crs--diff-filter-name (id)
  "The name the server gives the diff filter ID, or ID itself."
  (or (cdr (assq 'name (seq-find (lambda (f) (equal (cdr (assq 'id f)) id))
                                 crs-diff-filters)))
      id))

(defun crs--filtered-diff-shows-diff-p (result)
  "Non-nil when RESULT, a GetFilteredDiff reply, has a diff to show.
Anything but a ready result leaves the full diff on screen."
  (equal (cdr (assq 'status result)) "ready"))

(defun crs--filtered-diff-displayed-p ()
  "Non-nil when the review buffer shows a filtered diff rather than the full one.
Picking a filter whose answer has no diff (no baseline, a rewritten history)
leaves the full diff, and its comments, on screen."
  (and crs--buffer-diff-filter
       (crs--filtered-diff-shows-diff-p crs--buffer-filtered-diff)))

(defun crs--filtered-diff-summary (result filter-name)
  "One line saying what RESULT, FILTER-NAME's GetFilteredDiff reply, shows."
  (let* ((status (cdr (assq 'status result)))
         (message-text (or (cdr (assq 'message result)) ""))
         (baseline (cdr (assq 'baseline result)))
         (detail (or (cdr (assq 'detail baseline)) "the baseline"))
         (from (crs--short-sha (cdr (assq 'base_sha result))))
         (to (crs--short-sha (cdr (assq 'head_sha result))))
         (commits (length (cdr (assq 'commits result))))
         (diff (or (cdr (assq 'diff result)) "")))
    (cond
     ((and (equal status "ready") (string-empty-p (string-trim diff)))
      (format "%s: nothing has changed since %s (%s)." filter-name detail from))
     ((equal status "ready")
      (format "%s: changes since %s%s, %s -> %s%s.  Comments are hidden; press F for the full diff."
              filter-name detail
              (if (eq (cdr (assq 'inferred baseline)) t) " (estimated from its time)" "")
              from to
              (if (> commits 0)
                  (format ", %d new commit%s" commits (if (= commits 1) "" "s"))
                "")))
     ((equal status "error")
      (format "Couldn't load \"%s\": %s  Showing the full diff." filter-name message-text))
     (t (format "%s  Showing the full diff." message-text)))))

(defun crs--with-diff-filters (callback)
  "Call CALLBACK once the server's diff filters are known.
Fetches them with ListDiffFilters the first time."
  (if crs-diff-filters
      (funcall callback)
    (crs--send-request
     "RPCHandler.ListDiffFilters"
     (vector)
     (lambda (result)
       (let ((err (cdr (assq 'error result))))
         (if err
             (message "Couldn't list diff filters: %s"
                      (if (stringp err) err (cdr (assq 'message err))))
           (setq crs-diff-filters (append (cdr (assq 'filters result)) nil))
           (funcall callback)))))))

(defun crs--choose-diff-filter ()
  "Ask which diff to show: a filter's ID, or nil for the full diff."
  (let* ((choices (cons (cons crs--full-diff-choice nil)
                        (mapcar (lambda (f) (cons (cdr (assq 'name f)) (cdr (assq 'id f))))
                                crs-diff-filters)))
         (choice (completing-read "Show diff: " choices nil t)))
    (cdr (assoc choice choices))))

(defun crs--apply-filtered-diff (buffer filter result)
  "Show RESULT, FILTER's GetFilteredDiff reply, in the review BUFFER."
  (when (buffer-live-p buffer)
    (with-current-buffer buffer
      ;; Only the request for the filter still picked gets to paint.
      (when (equal crs--buffer-diff-filter filter)
        (setq crs--buffer-filtered-diff result)
        (setq crs--buffer-diff (if (crs--filtered-diff-shows-diff-p result)
                                   (or (cdr (assq 'diff result)) "")
                                 crs--buffer-full-diff))
        (crs--render-and-update buffer nil (line-number-at-pos))
        (message "%s" (crs--filtered-diff-summary result (crs--diff-filter-name filter)))))))

(defun crs--request-filtered-diff (buffer filter)
  "Ask the server for FILTER's diff of BUFFER's PR, and show it there."
  (let* ((info (with-current-buffer buffer (crs--get-current-review-info)))
         (owner (nth 0 info))
         (repo (nth 1 info))
         (number (nth 2 info)))
    (crs--send-request
     "RPCHandler.GetFilteredDiff"
     (vector (list (cons 'Owner owner)
                   (cons 'Repo repo)
                   (cons 'Number number)
                   (cons 'Filter filter)))
     (lambda (result)
       (let ((err (cdr (assq 'error result))))
         (if (not err)
             (crs--apply-filtered-diff buffer filter result)
           (message "Couldn't load the filtered diff: %s"
                    (if (stringp err) err (cdr (assq 'message err))))
           (when (buffer-live-p buffer)
             (with-current-buffer buffer
               (when (equal crs--buffer-diff-filter filter)
                 (crs--show-full-diff buffer))))))))))

(defun crs--show-full-diff (buffer)
  "Put the PR's full diff back in the review BUFFER."
  (with-current-buffer buffer
    (let ((full crs--buffer-full-diff)
          (was crs--buffer-diff-filter))
      (setq crs--buffer-diff-filter nil
            crs--buffer-filtered-diff nil
            crs--buffer-full-diff nil)
      (when was
        (setq crs--buffer-diff full)
        (crs--render-and-update buffer nil (line-number-at-pos))))))

(defun crs--refresh-diff-filter (buffer)
  "Ask again for the filtered diff the review BUFFER shows.
Called after a fresh PR payload, which can move the filter's baseline or
the head it diffs to."
  (when (buffer-live-p buffer)
    (let ((filter (buffer-local-value 'crs--buffer-diff-filter buffer)))
      (when filter
        (crs--request-filtered-diff buffer filter)))))

;;;###autoload
(defun crs-show-filtered-diff (&optional filter)
  "Narrow the review buffer's diff to the commits FILTER picks.
FILTER is a diff filter ID such as \"since-last-review\"; nil goes back to
the PR's full diff.  Interactively, pick one by name."
  (interactive)
  (let ((buffer (current-buffer)))
    (crs--get-current-review-info)
    (if (called-interactively-p 'any)
        (crs--with-diff-filters
         (lambda ()
           (with-current-buffer buffer
             (crs--select-diff-filter buffer (crs--choose-diff-filter)))))
      (crs--select-diff-filter buffer filter))))

(defun crs--select-diff-filter (buffer filter)
  "Show FILTER's diff in the review BUFFER, or the full diff for nil."
  (with-current-buffer buffer
    (if (null filter)
        (crs--show-full-diff buffer)
      (unless crs--buffer-diff-filter
        ;; Switching from the full diff: keep it for switching back.
        (setq crs--buffer-full-diff crs--buffer-diff))
      (setq crs--buffer-diff-filter filter)
      (message "Loading %s..." (crs--diff-filter-name filter))
      (crs--request-filtered-diff buffer filter))))

;;;###autoload
(defun crs-show-diff-since-last-review ()
  "Narrow the review buffer's diff to the changes since your last review."
  (interactive)
  (crs-show-filtered-diff "since-last-review"))

;;;###autoload
(defun crs-show-diff-since-last-interaction ()
  "Narrow the review buffer's diff to the changes since you last took part.
That is your most recent review or comment on the PR."
  (interactive)
  (crs-show-filtered-diff "since-last-interaction"))

;;;###autoload
(defun crs-show-full-diff ()
  "Put the PR's full diff back in the review buffer."
  (interactive)
  (crs--get-current-review-info)
  (crs--show-full-diff (current-buffer)))

(defun crs--refuse-comment-on-filtered-diff ()
  "Signal a `user-error' when the review buffer shows a filtered diff.
Comments are placed by their position in the PR's full diff."
  (when (crs--filtered-diff-displayed-p)
    (user-error "Comments anchor to the full diff; press F and pick \"%s\" first"
                crs--full-diff-choice)))

(provide 'crs-filtered-diff)
;;; crs-filtered-diff.el ends here
