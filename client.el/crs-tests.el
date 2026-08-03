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
(require 'cl-lib)
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
                crs-toggle-annotations
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
                crs--process-html-placeholders crs--strip-comments-tree
                crs--index-annotations crs--render-annotation-block
                crs--insert-annotations-into-buffer
                crs--format-compact-annotation-indicator
                crs--annotations-on-current-line
                crs--annotations-minibuffer-summary
                crs--insert-plugin-output-entry))
    (should (fboundp fn))))

(ert-deftest crs-test-buffer-local-state-declared ()
  "Buffer-local state variables are declared (centralized in crs-vars)."
  (dolist (var '(crs--process crs--pending-requests crs-reviews-buffer-name
                 crs--buffer-owner crs--buffer-diff crs--buffer-comments
                 crs--buffer-metadata crs--buffer-show-comments
                 crs--buffer-annotations crs--buffer-show-annotations
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

;;; --- Plugin annotations ---

(defconst crs-test--annotation-diff
  (concat "diff --git a/test.py b/test.py\n"
          "--- a/test.py\n"
          "+++ b/test.py\n"
          "@@ -1,3 +1,4 @@\n"
          " line one\n"
          "+line two\n"
          " line three\n"
          " line four\n")
  "A raw single-file diff; head-side lines 1-4 are visible.")

(defconst crs-test--annotations
  (vector '((filename . "./test.py") (line . 2) (severity . "warning")
            (content . "this line looks wrong") (plugin . "Security Check"))
          '((filename . "test.py") (line . 99) (severity . "info")
            (content . "outside the hunk") (plugin . "Style"))
          '((filename . "missing.py") (line . 1) (severity . "error")
            (content . "not in the diff") (plugin . "Style")))
  "Annotations as decoded from a GetPR reply: one anchorable, one whose
line the diff does not show, and one for a file outside the diff.")

(ert-deftest crs-test-index-annotations ()
  "Annotations index by \"file:line\" with a leading ./ normalized away."
  (let ((map (crs--index-annotations crs-test--annotations)))
    (should (= (hash-table-count map) 3))
    (should (= (length (gethash "test.py:2" map)) 1))
    (should (gethash "test.py:99" map))
    (should (gethash "missing.py:1" map))))

(ert-deftest crs-test-compact-annotation-indicator ()
  (should (equal (crs--format-compact-annotation-indicator
                  '(((plugin . "A")) ((plugin . "B")) ((plugin . "A"))))
                 "<A: A, B>")))

(ert-deftest crs-test-insert-annotations-collapsed ()
  "Collapsed annotations append <A: ...> indicators without touching the diff lines."
  (with-temp-buffer
    (insert crs-test--annotation-diff)
    (crs--insert-annotations-into-buffer crs-test--annotations nil)
    ;; The head-line-2 annotation lands on \"+line two\"...
    (goto-char (point-min))
    (search-forward "+line two")
    (should (string-match-p "<A: Security Check>"
                            (buffer-substring-no-properties
                             (line-beginning-position) (line-end-position))))
    ;; ...carrying its annotations in a text property for the echo-area preview.
    (let ((found (crs--annotations-on-current-line)))
      (should found)
      (should (equal (cdr (assq 'content (car found))) "this line looks wrong")))
    ;; The out-of-hunk annotation attaches to the file's first hunk header.
    (goto-char (point-min))
    (search-forward "@@ -1,3 +1,4 @@")
    (should (string-match-p "<A: Style>"
                            (buffer-substring-no-properties
                             (line-beginning-position) (line-end-position))))
    ;; The annotation for a file not in the diff is dropped.
    (should-not (string-match-p "<A: [^>]*>[^\n]*not in the diff" (buffer-string)))
    (should-not (text-property-any (point-min) (point-max)
                                   'crs-annotations
                                   (list (aref crs-test--annotations 2))))))

(ert-deftest crs-test-insert-annotations-expanded ()
  "Expanded annotations render full blocks beneath the annotated lines."
  (with-temp-buffer
    (insert crs-test--annotation-diff)
    (crs--insert-annotations-into-buffer crs-test--annotations t)
    (goto-char (point-min))
    (search-forward "+line two")
    (forward-line 1)
    (should (string-match-p "┌─ PLUGIN ANNOTATION"
                            (buffer-substring-no-properties
                             (line-beginning-position) (line-end-position))))
    (let ((text (buffer-string)))
      (should (string-match-p "\\[warning\\] Security Check" text))
      (should (string-match-p "this line looks wrong" text))
      ;; The unanchorable annotation renders before the hunk header, naming its line.
      (should (string-match-p "\\[info\\] Style @ line 99" text))
      (should (< (string-match "Style @ line 99" text)
                 (string-match "@@ -1,3 \\+1,4 @@" text)))
      (should-not (string-match-p "not in the diff" text)))))

(ert-deftest crs-test-annotations-do-not-shift-comment-positions ()
  "Expanded annotation blocks are invisible to diff-position counting."
  (with-temp-buffer
    (insert crs-test--annotation-diff)
    (crs--insert-annotations-into-buffer crs-test--annotations t)
    ;; \" line three\" is diff position 3 (line one=1, +line two=2).
    (let ((found (crs--find-position-in-diff "test.py" 3)))
      (should found)
      (goto-char (point-min))
      (forward-line (1- found))
      (should (string-match-p "line three"
                              (buffer-substring-no-properties
                               (line-beginning-position) (line-end-position)))))))

(ert-deftest crs-test-annotations-minibuffer-summary ()
  (should (equal (crs--annotations-minibuffer-summary
                  '(((plugin . "Sec") (severity . "warning")
                     (content . "bad line\nsecond line"))))
                 "1 annotation: [Sec/warning]: bad line")))

(ert-deftest crs-test-insert-plugin-output-entry ()
  "Plugin output entries prefer the parsed body and list annotations sorted."
  ;; Contract output: parsed body shown instead of the raw JSON result.
  (with-temp-buffer
    (crs--insert-plugin-output-entry
     "Security Check"
     '((result . "{\"body\":{\"body_type\":\"markdown\",\"body_content\":\"All clear.\"}}")
       (status . "success")
       (body . ((body_type . "markdown") (body_content . "All clear.")))
       (annotations . [((filename . "b.py") (line . 2) (severity . "warning") (content . "hm"))
                       ((filename . "a.py") (line . 5) (severity . "") (content . "note"))])))
    (let ((text (buffer-string)))
      (should (string-match-p (regexp-quote "# Plugin: Security Check (Status: success)") text))
      (should (string-match-p (regexp-quote "All clear.") text))
      (should-not (string-match-p "body_type" text))
      (should (string-match-p (regexp-quote "## Annotations (2)") text))
      (should (< (string-match (regexp-quote "a.py:5") text)
                 (string-match (regexp-quote "b.py:2 [warning]") text)))))
  ;; Legacy output: no body object, the raw result is shown verbatim.
  (with-temp-buffer
    (crs--insert-plugin-output-entry
     "Legacy" '((result . "plain text output") (status . "success")))
    (should (string-match-p "plain text output" (buffer-string)))))

;;; --- JSON-RPC transport ---

(defun crs-tests--filter-lines (chunks)
  "Feed CHUNKS through `crs--process-filter', returning the lines dispatched."
  (let ((got nil))
    (setq crs--response-pending nil)
    (cl-letf (((symbol-function 'crs--handle-response)
               (lambda (line) (push line got))))
      (dolist (chunk chunks)
        (crs--process-filter nil chunk)))
    (nreverse got)))

(ert-deftest crs-test-process-filter-reassembles-chunks ()
  "Complete lines are dispatched however the output is chunked."
  ;; One chunk, one line.
  (should (equal (crs-tests--filter-lines '("{\"id\":1}\n")) '("{\"id\":1}")))
  ;; A line split across several chunks is dispatched once, intact.
  (should (equal (crs-tests--filter-lines '("{\"id" "\":1}" "\n"))
                 '("{\"id\":1}")))
  ;; Several lines arriving in a single chunk are dispatched in order.
  (should (equal (crs-tests--filter-lines '("a\nb\nc\n")) '("a" "b" "c")))
  ;; A newline landing mid-chunk splits correctly.
  (should (equal (crs-tests--filter-lines '("a\nb" "c\n")) '("a" "bc")))
  ;; Empty lines are skipped, matching the previous behavior.
  (should (equal (crs-tests--filter-lines '("a\n\n\nb\n")) '("a" "b"))))

(ert-deftest crs-test-process-filter-holds-partial-line ()
  "A trailing partial line is buffered until its newline arrives."
  ;; Nothing dispatched while the line is incomplete.
  (should (equal (crs-tests--filter-lines '("{\"id\":1}")) nil))
  (should crs--response-pending)
  ;; The buffered remainder is prepended to the next chunk.
  (setq crs--response-pending nil)
  (let ((got nil))
    (cl-letf (((symbol-function 'crs--handle-response)
               (lambda (line) (push line got))))
      (crs--process-filter nil "{\"id\"")
      (should (null got))
      (crs--process-filter nil ":1}\nnext")
      (should (equal got '("{\"id\":1}")))
      ;; "next" is still pending, not dispatched.
      (crs--process-filter nil "-line\n")
      (should (equal (nreverse got) '("{\"id\":1}" "next-line")))))
  (should-not crs--response-pending))

(ert-deftest crs-test-process-filter-large-payload-roundtrip ()
  "A large single-line reply survives chunking byte-for-byte.
This is the shape that made the old filter quadratic: many chunks with no
newline until the very end."
  (let* ((payload (json-encode `((jsonrpc . "2.0") (id . 1)
                                 (result . ((content . ,(make-string 200000 ?x)))))))
         (chunks nil)
         (i 0))
    (while (< i (length payload))
      (push (substring payload i (min (length payload) (+ i 4096))) chunks)
      (setq i (+ i 4096)))
    (setq chunks (nreverse (cons "\n" chunks)))
    (should (equal (crs-tests--filter-lines chunks) (list payload)))))

(ert-deftest crs-test-parse-json-matches-json-read ()
  "`crs--parse-json' reproduces `json-read-from-string' semantics."
  (dolist (doc '("{\"a\":1,\"b\":[1,2,3]}"
                 "{\"t\":true,\"f\":false,\"n\":null}"
                 "{\"nested\":{\"deep\":[{\"k\":\"v\"}]}}"
                 "{\"unicode\":\"caf\\u00e9 \\u2014 ok\"}"
                 "{\"empty_arr\":[],\"empty_obj\":{}}"
                 "[1,\"two\",false]"))
    (should (equal (crs--parse-json doc) (json-read-from-string doc))))
  ;; Arrays stay vectors and false stays :json-false, which callers rely on.
  (let ((parsed (crs--parse-json "{\"items\":[1,2],\"ok\":false}")))
    (should (vectorp (cdr (assq 'items parsed))))
    (should (eq (cdr (assq 'ok parsed)) :json-false))
    (should (eq (cdr (assq 'missing parsed)) nil))))

(provide 'crs-tests)
;;; crs-tests.el ends here
