;;; crs-review-actions.el --- Review verdict submission and feedback editing. -*- lexical-binding: t; -*-

;;; Commentary:

;; Review verdict submission and feedback editing.
;;
;; A submit runs in the background.  `crs-submit-review' returns as soon as
;; the request is sent, so the reviewer can read on, or move to the next PR,
;; while GitHub takes the review and the server refetches the PR.  When the
;; server replies, a message says what went out and the PR's review buffer is
;; redrawn from the reply.
;;
;; A submit names the pending comments the review buffer showed
;; (ExpectedDraftIDs), so the server posts nothing if any of them is gone:
;; sent by an earlier submit, or deleted with the user on a shared server.
;; A submit whose connection to a shared server dropped before the answer
;; may have gone out all the same, so it is reported as such rather than as
;; not submitted.  Either way the PR is reloaded to show where it stands.

;;; Code:

(require 'crs-vars)
(require 'crs-rpc)
(require 'crs-render)

(declare-function crs--get-current-review-info "crs-review")
(declare-function crs--render-and-update "crs-render")
(declare-function crs--review-buffer-name "crs-review")

(defun crs--pr-label (owner repo number)
  "OWNER/REPO #NUMBER, the way messages name a PR."
  (format "%s/%s #%d" owner repo number))

(defun crs--review-submit-phrases (event)
  "How a submit of EVENT reads in flight and once done, as (DOING . DONE)."
  (pcase event
    ("APPROVE" '("Approving" . "Approved"))
    ("REQUEST_CHANGES" '("Requesting changes on" . "Requested changes on"))
    (_ '("Submitting review on" . "Review submitted on"))))

(defun crs--review-submitted-message (event ref failed-replies)
  "The message for a finished submit of EVENT on the PR named REF.
REF is spelled out because the reviewer may be on another PR by the time
the submit lands.  FAILED-REPLIES are the server's descriptions of the
replies GitHub refused; those stay pending, so the message says so rather
than reading as a clean submit."
  (let ((done (format "%s %s" (cdr (crs--review-submit-phrases event)) ref))
        (count (length failed-replies)))
    (if (zerop count)
        done
      (format "%s, but %s still pending: %s"
              done
              (if (= count 1)
                  "1 reply could not be posted and is"
                (format "%d replies could not be posted and are" count))
              (mapconcat #'identity failed-replies "; ")))))

(defun crs--review-draft-ids (comments)
  "The ids of the pending comments among COMMENTS, as a vector of numbers.
These are the drafts a submit from the review buffer showing COMMENTS
carries, which the server checks are all still there before it posts."
  (vconcat
   (delq nil
         (mapcar (lambda (c)
                   (let ((id (cdr (assq 'id c))))
                     (when (equal (cdr (assq 'author c)) "local")
                       (cond ((integerp id) id)
                             ((and (stringp id) (string-match-p "\\`[0-9]+\\'" id))
                              (string-to-number id))))))
                 comments))))

