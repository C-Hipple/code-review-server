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
                  crs-review crs-comments crs-review-actions crs-plugins
                  crs-ai))
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
                crs-toggle-annotations crs-add-annotation-as-comment
                crs-sync-pr crs-checkout-current-project
                crs-get-plugin-output crs-rerun-plugin crs-run-on-demand-plugin
                crs-get-ai-output crs-run-ai-feature crs-ai-refresh crs-ai-rerun
                crs-ai-list-features crs-quit-ai-output
                crs-get-rate-limit-status))
    (should (fboundp fn))
    (should (commandp fn))))

(ert-deftest crs-test-internal-helpers-defined ()
  "Cross-module internal helpers resolve after loading."
  (dolist (fn '(crs--send-request crs--render-and-update crs--render-diff
                crs--get-comment-context crs--get-current-review-info
                crs--review-buffer-name crs--parse-hunk-header
                crs--ensure-html crs--make-html-placeholder
                crs--localize-image-srcs crs--image-cache-path
                crs--process-html-placeholders crs--strip-comments-tree
                crs--format-reaction crs--format-reactions
                crs--index-annotations crs--render-annotation-block
                crs--insert-annotations-into-buffer
                crs--format-compact-annotation-indicator
                crs--annotations-on-current-line
                crs--annotations-minibuffer-summary
                crs--annotation-comment-body crs--annotation-choice-label
                crs--select-annotation
                crs--insert-plugin-output-entry
                crs--insert-ai-output crs--ai-should-run-p crs--ai-pending-p
                crs--ai-buffer-name crs--ai-choose-feature
                crs--ai-report-features))
    (should (fboundp fn))))

(ert-deftest crs-test-buffer-local-state-declared ()
  "Buffer-local state variables are declared (centralized in crs-vars)."
  (dolist (var '(crs--process crs--pending-requests crs--review-submits
                 crs-reviews-buffer-name
                 crs--buffer-owner crs--buffer-diff crs--buffer-comments
                 crs--buffer-metadata crs--buffer-show-comments
                 crs--buffer-annotations crs--buffer-show-annotations
                 crs--buffer-images
                 crs--comment-owner crs--comment-filename crs--comment-position
                 crs--plugin-owner crs--plugin-name crs--plugin-output-map
                 crs-ai-features crs--ai-owner crs--ai-feature crs--ai-output
                 crs--ai-poll-timer))
    (should (boundp var))))

;;; --- Modes can be entered without error ---

