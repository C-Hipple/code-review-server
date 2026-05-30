;;; crs-tests.el --- Smoke tests for the crs Emacs client -*- lexical-binding: t; -*-

;;; Commentary:

;; Lightweight ERT smoke tests for the crs client.  These do NOT exercise
;; the live JSON-RPC server; they verify that the split modules load and
;; wire together correctly (commands and modes are defined, buffer-local
;; state is declared) and that the pure helper functions behave as
;; expected.  Run with:
;;
;;   emacs --batch -L . -l crs-tests.el -f ert-run-tests-batch-and-exit

;;; Code:

(require 'ert)
(require 'crs-client)

;;; --- Wiring: the loader pulls in every module ---

(ert-deftest crs-test-feature-loaded ()
  "Loading `crs-client' provides the feature and all submodules."
  (should (featurep 'crs-client))
  (dolist (feat '(crs-vars crs-html crs-rpc crs-render crs-list-mode
                  crs-review crs-comments crs-review-actions crs-plugins))
    (should (featurep feat))))

(ert-deftest crs-test-key-commands-defined ()
  "Representative interactive commands from each module are defined."
  (dolist (fn '(crs-start-server crs-shutdown-server crs-restart-server
                crs-get-reviews crs-refresh-reviews crs-get-review
                crs-next-pr crs-prev-pr crs-start-review-at-point
                crs-toggle-section crs-toggle-comments crs-visit-file
                crs-add-or-edit-comment crs-add-comment crs-delete-local-comment
                crs-submit-comment crs-abort-comment
                crs-submit-review crs-approve-review crs-comment-review
                crs-request-changes-review crs-set-review-feedback
                crs-expand-hunk-before crs-expand-hunk-after
                crs-sync-pr crs-checkout-current-project
                crs-get-plugin-output crs-rerun-plugin crs-run-on-demand-plugin
                crs-get-rate-limit-status))
    (should (fboundp fn))
    (should (commandp fn))))

(ert-deftest crs-test-internal-helpers-defined ()
  "Cross-module internal helpers resolve after loading."
  (dolist (fn '(crs--send-request crs--render-and-update crs--render-diff
                crs--get-comment-context crs--get-current-review-info
                crs--review-buffer-name crs--parse-hunk-header
                crs--ensure-html crs--make-html-placeholder
                crs--process-html-placeholders crs--strip-comments-tree))
    (should (fboundp fn))))

(ert-deftest crs-test-buffer-local-state-declared ()
  "Buffer-local state variables are declared (centralized in crs-vars)."
  (dolist (var '(crs--process crs--pending-requests crs-reviews-buffer-name
                 crs--buffer-owner crs--buffer-diff crs--buffer-comments
                 crs--buffer-metadata crs--buffer-show-comments
                 crs--comment-owner crs--comment-filename crs--comment-position
                 crs--plugin-owner crs--plugin-name crs--plugin-output-map))
    (should (boundp var))))

;;; --- Modes can be entered without error ---

(ert-deftest crs-test-modes-instantiate ()
  "Each major mode can be activated in a fresh buffer without error."
  (dolist (mode '(crs-list-mode my-code-review-mode comment-edit-mode
                  crs-outdated-comments-mode crs-plugin-output-mode))
    (should (fboundp mode))
    (with-temp-buffer
      (funcall mode)
      (should (eq major-mode mode)))))

;;; --- Pure helper behavior ---

(ert-deftest crs-test-review-buffer-name ()
  (should (equal (crs--review-buffer-name "C-Hipple" "code-review-server" 83)
                 "* CRS: #83 - code-review-server - C-Hipple *")))

(ert-deftest crs-test-parse-hunk-header ()
  (let ((p (crs--parse-hunk-header "@@ -1,5 +2,6 @@ func main() {")))
    (should p)
    (should (= (plist-get p :orig-start) 1))
    (should (= (plist-get p :orig-length) 5))
    (should (= (plist-get p :new-start) 2))
    (should (= (plist-get p :new-length) 6))
    (should (equal (plist-get p :suffix) " func main() {")))
  ;; Omitted lengths default to 1.
  (let ((p (crs--parse-hunk-header "@@ -3 +4 @@")))
    (should (= (plist-get p :orig-length) 1))
    (should (= (plist-get p :new-length) 1)))
  ;; Non-hunk lines return nil.
  (should-not (crs--parse-hunk-header "not a hunk header")))

(ert-deftest crs-test-ensure-html ()
  ;; Plain text is HTML-escaped and wrapped in a paragraph.
  (let ((out (crs--ensure-html "a & b < c")))
    (should (string-match-p "&amp;" out))
    (should (string-match-p "&lt;" out))
    (should (string-prefix-p "<p>" out)))
  ;; Content that already looks like HTML is left structurally intact.
  (should (equal (crs--ensure-html "<div>hi</div>") "<div>hi</div>")))

(ert-deftest crs-test-html-placeholder-roundtrip ()
  ;; Empty content yields the no-content sentinel, not a placeholder.
  (should (equal (crs--make-html-placeholder "") "(No content provided)"))
  ;; Non-empty content yields a placeholder matching the decode regexp.
  (let ((ph (crs--make-html-placeholder "hello")))
    (should (string-match crs--html-placeholder-regexp ph))
    (should (equal (decode-coding-string
                    (base64-decode-string (match-string 2 ph)) 'utf-8)
                   "hello"))))

(ert-deftest crs-test-strip-comments-tree ()
  (let ((stripped (crs--strip-comments-tree
                   "** PR one\nbody\n*** Comments\na comment\n** PR two\n")))
    (should-not (string-match-p "Comments" stripped))
    (should-not (string-match-p "a comment" stripped))
    (should (string-match-p "PR one" stripped))
    (should (string-match-p "PR two" stripped))))

(provide 'crs-tests)
;;; crs-tests.el ends here
