;;; crs-review-actions.el --- Review verdict submission and feedback editing. -*- lexical-binding: t; -*-

;;; Commentary:

;; Review verdict submission and feedback editing.
;;
;; A submit runs in the background.  `crs-submit-review' returns as soon as
;; the request is sent, so the reviewer can read on, or move to the next PR,
;; while GitHub takes the review and the server refetches the PR.  When the
;; server replies, a message says what went out and the PR's review buffer is
;; redrawn from the reply.

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
  (let ((ref (crs--pr-label owner repo number))
        (err (cdr (assq 'error result))))
    (remhash ref crs--review-submits)
    (if err
        ;; Nothing went out: the feedback and the pending comments are left
        ;; as they were, ready to submit again.
        (message "Review on %s was not submitted: %s"
                 ref (if (stringp err) err (cdr (assq 'message err))))
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
            (crs--render-and-update review-buffer result)))))))

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
    (puthash ref crs--process crs--review-submits)
    (condition-case err
        (crs--send-request
         "RPCHandler.SubmitReview"
         (vector (list (cons 'Owner owner)
                       (cons 'Repo repo)
                       (cons 'Number number)
                       (cons 'Event event)
                       (cons 'Body (or body ""))))
         (lambda (result)
           (crs--review-submit-finished owner repo number event body result)))
      ;; Never sent, so no reply will come to clear it.
      (error
       (remhash ref crs--review-submits)
       (signal (car err) (cdr err))))))

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