(ert-deftest crs-test-modes-instantiate ()
  "Each major mode can be activated in a fresh buffer without error."
  (dolist (mode '(crs-list-mode my-code-review-mode comment-edit-mode
                  crs-outdated-comments-mode crs-plugin-output-mode
                  crs-ai-output-mode))
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

(ert-deftest crs-test-ensure-html-keeps-embedded-images ()
  "A screenshot in an otherwise-prose body survives as a tag, not as text."
  (let* ((body "Mid race: <img width=\"1905\" src=\"https://github.com/user-attachments/assets/one\" />\n\nEnd: <img src=\"https://github.com/user-attachments/assets/two\">")
         (out (crs--ensure-html body)))
    (should (string-match-p "<img width=\"1905\" src=\"https://github.com/user-attachments/assets/one\" />" out))
    (should (string-match-p "<img src=\"https://github.com/user-attachments/assets/two\">" out))
    ;; The prose around them is still escaped and paragraphed.
    (should (string-prefix-p "<p>" out))
    (should-not (string-match-p "&lt;img" out)))
  ;; Prose that merely mentions a tag is still escaped.
  (should (string-match-p "&lt;div&gt;" (crs--ensure-html "use <div> here"))))

(ert-deftest crs-test-localize-image-srcs ()
  "Images the server cached are rendered from disk; the rest are left alone."
  (let* ((cached (make-temp-file "crs-image" nil ".png"))
         (crs--buffer-images
          (vector `((url . "https://github.com/user-attachments/assets/one")
                    (path . ,cached)
                    (cached . t))
                  ;; Not downloaded yet: no path to point at.
                  `((url . "https://github.com/user-attachments/assets/two")
                    (path . "")
                    (cached . :json-false)))))
    (unwind-protect
        (let ((out (crs--localize-image-srcs
                    (concat "<img src=\"https://github.com/user-attachments/assets/one\">"
                            "<img src=\"https://github.com/user-attachments/assets/two\">"
                            "<img src=\"https://img.shields.io/badge.svg\">"))))
          (should (string-match-p (concat "src=\"file://" (regexp-quote cached) "\"") out))
          (should (string-match-p "src=\"https://github.com/user-attachments/assets/two\"" out))
          (should (string-match-p "src=\"https://img.shields.io/badge.svg\"" out)))
      (delete-file cached)))
  ;; With nothing cached the body comes back untouched.
  (let ((crs--buffer-images nil))
    (should (equal (crs--localize-image-srcs "<img src=\"https://github.com/x\">")
                   "<img src=\"https://github.com/x\">"))))

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

(ert-deftest crs-test-expanded-annotations-are-findable-at-point ()
  "An expanded block and the line it annotates both carry the annotations."
  (with-temp-buffer
    (insert crs-test--annotation-diff)
    (crs--insert-annotations-into-buffer crs-test--annotations t)
    ;; The annotated diff line itself...
    (goto-char (point-min))
    (search-forward "+line two")
    (should (equal (cdr (assq 'content (car (crs--annotations-on-current-line))))
                   "this line looks wrong"))
    ;; ...and the block rendered beneath it.
    (forward-line 2)
    (should (string-match-p "Security Check"
                            (buffer-substring-no-properties
                             (line-beginning-position) (line-end-position))))
    (should (equal (cdr (assq 'content (car (crs--annotations-on-current-line))))
                   "this line looks wrong"))
    ;; The unanchored block hangs above its hunk header, which is marked too.
    (goto-char (point-min))
    (search-forward "@@ -1,3 +1,4 @@")
    (should (equal (cdr (assq 'content (car (crs--annotations-on-current-line))))
                   "outside the hunk"))))

;;; --- Annotations promoted to local comments ---

(defconst crs-test--washed-annotation-diff
  (concat "modified test.py\n"
          "@@ -1,3 +1,4 @@\n"
          " line one\n"
          "+line two\n"
          " line three\n"
          " line four\n")
  "`crs-test--annotation-diff' after `crs--simplify-diff-headers'.
This is the shape a review buffer holds, and the one the comment-context
parser reads file names from.")

(ert-deftest crs-test-annotation-comment-body ()
  "The comment body names the plugin, then carries the annotation content."
  (should (equal (crs--annotation-comment-body
                  '((plugin . "Security Check") (content . "this line looks wrong")))
                 "Automated comment by Security Check\n\nthis line looks wrong"))
  ;; An annotation carrying no plugin name still yields a well-formed body.
  (should (equal (crs--annotation-comment-body '((content . "hm")))
                 "Automated comment by plugin\n\nhm")))

(ert-deftest crs-test-select-annotation ()
  "A lone annotation is taken directly; several are chosen by numbered label."
  (let ((only '((plugin . "Sec") (content . "x"))))
    (should (eq (crs--select-annotation (list only)) only)))
  (should (equal (crs--annotation-choice-label
                  0 '((plugin . "Sec") (severity . "warning") (content . "bad\nmore")))
                 "1. [Sec/warning] bad"))
  (should (equal (crs--annotation-choice-label 1 '((plugin . "Sec") (content . "bad")))
                 "2. [Sec] bad"))
  (let* ((a '((plugin . "A") (content . "first")))
         (b '((plugin . "B") (content . "second")))
         (offered nil))
    (cl-letf (((symbol-function 'completing-read)
               (lambda (_prompt choices &rest _)
                 (setq offered choices)
                 (nth 1 choices))))
      (should (equal (crs--select-annotation (list a b)) b))
      (should (equal offered '("1. [A] first" "2. [B] second"))))))

(ert-deftest crs-test-add-annotation-as-comment ()
  "The annotation at point is sent as a local comment at the same position."
  (with-temp-buffer
    (insert crs-test--washed-annotation-diff)
    (crs--insert-annotations-into-buffer crs-test--annotations nil)
    (goto-char (point-min))
    (search-forward "+line two")
    (let ((sent nil))
      (cl-letf (((symbol-function 'crs--get-current-review-info)
                 (lambda () (list "C-Hipple" "code-review-server" 83)))
                ((symbol-function 'crs--send-request)
                 (lambda (method params &optional _callback)
                   (setq sent (list method params)))))
        (crs-add-annotation-as-comment))
      (should sent)
      (should (equal (nth 0 sent) "RPCHandler.AddComment"))
      (let ((args (aref (nth 1 sent) 0)))
        (should (equal (cdr (assq 'Owner args)) "C-Hipple"))
        (should (equal (cdr (assq 'Repo args)) "code-review-server"))
        (should (equal (cdr (assq 'Number args)) 83))
        (should (equal (cdr (assq 'Filename args)) "test.py"))
        ;; " line one" is diff position 1, so the annotated "+line two" is 2.
        (should (equal (cdr (assq 'Position args)) 2))
        (should-not (cdr (assq 'ReplyToID args)))
        (should (equal (cdr (assq 'Body args))
                       "Automated comment by Security Check\n\nthis line looks wrong"))))))

(ert-deftest crs-test-add-annotation-as-comment-needs-an-anchored-annotation ()
  "Lines with no annotation, and unanchored annotations, send nothing."
  (with-temp-buffer
    (insert crs-test--washed-annotation-diff)
    (crs--insert-annotations-into-buffer crs-test--annotations nil)
    (cl-letf (((symbol-function 'crs--get-current-review-info)
               (lambda () (list "C-Hipple" "code-review-server" 83)))
              ((symbol-function 'crs--send-request)
               (lambda (&rest _) (ert-fail "should not send a request"))))
      ;; A plain diff line carries no annotation.
      (goto-char (point-min))
      (search-forward "line three")
      (should-error (crs-add-annotation-as-comment) :type 'user-error)
      ;; The out-of-hunk annotation sits on a hunk header, which has no diff
      ;; position for a comment to anchor to.
      (goto-char (point-min))
      (search-forward "@@ -1,3 +1,4 @@")
      (should (crs--annotations-on-current-line))
      (should-error (crs-add-annotation-as-comment) :type 'user-error))))

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

;;; --- Reactions ---
;;
;; A thumbs-up on a comment is an acknowledgement that never arrives as a
;; reply, so the formatting of *who* reacted is what these pin down.

(ert-deftest crs-test-format-reactions-empty ()
  "Nothing reacted to renders no line at all, not an empty label."
  (should-not (crs--format-reactions nil))
  (should-not (crs--format-reactions [])))

(ert-deftest crs-test-format-reactions-names-reactors ()
  (should (equal (crs--format-reactions
                  (vector '((content . "+1") (emoji . "👍")
                            (users . ["alice" "bob"]) (count . 2)
                            (viewer_reacted . t))))
                 "Reactions: 👍 alice, bob")))

(ert-deftest crs-test-format-reactions-joins-emoji ()
  (should (equal (crs--format-reactions
                  (vector '((content . "+1") (emoji . "👍")
                            (users . ["alice"]) (count . 1))
                          '((content . "eyes") (emoji . "👀")
                            (users . ["bob"]) (count . 1))))
                 "Reactions: 👍 alice  👀 bob")))

(ert-deftest crs-test-format-reactions-reports-truncated-reactors ()
  "The server caps the logins it fetches, so the count can run ahead of them."
  (should (equal (crs--format-reactions
                  (vector '((content . "heart") (emoji . "❤️")
                            (users . ["alice"]) (count . 4))))
                 "Reactions: ❤️ alice +3 more")))

(ert-deftest crs-test-format-reactions-falls-back-to-name ()
  "An emoji the server did not recognise still renders under its name."
  (should (equal (crs--format-reactions
                  (vector '((content . "party_popper") (emoji . "")
                            (users . ["carol"]) (count . 1))))
                 "Reactions: :party_popper: carol")))

(ert-deftest crs-test-comment-tree-renders-reactions ()
  "Reaction lines appear under the comment they belong to, root and reply."
  (let ((rendered
         (crs--render-comment-tree
          (list '((id . "1") (author . "alice") (path . "a.go") (position . "3")
                  (body . "please rename this")
                  (reactions . [((content . "+1") (emoji . "👍")
                                 (users . ["bob"]) (count . 1))]))
                '((id . "2") (author . "bob") (body . "done")
                  (reactions . [((content . "eyes") (emoji . "👀")
                                 (users . ["alice"]) (count . 1))]))))))
    (should (string-match-p "👍 bob" rendered))
    (should (string-match-p "👀 alice" rendered))))

(ert-deftest crs-test-comment-tree-without-reactions ()
  "A comment nobody reacted to gains no extra line."
  (let ((rendered
         (crs--render-comment-tree
          (list '((id . "1") (author . "alice") (path . "a.go") (position . "3")
                  (body . "please rename this"))))))
    (should-not (string-match-p "Reactions:" rendered))))

(ert-deftest crs-test-conversation-renders-reactions ()
  "Conversation comments and review bodies both carry their reactions."
  (let ((rendered
         (crs--render-conversation-from-data
          (vector '((id . "1") (author . "alice") (path . "")
                    (created_at . "2026-01-01T00:00:00Z")
                    (body . "shipping this")
                    (reactions . [((content . "rocket") (emoji . "🚀")
                                   (users . ["bob"]) (count . 1))])))
          (vector '((id . 900) (user . "bob") (state . "APPROVED")
                    (submitted_at . "2026-01-02T00:00:00Z")
                    (body . "nice work")
                    (reactions . [((content . "hooray") (emoji . "🎉")
                                   (users . ["alice"]) (count . 1))]))))))
    (should (string-match-p "🚀 bob" rendered))
    (should (string-match-p "🎉 alice" rendered))))

;;; --- AI feature reports ---

(defun crs-test--ai-output (&rest overrides)
  "A GetAIOutput entry for comments-addressed, with OVERRIDES applied.
OVERRIDES is a plist of field symbols and values."
  (let ((output (list (cons 'feature "comments-addressed")
                      (cons 'name "Comments addressed?")
                      (cons 'status "success")
                      (cons 'body '((body_type . "markdown")
                                    (body_content . "**1 outstanding of 1 item(s); 0 addressed.**")))
                      (cons 'annotations [])
                      (cons 'report '((verdict . "outstanding")))
                      (cons 'stale :json-false)
                      (cons 'truncated :json-false)
                      (cons 'updated_at "2026-09-01T10:00:00Z"))))
    (while overrides
      (setf (alist-get (pop overrides) output) (pop overrides)))
    output))

(ert-deftest crs-test-ai-should-run ()
  "A feature runs on open when it never ran or went stale, never while pending."
  (should (crs--ai-should-run-p (crs-test--ai-output 'status "not-run")))
  (should (crs--ai-should-run-p (crs-test--ai-output 'stale t)))
  (should-not (crs--ai-should-run-p (crs-test--ai-output)))
  (should-not (crs--ai-should-run-p (crs-test--ai-output 'status "pending" 'stale t)))
  (should-not (crs--ai-should-run-p nil))
  (should (crs--ai-pending-p (crs-test--ai-output 'status "pending"))))

(ert-deftest crs-test-ai-output-rendering ()
  "The report body renders as-is, with the stale warning when it applies."
  (let ((rendered (with-temp-buffer
                    (crs--insert-ai-output (crs-test--ai-output 'stale t))
                    (buffer-string))))
    (should (string-match-p "^# Comments addressed\\? (Status: success)" rendered))
    (should (string-match-p "1 outstanding of 1 item" rendered))
    (should (string-match-p "Press R to re-run" rendered)))
  (let ((fresh (with-temp-buffer
                 (crs--insert-ai-output (crs-test--ai-output))
                 (buffer-string))))
    (should-not (string-match-p "re-run" fresh))))

(ert-deftest crs-test-ai-output-pending ()
  "A pending run says so, over the previous report when there is one."
  (let ((first-run (with-temp-buffer
                     (crs--insert-ai-output
                      (crs-test--ai-output 'status "pending"
                                           'body '((body_type . "markdown") (body_content . ""))
                                           'report nil 'updated_at ""))
                     (buffer-string)))
        (refresh (with-temp-buffer
                   (crs--insert-ai-output (crs-test--ai-output 'status "pending"))
                   (buffer-string))))
    (should (string-match-p "Running\\.\\.\\." first-run))
    (should (string-match-p "Refreshing" refresh))
    (should (string-match-p "1 outstanding" refresh))))

(ert-deftest crs-test-ai-output-annotations ()
  "Annotations are listed for a feature without a typed report, not for one with."
  (let* ((annotations [((filename . "src/main.ts") (line . 4) (severity . "warning")
                        (content . "look here") (source . "ai"))])
         (plain (with-temp-buffer
                  (crs--insert-ai-output
                   (crs-test--ai-output 'report nil 'annotations annotations))
                  (buffer-string)))
         (typed (with-temp-buffer
                  (crs--insert-ai-output (crs-test--ai-output 'annotations annotations))
                  (buffer-string))))
    (should (string-match-p "## Annotations (1)" plain))
    (should (string-match-p "src/main.ts:4 \\[warning\\] look here" plain))
    (should-not (string-match-p "Annotations" typed))))

(ert-deftest crs-test-ai-choose-feature ()
  "One enabled feature is picked without asking; none is an error."
  (let ((only '((id . "comments-addressed") (name . "Comments addressed?"))))
    (should (equal (crs--ai-choose-feature (list only)) only)))
  (should-error (crs--ai-choose-feature nil) :type 'user-error)
  (should (equal (crs--ai-buffer-name "comments-addressed" "acme" "widgets" 42)
                 "* AI: comments-addressed acme/widgets #42 *")))

(ert-deftest crs-test-ai-report-features ()
  "Only enabled features the server doesn't apply itself are offered."
  (let ((features (list '((id . "comments-addressed") (enabled . t) (applied . :json-false))
                        '((id . "feature-flags") (enabled . :json-false))
                        '((id . "file-ordering") (enabled . t) (applied . t))
                        '((id . "review-ease") (enabled . t) (applied . t))
                        ;; A server that predates `applied' leaves it out.
                        '((id . "older") (enabled . t)))))
    (should (equal (mapcar (lambda (f) (cdr (assq 'id f)))
                           (crs--ai-report-features features))
                   '("comments-addressed" "older")))))

(ert-deftest crs-test-ai-keys-bound ()
  "The AI commands are reachable from the review and AI output buffers."
  (should (eq (lookup-key my-code-review-mode-map (kbd "C")) #'crs-get-ai-output))
  (should (eq (lookup-key crs-ai-output-mode-map (kbd "r")) #'crs-ai-refresh))
  (should (eq (lookup-key crs-ai-output-mode-map (kbd "R")) #'crs-ai-rerun))
  (should (eq (lookup-key crs-ai-output-mode-map (kbd "q")) #'crs-quit-ai-output)))

(ert-deftest crs-test-ai-killing-the-buffer-stops-polling ()
  "A pending poll timer does not outlive its buffer."
  (let ((buffer (generate-new-buffer "crs-ai-test")))
    (with-current-buffer buffer
      (crs-ai-output-mode)
      (setq crs--ai-poll-timer (run-with-timer 3600 nil #'ignore)))
    (let ((timer (buffer-local-value 'crs--ai-poll-timer buffer)))
      (kill-buffer buffer)
      (should-not (memq timer timer-list)))))

(defmacro crs-test--with-fake-rpc (replies &rest body)
  "Run BODY with RPCs answered from REPLIES and timers recorded, not run.
REPLIES maps a method name to the result its callback receives.  Binds
`calls' to the (METHOD . PARAMS) sent, most recent first, and `timers' to
the functions scheduled."
  (declare (indent 1))
  `(let ((calls '())
         (timers '()))
     (cl-letf (((symbol-function 'crs--send-request)
                (lambda (method params callback)
                  (push (cons method params) calls)
                  (funcall callback (cdr (assoc method ,replies)))))
               ((symbol-function 'run-with-timer)
                (lambda (_secs _repeat fn &rest _args)
                  (push fn timers)
                  'crs-test-timer))
               ((symbol-function 'pop-to-buffer) #'ignore))
       ,@body)))

(defconst crs-test--ai-feature '((id . "comments-addressed") (name . "Comments addressed?")))

(ert-deftest crs-test-ai-runs-a-feature-that-never-ran-and-polls ()
  "Opening a feature with no result asks for a run, then polls while pending."
  (crs-test--with-fake-rpc
      `(("RPCHandler.GetAIOutput"
         . ((output . ((comments-addressed
                        . ,(crs-test--ai-output 'status "not-run" 'report nil
                                                'body '((body_content . ""))))))))
        ("RPCHandler.RunAIFeature"
         . ((okay . t) (outcome . "started")
            (output . ,(crs-test--ai-output 'status "pending" 'report nil
                                            'body '((body_content . "")))))))
    (let ((buffer (crs--ai-open "acme" "widgets" 42 crs-test--ai-feature)))
      (unwind-protect
          (progn
            (crs--ai-fetch buffer t)
            (should (equal (mapcar #'car (reverse calls))
                           '("RPCHandler.GetAIOutput" "RPCHandler.RunAIFeature")))
            ;; Not forced: the server may answer from its cache.
            (should (equal (json-encode (cdar calls))
                           "[{\"Owner\":\"acme\",\"Repo\":\"widgets\",\"Number\":42,\"Feature\":\"comments-addressed\"}]"))
            (should (= (length timers) 1))
            (with-current-buffer buffer
              (should (string-match-p "Running" (buffer-string)))))
        (kill-buffer buffer)))))

(ert-deftest crs-test-ai-current-report-needs-no-run ()
  "A current report is shown without a run or a poll."
  (crs-test--with-fake-rpc
      `(("RPCHandler.GetAIOutput"
         . ((output . ((comments-addressed . ,(crs-test--ai-output)))))))
    (let ((buffer (crs--ai-open "acme" "widgets" 42 crs-test--ai-feature)))
      (unwind-protect
          (progn
            (crs--ai-fetch buffer t)
            (should (equal (mapcar #'car calls) '("RPCHandler.GetAIOutput")))
            (should-not timers)
            (with-current-buffer buffer
              (should (string-match-p "1 outstanding of 1 item" (buffer-string)))
              (should buffer-read-only)))
        (kill-buffer buffer)))))

(ert-deftest crs-test-ai-rerun-forces ()
  "Re-running from the AI buffer forces a fresh run."
  (crs-test--with-fake-rpc
      `(("RPCHandler.RunAIFeature"
         . ((okay . t) (outcome . "started")
            (output . ,(crs-test--ai-output 'status "pending")))))
    (let ((buffer (crs--ai-open "acme" "widgets" 42 crs-test--ai-feature)))
      (unwind-protect
          (with-current-buffer buffer
            (crs-ai-rerun)
            (should (equal (json-encode (cdar calls))
                           "[{\"Owner\":\"acme\",\"Repo\":\"widgets\",\"Number\":42,\"Feature\":\"comments-addressed\",\"Force\":true}]"))
            (should (string-match-p "Refreshing" (buffer-string))))
        (kill-buffer buffer)))))

;;; --- Submitting a review ---

(defmacro crs-test--with-held-rpc (&rest body)
  "Run BODY with RPCs held for BODY to answer, and output recorded.
Binds `sent' to the (METHOD PARAMS CALLBACK) of each request, most recent
first, and `events' to what the user was shown, most recent first: each is
\(message . TEXT) or (render BUFFER CONTENT INHIBIT-MESSAGE)."
  (declare (indent 0))
  `(let ((sent '())
         (events '()))
     (cl-letf (((symbol-function 'crs--send-request)
                (lambda (method params callback)
                  (push (list method params callback) sent)))
               ((symbol-function 'message)
                (lambda (format-string &rest args)
                  (when format-string
                    (push (cons 'message (apply #'format-message format-string args))
                          events))))
               ((symbol-function 'crs--render-and-update)
                (lambda (buffer content &rest _)
                  (push (list 'render buffer content inhibit-message) events))))
       ,@body)))

(defmacro crs-test--with-review-buffer (feedback &rest body)
  "Run BODY in a fresh review buffer for acme/widgets #42 holding FEEDBACK.
The buffer is bound to `buffer' and killed afterwards."
  (declare (indent 1))
  `(let ((buffer (get-buffer-create (crs--review-buffer-name "acme" "widgets" 42))))
     (clrhash crs--review-submits)
     (unwind-protect
         (progn
           (with-current-buffer buffer
             (setq crs--buffer-review-feedback ,feedback))
           ,@body)
       (kill-buffer buffer))))

(defconst crs-test--submit-reply
  '((okay . t)
    (metadata . ((number . 42) (title . "Widgets")))
    (reviews . [((id . 1) (user . "me") (state . "APPROVED"))]))
  "A SubmitReview reply: the PR as the server refetched it.")

(ert-deftest crs-test-review-submitted-message ()
  "A finished submit names its PR, and the replies it left behind."
  (should (equal (crs--review-submitted-message "APPROVE" "acme/widgets #42" nil)
                 "Approved acme/widgets #42"))
  (should (equal (crs--review-submitted-message "REQUEST_CHANGES" "acme/widgets #42" [])
                 "Requested changes on acme/widgets #42"))
  (should (equal (crs--review-submitted-message "COMMENT" "acme/widgets #42" nil)
                 "Review submitted on acme/widgets #42"))
  (should (equal (crs--review-submitted-message
                  "APPROVE" "acme/widgets #42" ["reply to comment 7: 422 Validation Failed"])
                 (concat "Approved acme/widgets #42, but 1 reply could not be posted"
                         " and is still pending: reply to comment 7: 422 Validation Failed")))
  (should (equal (crs--review-submitted-message "COMMENT" "acme/widgets #42" ["a" "b"])
                 (concat "Review submitted on acme/widgets #42, but 2 replies could not"
                         " be posted and are still pending: a; b"))))

(ert-deftest crs-test-submit-review-runs-in-the-background ()
  "Submitting returns once the request is sent; the reply is announced, then drawn."
  (crs-test--with-review-buffer "Ship it"
    (crs-test--with-held-rpc
      (with-current-buffer buffer
        (crs-submit-review "APPROVE"))
      (should (= (length sent) 1))
      (should (equal (nth 0 (car sent)) "RPCHandler.SubmitReview"))
      (should (equal (json-encode (nth 1 (car sent)))
                     "[{\"Owner\":\"acme\",\"Repo\":\"widgets\",\"Number\":42,\"Event\":\"APPROVE\",\"Body\":\"Ship it\"}]"))
      ;; Nothing has come back yet: the reviewer is told it is under way,
      ;; and the buffer is as it was.
      (should (equal events '((message . "Approving acme/widgets #42..."))))
      (should (equal (buffer-local-value 'crs--buffer-review-feedback buffer) "Ship it"))
      ;; The server replies: the outcome is shown, then the buffer is redrawn
      ;; from the reply with the outcome kept in the echo area.
      (funcall (nth 2 (car sent)) crs-test--submit-reply)
      (should (equal (reverse events)
                     `((message . "Approving acme/widgets #42...")
                       (message . "Approved acme/widgets #42")
                       (render ,buffer ,crs-test--submit-reply t))))
      (should-not (buffer-local-value 'crs--buffer-review-feedback buffer)))))

(ert-deftest crs-test-submit-review-failure-keeps-the-draft ()
  "A submit the server refuses says so, and leaves the feedback to retry with."
  (crs-test--with-review-buffer "Ship it"
    (crs-test--with-held-rpc
      (with-current-buffer buffer
        (crs-submit-review "REQUEST_CHANGES"))
      (funcall (nth 2 (car sent)) '((error . "GitHub said no")))
      (should (equal (car events)
                     '(message . "Review on acme/widgets #42 was not submitted: GitHub said no")))
      (should-not (assq 'render events))
      (should (equal (buffer-local-value 'crs--buffer-review-feedback buffer) "Ship it")))))

(ert-deftest crs-test-submit-review-keeps-a-draft-written-mid-submit ()
  "Feedback written while the submit was in flight is not the feedback that went out."
  (crs-test--with-review-buffer "Ship it"
    (crs-test--with-held-rpc
      (with-current-buffer buffer
        (crs-submit-review "COMMENT")
        (setq crs--buffer-review-feedback "One more thing"))
      (funcall (nth 2 (car sent)) crs-test--submit-reply)
      (should (equal (buffer-local-value 'crs--buffer-review-feedback buffer)
                     "One more thing")))))

(ert-deftest crs-test-submit-review-one-at-a-time ()
  "A PR with a submit in flight takes no other until the reply lands."
  (let ((crs--process (make-pipe-process :name "crs-test-server" :noquery t)))
    (unwind-protect
        (crs-test--with-review-buffer "Ship it"
          (crs-test--with-held-rpc
            (with-current-buffer buffer
              (crs-submit-review "APPROVE")
              (should-error (crs-submit-review "APPROVE") :type 'user-error)
              (should (= (length sent) 1))
              ;; Answered, even with an error: the PR can be submitted again.
              (funcall (nth 2 (car sent)) '((error . "GitHub said no")))
              (crs-submit-review "APPROVE")
              (should (= (length sent) 2))
              ;; A server that exits takes its unanswered submits with it.
              (delete-process crs--process)
              (crs-submit-review "APPROVE")
              (should (= (length sent) 3)))))
      (delete-process crs--process))))

(ert-deftest crs-test-submit-review-reply-after-the-buffer-is-gone ()
  "A reply for a review buffer that was closed is still announced."
  (crs-test--with-review-buffer "Ship it"
    (crs-test--with-held-rpc
      (with-current-buffer buffer
        (crs-submit-review "APPROVE"))
      (kill-buffer buffer)
      (funcall (nth 2 (car sent)) crs-test--submit-reply)
      (should (equal (car events) '(message . "Approved acme/widgets #42")))
      (should-not (assq 'render events)))))

(provide 'crs-tests)
;;; crs-tests.el ends here
