;;; crs-review-actions.el --- Review verdict submission and feedback editing. -*- lexical-binding: t; -*-

;;; Commentary:

;; Review verdict submission and feedback editing.

;;; Code:

(require 'crs-vars)
(require 'crs-rpc)
(require 'crs-render)

(declare-function crs--get-current-review-info "crs-review")
(declare-function crs--render-and-update "crs-render")
(declare-function crs--review-buffer-name "crs-review")

(defun crs-submit-review (event)
  "Submit a review with EVENT.
The body is taken from `crs--buffer-review-feedback`.
If the body is empty, prompts the user."
  (interactive
   (list (completing-read "Event: " '("APPROVE" "REQUEST_CHANGES" "COMMENT") nil t)))
  (let ((body crs--buffer-review-feedback)
        (owner nil)
        (repo nil)
        (number nil))
    (when (or (null body) (string-match-p "\\`[[:space:]\n]*\\'" body))
      (unless (yes-or-no-p "Review feedback is empty. Continue anyway? ")
        (user-error "Aborted")))

    (let ((info (crs--get-current-review-info)))
      (setq owner (nth 0 info)
            repo (nth 1 info)
            number (nth 2 info)))

    (crs--send-request
     "RPCHandler.SubmitReview"
     (vector (list (cons 'Owner owner)
                   (cons 'Repo repo)
                   (cons 'Number number)
                   (cons 'Event event)
                   (cons 'Body (or body ""))))
     (lambda (result)
       (let ((err (cdr (assq 'error result))))
         (if err
             (message "Error submitting review: %s" (if (stringp err) err (cdr (assq 'message err))))
           (let ((review-buffer (get-buffer (crs--review-buffer-name owner repo number))))
             (when review-buffer
               (with-current-buffer review-buffer
                 (setq crs--buffer-review-feedback nil)
                 (crs--render-and-update review-buffer result)))
             (message "Review submitted successfully!"))))))))

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