(defun crs--review-reload (owner repo number)
  "Fetch OWNER/REPO #NUMBER afresh into its review buffer, if one is open.
Quietly: the message before it says why.  It runs from a timer, out of the
sentinel or filter that reported the submit, since reaching a shared
server again may mean reconnecting."
  (when (get-buffer (crs--review-buffer-name owner repo number))
    (condition-case err
        (crs--send-request
         "RPCHandler.SyncPR"
         (vector (list (cons 'Owner owner)
                       (cons 'Repo repo)
                       (cons 'Number number)))
         (lambda (result)
           (let ((buffer (get-buffer (crs--review-buffer-name owner repo number)))
                 (inhibit-message t))
             (if (cdr (assq 'error result))
                 (message "Could not reload %s: %s" (crs--pr-label owner repo number)
                          (cdr (assq 'error result)))
               (when buffer
                 (crs--render-and-update buffer result))))))
      (error
       (let ((inhibit-message t))
         (message "Could not reload %s: %s" (crs--pr-label owner repo number)
                  (error-message-string err)))))))

(defconst crs--drafts-changed-prefix "drafts changed:"
  "Prefix of SubmitReview's refusal of a review whose pending comments are gone.")

(defun crs--review-submit-in-flight-p (ref)
  "Non-nil while a review submitted on the PR named REF awaits its reply.
A submit sent to a server that has since exited will never be answered,
so it stops counting once that process is gone."
  (let ((process (gethash ref crs--review-submits)))
    (and process (process-live-p process))))

(defun crs--review-submit-finished (owner repo number event body result)
  "Report RESULT, the reply to a submit of EVENT on OWNER/REPO #NUMBER.
BODY is the feedback that was sent.  The message comes first; the review
buffer is then redrawn from RESULT, which is the PR as the server
refetched it once the review was in."
  (let* ((ref (crs--pr-label owner repo number))
         (err (cdr (assq 'error result)))
         (text (if (stringp err) err (cdr (assq 'message err)))))
    (remhash ref crs--review-submits)
    (cond
     ((crs--connection-lost-p text)
      ;; The server finishes a submit whose connection has gone, so it may
      ;; well have been posted: offering it again would post it twice.
      ;; Show where the PR stands instead.  The feedback is kept, in case.
      (message "Lost the connection to the server while submitting the review on %s: it may or may not have gone out.  Check the PR before submitting again; reloading it"
               ref)
      (run-at-time 0 nil #'crs--review-reload owner repo number))
     ((and (stringp text) (string-prefix-p crs--drafts-changed-prefix text))
      ;; Nothing went out, but the buffer showed drafts that are gone.
      (message "Review on %s was not submitted: some of its pending comments are gone, already submitted or removed with your account after a stretch of inactivity.  Reloading the PR: check what is pending, then submit again"
               ref)
      (run-at-time 0 nil #'crs--review-reload owner repo number))
     (err
      ;; Nothing went out: the feedback and the pending comments are left
      ;; as they were, ready to submit again.  A shared server that needs
      ;; the reviewer's own GitHub account says so; linking it is the fix.
      (message "Review on %s was not submitted: %s%s" ref text
               (if (crs--link-github-refusal-p text)
                   ".  Your feedback is kept: link your GitHub account with M-x crs-link-github, then submit again"
                 "")))
     (t
      (message "%s" (crs--review-submitted-message
                     event ref (cdr (assq 'failed_replies result))))
      (let ((review-buffer (get-buffer (crs--review-buffer-name owner repo number))))
        (when review-buffer
          (with-current-buffer review-buffer
            ;; Clear only the feedback that went out; a draft written while
            ;; the submit was in flight is for the next review.
            (when (equal crs--buffer-review-feedback body)
              (setq crs--buffer-review-feedback nil)))
          ;; Anything the redraw says goes to *Messages* only, so the
          ;; outcome stays in the echo area.
          (let ((inhibit-message t))
            (crs--render-and-update review-buffer result))))))))

(defun crs-submit-review (event)
  "Submit a review with EVENT on the PR in the current review buffer.
The body is taken from `crs--buffer-review-feedback'.  If the body is
empty, prompts the user.

The submit runs in the background: this returns once the request is
sent.  When the server replies, a message says what went out and the
review buffer is redrawn from the reply, the PR as it stands after the
review.  A PR takes one submit at a time."
  (interactive
   (list (completing-read "Event: " '("APPROVE" "REQUEST_CHANGES" "COMMENT") nil t)))
  (let* ((info (crs--get-current-review-info))
         (owner (nth 0 info))
         (repo (nth 1 info))
         (number (nth 2 info))
         (ref (crs--pr-label owner repo number))
         (body crs--buffer-review-feedback))
    (when (crs--review-submit-in-flight-p ref)
      (user-error "A review on %s is already being submitted" ref))
    (when (or (null body) (string-match-p "\\`[[:space:]\n]*\\'" body))
      (unless (yes-or-no-p "Review feedback is empty. Continue anyway? ")
        (user-error "Aborted")))

    (message "%s %s..." (car (crs--review-submit-phrases event)) ref)
    (crs--send-request
     "RPCHandler.SubmitReview"
     (vector (list (cons 'Owner owner)
                   (cons 'Repo repo)
                   (cons 'Number number)
                   (cons 'Event event)
                   (cons 'Body (or body ""))
                   (cons 'ExpectedDraftIDs (crs--review-draft-ids crs--buffer-comments))))
     (lambda (result)
       (crs--review-submit-finished owner repo number event body result)))
    ;; Recorded once sent, against the connection it went out on: sending
    ;; to a shared server may have reconnected first, and the connection
    ;; before that is gone.  The reply comes from the process filter, so it
    ;; cannot land before this.
    (puthash ref crs--process crs--review-submits)))

(defun crs-set-review-feedback ()
  "Set the review feedback for the current PR."
  (interactive)
  (let* ((info (crs--get-current-review-info))
         (owner (nth 0 info))
         (repo (nth 1 info))
         (number (nth 2 info))
         (buffer (get-buffer-create (format "*Review Feedback %s/%s #%d*" owner repo number)))
         (current-feedback crs--buffer-review-feedback)
         (original-review-buffer (current-buffer)))
    (with-current-buffer buffer
      (markdown-mode)
      (erase-buffer)
      (when current-feedback
        (insert current-feedback))
      (setq-local crs--comment-owner owner)
      (setq-local crs--comment-repo repo)
      (setq-local crs--comment-number number)
      (local-set-key (kbd "C-c C-c")
                     (lambda ()
                       (interactive)
                       (let ((feedback (buffer-string)))
                         (with-current-buffer original-review-buffer
                           (setq crs--buffer-review-feedback feedback)
                           (crs--render-and-update (current-buffer) nil))
                         (kill-buffer-and-window)
                         (message "Review feedback set."))))
      (local-set-key (kbd "C-c C-k") (lambda () (interactive) (kill-buffer-and-window) (message "Review feedback aborted."))))
    (switch-to-buffer-other-window buffer)
    (when (fboundp 'evil-insert-state)
      (evil-insert-state))))

(defun crs-approve-review ()
  "Approve the review."
  (interactive)
  (crs-submit-review "APPROVE"))

(defun crs-comment-review ()
  "Comment on the review."
  (interactive)
  (crs-submit-review "COMMENT"))


(defun crs-request-changes-review ()
  "Request changes on the review."
  (interactive)
  (crs-submit-review "REQUEST_CHANGES"))

(provide 'crs-review-actions)
;;; crs-review-actions.el ends here
