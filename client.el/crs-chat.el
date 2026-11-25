;;; crs-chat.el --- The review chat: a conversation with a local agent about a PR. -*- lexical-binding: t; -*-

;;; Commentary:

;; The server can run a local coding agent to chat about the PR under review
;; (docs/protocol.md#review-chat): Claude Code, Antigravity, or any agent that
;; speaks the Agent Client Protocol.  Which agent is the server config's
;; choice ([Chat] Agent), not the client's.  The server holds the
;; conversations and runs the agent; this module shows them.
;;
;; `crs-chat' (`t' in a review buffer) opens the PR's chat in a window split
;; off the bottom of the review's: the review keeps the top three quarters
;; (`crs-chat-window-height').  The buffer, in `crs-chat-mode' (markdown-mode
;; underneath), holds the conversation as Markdown, read-only, with a line
;; under it and the message being written below that.  C-c C-c sends it.
;;
;; A PR holds any number of chats — sessions, each a conversation of its
;; own with the agent.  Opening a PR's chat shows its most recent session,
;; or a new one when it has none; the header line says which of the PR's
;; sessions it is ("Session 2/3").  `crs-chat-new' clears the context with
;; a new session, `crs-chat-fork' carries this one into a new session (and
;; the PR's latest diff), `crs-chat-previous-session' / `crs-chat-next-session'
;; step between them, and `crs-chat-sessions' lists them all in a buffer of
;; their own, to open, fork, rename or delete one.
;;
;; A message is stored as soon as it is sent and the agent
;; answers in the background: the server announces each step of the answer
;; (Server.ChatChanged), and the buffer reads the chat again on each, so the
;; reply streams in without polling.  Only what changed is redrawn — while a
;; reply streams, the text it grew by — and the message being written, point,
;; each window's place and the input's undo history are left as they were.
;; A window whose point is in the input follows the conversation down.

;;; Code:

(require 'cl-lib)
(require 'parse-time)
(require 'tabulated-list)
(require 'crs-vars)
(require 'crs-rpc)
(require 'crs-diagram)
(require 'markdown-mode)

(declare-function evil-insert-state "evil-states")

(defface crs-chat-prompt-face
  '((t :inherit shadow))
  "Face for the line between a chat's conversation and the message being written."
  :group 'crs)

(defvar-keymap crs-chat-mode-map
  :doc "Keymap for `crs-chat-mode'.  Bindings are defined in crs-client.el.")

(define-derived-mode crs-chat-mode markdown-mode "CRS-Chat"
  "Major mode for a review chat with a local agent about a PR.
The conversation is Markdown, read-only; the message being written goes
below the line under it.
\\{crs-chat-mode-map}"
  (setq crs--chat-input-start (copy-marker (point-max)))
  ;; The transcript is `rear-nonsticky', so that typing just after it
  ;; isn't editing it.  markdown-mode's font-lock manages that property,
  ;; for its hidden link markup, and would strip it from the transcript
  ;; as it fontifies; here it leaves it be.
  (setq-local font-lock-defaults
              (mapcar (lambda (entry)
                        (if (eq (car-safe entry) 'font-lock-extra-managed-props)
                            (cons (car entry) (remq 'rear-nonsticky (cdr entry)))
                          entry))
                      font-lock-defaults))
  (setq-local font-lock-extra-managed-props
              (remq 'rear-nonsticky font-lock-extra-managed-props))
  (setq-local header-line-format '(:eval (crs--chat-header-line)))
  (visual-line-mode 1))


;;; Agents and models

(defun crs--chat-backends ()
  "The chat agents the server offers, as a list."
  (append crs-chat-backends nil))

(defun crs--chat-available-p (backend)
  "Non-nil when the server can run BACKEND."
  (eq (cdr (assq 'available backend)) t))

(defun crs--chat-find-backend (id)
  "The agent whose ID is ID, or nil."
  (seq-find (lambda (b) (equal (cdr (assq 'id b)) id)) (crs--chat-backends)))

(defun crs--chat-agent-backend (backends agent)
  "The agent of BACKENDS new chats talk to: the one whose ID is AGENT.
An AGENT the server names but doesn't list stands in as unavailable, so
the buffer says why.  AGENT nil or empty (an older server that doesn't
say) takes the first agent it can run, else the first listed."
  (if (and (stringp agent) (not (string-empty-p agent)))
      (or (seq-find (lambda (b) (equal (cdr (assq 'id b)) agent)) backends)
          `((id . ,agent) (name . ,agent) (available . :json-false)
            (reason . "the server's config names it as the chat agent, but doesn't define it.")
            (models . []) (default_model . "")))
    (or (seq-find #'crs--chat-available-p backends)
        (car backends))))

(defun crs--chat-models (backend)
  "The models BACKEND offers, as a list of alists with `id' and `name'."
  (append (cdr (assq 'models backend)) nil))

(defun crs--chat-pick-model (backend preferred)
  "The model a new chat with BACKEND asks for.
PREFERRED, a model's ID, when BACKEND offers it; else its default."
  (cond ((null backend) "")
        ((and preferred
              (seq-find (lambda (m) (equal (cdr (assq 'id m)) preferred))
                        (crs--chat-models backend)))
         preferred)
        (t (or (cdr (assq 'default_model backend)) ""))))

(defun crs--chat-model-label (backend model)
  "How MODEL reads: its name in BACKEND's list, else its ID, else \"Default\"."
  (let ((found (seq-find (lambda (m) (equal (cdr (assq 'id m)) model))
                         (crs--chat-models backend))))
    (cond (found (cdr (assq 'name found)))
          ((and (stringp model) (not (string-empty-p model))) model)
          (t "Default"))))

(defun crs--chat-ensure-choice ()
  "Choose the agent and model of the buffer's next new chat.
Once the agents are known: the agent is the server's, and the model the
last one chosen for it this session, else `crs-chat-model'."
  (when (and crs-chat-backends (null crs--chat-backend))
    (let* ((last crs--chat-last-choice)
           (backend (crs--chat-agent-backend (crs--chat-backends) crs-chat-agent))
           (id (cdr (assq 'id backend))))
      (when backend
        (setq crs--chat-backend id
              crs--chat-model (crs--chat-pick-model
                               backend
                               (if (and last (equal (car last) id))
                                   (cdr last)
                                 crs-chat-model)))))))

(defun crs--chat-backend-id (&optional chat)
  "ID of CHAT's agent; CHAT is the buffer's, else its next new chat's."
  (let ((chat (or chat crs--chat)))
    (if chat (cdr (assq 'backend chat)) crs--chat-backend)))

(defun crs--chat-next-backend ()
  "The agent the buffer's next new chat talks to, once the agents are known."
  (and crs-chat-backends crs--chat-backend
       (crs--chat-agent-backend (crs--chat-backends) crs--chat-backend)))

(defun crs--chat-backend-shown (&optional chat)
  "The agent of CHAT, by default the buffer's chat, or else its next new chat."
  (let ((chat (or chat crs--chat)))
    (if chat
        (crs--chat-find-backend (cdr (assq 'backend chat)))
      (crs--chat-next-backend))))

(defun crs--chat-agent-name (&optional chat)
  "The name of the agent CHAT talks to; CHAT is as for `crs--chat-backend-shown'."
  (let ((backend (crs--chat-backend-shown chat))
        (id (crs--chat-backend-id chat)))
    (or (cdr (assq 'name backend))
        (and (stringp id) (not (string-empty-p id)) id)
        "Agent")))

(defun crs--chat-with-backends (callback)
  "Call CALLBACK once the chat agents are known, fetching them the first time.
A server that can't list them is taken to offer none."
  (if crs-chat-backends
      (funcall callback)
    (crs--send-request
     "RPCHandler.ListChatBackends"
     (vector)
     (lambda (result)
       (let ((err (crs--chat-reply-error result)))
         (when err
           (message "Could not list the chat agents: %s" err))
         (setq crs-chat-backends (or (and (not err) (cdr (assq 'backends result)))
                                     [])
               crs-chat-agent (and (not err) (cdr (assq 'agent result)))))
       (funcall callback)))))


;;; Small helpers

(defun crs--chat-buffer-name (owner repo number)
  "Name of the buffer holding the chat about OWNER/REPO #NUMBER."
  (format "* CRS Chat: %s/%s #%d *" owner repo number))

(defun crs--chat-reply-error (result)
  "The error message in RESULT, or nil when it is a normal reply."
  (let ((err (cdr (assq 'error result))))
    (cond ((null err) nil)
          ((stringp err) err)
          (t (format "%s" (or (cdr (assq 'message err)) err))))))

(defun crs--chat-ticket ()
  "A new ticket for a request whose reply carries a chat."
  (setq crs--chat-tickets (1+ crs--chat-tickets)))

(defun crs--chat-running-p (&optional buffer)
  "Non-nil when the agent is answering in BUFFER's chat (default: this buffer's)."
  (equal (cdr (assq 'status (buffer-local-value 'crs--chat (or buffer (current-buffer)))))
         "running"))

(defun crs--chat-stale-p (chat)
  "Non-nil when the PR has moved on since CHAT attached its diff."
  (let ((head (cdr (assq 'head_sha chat)))
        (current (cdr (assq 'current_sha chat))))
    (and (stringp head) (not (string-empty-p head))
         (stringp current) (not (string-empty-p current))
         (not (equal head current)))))

(defun crs--chat-format-cost (usd)
  "USD as a cost worth showing, or nil for a chat that cost nothing yet."
  (cond ((not (numberp usd)) nil)
        ((<= usd 0) nil)
        ((< usd 0.01) "<$0.01")
        (t (format "$%.2f" usd))))

(defun crs--chat-parse-time (string)
  "STRING, an RFC 3339 time, as a Lisp time; nil when it isn't one."
  (and (stringp string) (not (string-empty-p string))
       (ignore-errors (parse-iso8601-time-string string))))

(defun crs--chat-elapsed (started-at &optional now)
  "How long ago STARTED-AT (RFC 3339) was, as \"12s\" or \"3m 05s\".
NOW is the time to count to, by default the current time."
  (let ((started (crs--chat-parse-time started-at)))
    (if (null started)
        ""
      (let ((seconds (max 0 (floor (float-time (time-subtract (or now (current-time))
                                                               started))))))
        (if (< seconds 60)
            (format "%ds" seconds)
          (format "%dm %02ds" (/ seconds 60) (% seconds 60)))))))

(defun crs--chat-keys (command)
  "The key that runs COMMAND in a chat buffer, as text."
  (substring-no-properties
   (substitute-command-keys (format "\\<crs-chat-mode-map>\\[%s]" command))))


;;; The transcript

(defun crs--chat-quote (text)
  "TEXT with every line after the first quoted, to sit in a Markdown blockquote."
  (replace-regexp-in-string "\n" "\n> " text t t))

(defun crs--chat-close-fence (text)
  "TEXT, with the code fence it leaves open closed.
A reply being written can stop inside a code block, and an open fence
would draw the rest of the buffer, the message being written included,
as code."
  (let ((fence nil)
        (start 0))
    (while (string-match "^[ \t]*\\(```+\\|~~~+\\)" text start)
      (let ((marker (match-string 1 text)))
        (cond ((null fence) (setq fence marker))
              ((string-prefix-p fence marker) (setq fence nil))))
      (setq start (match-end 0)))
    (if fence (concat text "\n" fence) text)))

(defun crs--chat-attached-label (message)
  "What a reviewer's MESSAGE attached for the agent, in words, or nil."
  (let* ((attached (append (cdr (assq 'attached message)) nil))
         (parts (delq nil (list (when (member "description" attached) "description")
                                (when (member "diff" attached)
                                  (if (eq (cdr (assq 'diff_truncated message)) t)
                                      "diff (cut to fit)"
                                    "diff")))))
         (sha (cdr (assq 'head_sha message))))
    (when parts
      (concat "PR " (string-join parts " and ") " attached"
              (if (and (stringp sha) (not (string-empty-p sha)))
                  (format " at %s" (substring sha 0 (min 7 (length sha))))
                "")))))

(defun crs--chat-tool-line (message)
  "A blockquote line saying what tool MESSAGE used, on what, and how it went."
  (let* ((status (or (cdr (assq 'tool_status message)) "done"))
         (mark (pcase status ("running" "…") ("error" "✗") (_ "✓")))
         (tool (or (cdr (assq 'tool message)) ""))
         (content (string-trim (or (cdr (assq 'content message)) ""))))
    (concat "> " mark
            (if (string-empty-p tool) "" (concat " " tool))
            (if (string-empty-p content)
                ""
              (concat " " (replace-regexp-in-string "\n" " " content t t)))
            "\n")))

(defun crs--chat-intro ()
  "What the buffer shows before its chat has started."
  (let* ((backend (crs--chat-backend-shown))
         (name (or (cdr (assq 'name backend)) "the agent")))
    (cond
     ((null crs-chat-backends) "Loading the chat agents…\n\n")
     ((null backend) "The server offers no chat agents.\n\n")
     ((not (crs--chat-available-p backend))
      (format "**%s isn't available.** %s\n\n"
              name (or (cdr (assq 'reason backend)) "")))
     (t
      (format (concat "Ask %s about %s/%s #%s.  Your first message goes with the PR's "
                      "description and diff, and %s can read the repository, but not "
                      "change anything.\n\n"
                      "*%s sends · %s picks the model · %s lists the PR's sessions · %s closes*\n\n")
              name crs--chat-owner crs--chat-repo crs--chat-number name
              (crs--chat-keys 'crs-chat-send)
              (crs--chat-keys 'crs-chat-select-model)
              (crs--chat-keys 'crs-chat-sessions)
              (crs--chat-keys 'crs-chat-quit))))))

(defun crs--chat-transcript (chat)
  "The Markdown showing CHAT, a Chat from GetChat.
Each turn is a level-1 heading: `# You' over the reviewer's message, the
agent's name over its answer, so markdown-mode's heading commands step
from one to the next.  The tools the agent used are blockquote lines, and
the reply being written follows the last of them the way it will read
once it is stored, so the text only grows as it streams.  CHAT nil is a
new chat: the buffer's intro, or a note that the chat is loading."
  (cond
   ((null chat)
    (if crs--chat-id "Loading the chat…\n\n" (crs--chat-intro)))
   (t
    (let ((agent (crs--chat-agent-name chat))
          (parts '())
          (in-answer nil)
          (in-quote nil))
      (dolist (message (append (cdr (assq 'messages chat)) nil))
        (let ((role (cdr (assq 'role message)))
              (content (string-trim-right (or (cdr (assq 'content message)) ""))))
          (when (and in-quote (not (equal role "tool")))
            (push "\n" parts)
            (setq in-quote nil))
          (if (equal role "user")
              (let ((label (crs--chat-attached-label message)))
                (setq in-answer nil)
                (push (concat "# You\n\n" content "\n\n") parts)
                (when label
                  (push (format "*%s*\n\n" label) parts)))
            (unless in-answer
              (push (format "# %s\n\n" agent) parts)
              (setq in-answer t))
            (pcase role
              ("tool"
               (push (crs--chat-tool-line message) parts)
               (setq in-quote t))
              ("error"
               (push (format "> **Error:** %s\n\n" (crs--chat-quote content)) parts))
              ("notice"
               (push (format "> *%s*\n\n" (crs--chat-quote content)) parts))
              (_
               (unless (string-empty-p content)
                 (push (concat content "\n\n") parts)))))))
      (when (equal (cdr (assq 'status chat)) "running")
        (let ((draft (string-trim-right (or (cdr (assq 'draft chat)) ""))))
          (when in-quote
            (push "\n" parts)
            (setq in-quote nil))
          (unless in-answer
            (push (format "# %s\n\n" agent) parts))
          (push (if (string-empty-p draft)
                    "*Working…*\n\n"
                  (concat (crs--chat-close-fence draft) "\n\n"))
                parts)))
      (when in-quote
        (push "\n" parts))
      (when (crs--chat-stale-p chat)
        (push (format (concat "> **The PR has new commits since this session attached its diff.**  "
                              "%s forks it with the latest diff; %s starts a new session.\n\n")
                      (crs--chat-keys 'crs-chat-fork)
                      (crs--chat-keys 'crs-chat-new))
              parts))
      (apply #'concat (nreverse parts))))))


;;; Drawing

(defun crs--chat-common-suffix (a b floor)
  "Length of the longest common suffix of strings A and B.
It leaves the first FLOOR characters of each out, which they share."
  (let ((i (length a))
        (j (length b))
        (n 0))
    (while (and (> i floor) (> j floor)
                (eq (aref a (1- i)) (aref b (1- j))))
      (setq i (1- i)
            j (1- j)
            n (1+ n)))
    n))

(defun crs--chat-shift-undo (from delta)
  "Move the positions at or after FROM in `buffer-undo-list' by DELTA.
The transcript is redrawn without recording undo, so the input's undo
entries, which record positions after it, must move with it.  Entries
are changed in place, since `pending-undo-list' shares them."
  (unless (or (zerop delta) (eq buffer-undo-list t))
    (let ((shift (lambda (pos)
                   (cond ((< (abs pos) from) pos)
                         ((< pos 0) (- pos delta))
                         (t (+ pos delta)))))
          (cell buffer-undo-list))
      (while cell
        (let ((entry (car cell)))
          (cond
           ;; POSITION: where point was.
           ((integerp entry) (setcar cell (funcall shift entry)))
           ((not (consp entry)) nil)
           ;; (BEG . END): text inserted there.
           ((and (integerp (car entry)) (integerp (cdr entry)))
            (setcar entry (funcall shift (car entry)))
            (setcdr entry (funcall shift (cdr entry))))
           ;; (TEXT . POSITION): text deleted from there.
           ((and (stringp (car entry)) (integerp (cdr entry)))
            (setcdr entry (funcall shift (cdr entry))))
           ;; (nil PROPERTY VALUE BEG . END): a property changed there.
           ((and (null (car entry)) (consp (nthcdr 3 entry)))
            (let ((range (nthcdr 3 entry)))
              (when (and (integerp (car range)) (integerp (cdr range)))
                (setcar range (funcall shift (car range)))
                (setcdr range (funcall shift (cdr range))))))
           ;; (apply DELTA BEG END FUN . ARGS): a change confined to BEG..END.
           ((and (eq (car entry) 'apply) (integerp (nth 1 entry))
                 (integerp (nth 2 entry)) (integerp (nth 3 entry)))
            (setcar (nthcdr 2 entry) (funcall shift (nth 2 entry)))
            (setcar (nthcdr 3 entry) (funcall shift (nth 3 entry))))))
        (setq cell (cdr cell))))))

(defun crs--chat-show-end (window)
  "Scroll WINDOW so the buffer's last line is at its bottom, point staying put."
  (with-selected-window window
    (save-excursion
      (goto-char (point-max))
      (recenter -1))))

(defun crs--chat-replace-transcript (text)
  "Make TEXT the transcript, the read-only part of the buffer above the input.
Only what changed is replaced, which while a reply streams in is the
text it grew by.  The input, point, each window's point and start, and
the input's undo history are kept where they were; a window whose point
was in the input is scrolled to show the end."
  (let* ((beg (point-min))
         (old-end (marker-position crs--chat-input-start))
         (old (buffer-substring-no-properties beg old-end))
         (prefix (compare-strings old nil nil text nil nil)))
    (unless (eq prefix t)
      (let* ((same (1- (abs prefix)))
             (suffix (crs--chat-common-suffix old text same))
             (edit-beg (+ beg same))
             (edit-end (- old-end suffix))
             (new-text (substring text same (- (length text) suffix)))
             (new-seg-end (+ edit-beg (length new-text)))
             (delta (- (length text) (length old)))
             (move (lambda (pos)
                     (cond ((< pos edit-beg) pos)
                           ((>= pos edit-end) (+ pos delta))
                           (t (min pos new-seg-end)))))
             (windows (get-buffer-window-list (current-buffer) nil t))
             (following (seq-filter (lambda (w) (>= (window-point w) old-end)) windows))
             (places (mapcar (lambda (w) (list w (window-point w) (window-start w))) windows))
             (pt (point)))
        (let ((inhibit-read-only t)
              (buffer-undo-list t))
          (goto-char edit-beg)
          (delete-region edit-beg edit-end)
          (insert (propertize new-text
                              'read-only t
                              'front-sticky '(read-only)
                              'rear-nonsticky t)))
        (set-marker crs--chat-input-start (+ beg (length text)))
        (goto-char (funcall move pt))
        (dolist (place places)
          (set-window-point (nth 0 place) (funcall move (nth 1 place)))
          (set-window-start (nth 0 place) (funcall move (nth 2 place)) t))
        (crs--chat-shift-undo old-end delta)
        (mapc #'crs--chat-show-end following)))))

(defun crs--chat-prompt-string ()
  "The line drawn between the transcript and the input."
  (let* ((label (cond
                 ((crs--chat-running-p)
                  (format " %s is answering · %s stops it "
                          (crs--chat-agent-name) (crs--chat-keys 'crs-chat-stop)))
                 (crs--chat-sending " Sending… ")
                 (crs--chat (format " Reply · %s sends " (crs--chat-keys 'crs-chat-send)))
                 (t (format " Your message · %s sends " (crs--chat-keys 'crs-chat-send)))))
         (rule (concat "───" label (make-string (max 3 (- 72 (length label))) ?─))))
    (concat (propertize rule 'face 'crs-chat-prompt-face) "\n")))

(defun crs--chat-update-prompt ()
  "Draw the line between the transcript and the input where the input starts."
  (unless (overlayp crs--chat-prompt)
    (setq crs--chat-prompt (make-overlay (point-min) (point-min))))
  (move-overlay crs--chat-prompt crs--chat-input-start crs--chat-input-start)
  (overlay-put crs--chat-prompt 'before-string (crs--chat-prompt-string)))

(defun crs--chat-header-line ()
  "The header line of a chat buffer: the PR, the agent and model, cost and status."
  (let* ((chat crs--chat)
         (model (if chat (cdr (assq 'model chat)) crs--chat-model))
         (parts (delq nil
                      (list (crs--chat-session-label)
                            (format "%s/%s #%s" crs--chat-owner crs--chat-repo crs--chat-number)
                            (crs--chat-agent-name)
                            (crs--chat-model-label (crs--chat-backend-shown) model)
                            (and chat (crs--chat-format-cost (cdr (assq 'cost_usd chat))))
                            (when (crs--chat-running-p)
                              (string-trim
                               (concat "working " (crs--chat-elapsed
                                                   (cdr (assq 'started_at chat))))))
                            (when (and chat (crs--chat-stale-p chat)) "the PR has moved on")))))
    ;; `%' is a construct in a mode line; the parts are plain text.
    (replace-regexp-in-string "%" "%%" (mapconcat #'identity parts " · ") t t)))

(defun crs--chat-buffers ()
  "The live chat buffers."
  (seq-filter (lambda (buffer)
                (eq (buffer-local-value 'major-mode buffer) 'crs-chat-mode))
              (buffer-list)))

(defun crs--chat-sessions-buffers ()
  "The live session list buffers."
  (seq-filter (lambda (buffer)
                (eq (buffer-local-value 'major-mode buffer) 'crs-chat-sessions-mode))
              (buffer-list)))


;;; Sessions

(defun crs--chat-session-id (session)
  "The ID of SESSION, a chat as ListChats lists it."
  (cdr (assq 'id session)))

(defun crs--chat-sessions-by-age (&optional sessions)
  "SESSIONS (by default the buffer's), the first started first.
Sessions started in the same second keep the order they were last
active in, oldest first."
  (sort (reverse (or sessions crs--chat-sessions))
        (lambda (a b) (string< (or (cdr (assq 'created_at a)) "")
                               (or (cdr (assq 'created_at b)) "")))))

(defun crs--chat-session-position (id &optional sessions)
  "The index of session ID in SESSIONS (by default the buffer's, by age), or nil."
  (and id
       (cl-position id (or sessions (crs--chat-sessions-by-age))
                    :key #'crs--chat-session-id :test #'equal)))

(defun crs--chat-session-label ()
  "Which of the PR's sessions the buffer shows, for its header line."
  (let* ((sessions (crs--chat-sessions-by-age))
         (n (length sessions))
         (pos (crs--chat-session-position crs--chat-id sessions)))
    (cond (pos (format "Session %d/%d" (1+ pos) n))
          (crs--chat-id "Session")
          ((> n 0) (format "New session (%d earlier)" n))
          (t "New session"))))

(defun crs--chat-same-pr-p (owner repo number)
  "Non-nil when OWNER/REPO #NUMBER is the buffer's PR, whatever the names' case."
  (and (stringp owner) (stringp repo) (equal number crs--chat-number)
       (stringp crs--chat-owner) (stringp crs--chat-repo)
       (eq t (compare-strings owner nil nil crs--chat-owner nil nil t))
       (eq t (compare-strings repo nil nil crs--chat-repo nil nil t))))

(defun crs--chat-pr-params ()
  "The buffer's PR, as the chat RPCs that take one name it."
  (list (cons 'Owner crs--chat-owner)
        (cons 'Repo crs--chat-repo)
        (cons 'Number crs--chat-number)))

(defun crs--chat-list-sessions (buffer &optional then)
  "List BUFFER's PR's sessions again with ListChats, then call THEN with them.
BUFFER is a chat buffer, whose header line counts them, or a session list
buffer, which draws them.  One listing runs at a time: one asked for
meanwhile runs once it lands, and THEN waits for it."
  (when (buffer-live-p buffer)
    (with-current-buffer buffer
      (when then
        (setq crs--chat-list-callbacks (append crs--chat-list-callbacks (list then))))
      (if crs--chat-listing
          (setq crs--chat-listing 'again)
        (setq crs--chat-listing t)
        (condition-case err
            (crs--send-request
             "RPCHandler.ListChats"
             (vector (crs--chat-pr-params))
             (lambda (result) (crs--chat-on-listed buffer result)))
          (error (setq crs--chat-listing nil)
                 (signal (car err) (cdr err))))))))

(defun crs--chat-on-listed (buffer result)
  "Keep RESULT, ListChats' reply, as BUFFER's sessions and draw them.
A listing asked for meanwhile runs now, and the functions waiting for
one wait for it."
  (when (buffer-live-p buffer)
    (with-current-buffer buffer
      (let* ((again (eq crs--chat-listing 'again))
             (err (crs--chat-reply-error result))
             (callbacks (unless again crs--chat-list-callbacks)))
        (setq crs--chat-listing nil)
        (unless again
          (setq crs--chat-list-callbacks nil))
        (if err
            (message "Could not list the PR's sessions: %s" err)
          (setq crs--chat-sessions (append (cdr (assq 'chats result)) nil))
          (if (derived-mode-p 'crs-chat-sessions-mode)
              (crs--chat-sessions-print)
            (force-mode-line-update)))
        (unless err
          (dolist (callback callbacks)
            (funcall callback crs--chat-sessions)))
        (when again
          (crs--chat-list-sessions buffer))))))

(defun crs--chat-stop-timer ()
  "Stop redrawing running chats' header lines."
  (when (timerp crs--chat-timer)
    (cancel-timer crs--chat-timer))
  (setq crs--chat-timer nil))

(defun crs--chat-tick ()
  "Redraw the header lines of the running chats on screen, for the elapsed time."
  (let ((running (seq-filter #'crs--chat-running-p (crs--chat-buffers))))
    (dolist (buffer running)
      (when (get-buffer-window buffer t)
        (with-current-buffer buffer
          (force-mode-line-update))))
    (unless running
      (crs--chat-stop-timer))))

(defun crs--chat-update-timer ()
  "Run `crs--chat-tick' each second while any chat buffer shows a running turn."
  (if (seq-some #'crs--chat-running-p (crs--chat-buffers))
      (unless crs--chat-timer
        (setq crs--chat-timer (run-with-timer 1 1 #'crs--chat-tick)))
    (crs--chat-stop-timer)))

(defun crs--chat-render (buffer)
  "Draw BUFFER's chat above its input, which is left as it is."
  (when (buffer-live-p buffer)
    (with-current-buffer buffer
      (crs--chat-ensure-choice)
      (crs--chat-replace-transcript (crs--chat-transcript crs--chat))
      (crs--chat-update-prompt)
      (force-mode-line-update))
    (crs--chat-update-timer)))


;;; Talking to the server

(defun crs--chat-show (ticket chat)
  "Make CHAT, from the reply to the request that took TICKET, the one shown.
Not when the buffer shows a newer reply, or has moved on to another
chat.  Returns non-nil when CHAT is shown."
  (when (and chat
             (> ticket crs--chat-shown)
             (or (null crs--chat-id) (equal crs--chat-id (cdr (assq 'id chat)))))
    (setq crs--chat-shown ticket
          crs--chat-id (cdr (assq 'id chat))
          crs--chat chat)
    t))

(defun crs--chat-reset ()
  "Make the buffer a new chat, ignoring replies about the one it showed."
  (setq crs--chat-id nil
        crs--chat nil
        crs--chat-shown (crs--chat-ticket)))

(defun crs--chat-fetch (buffer)
  "Read BUFFER's chat again with GetChat and draw it.
One read runs at a time: one asked for meanwhile runs once it lands."
  (when (buffer-live-p buffer)
    (with-current-buffer buffer
      (cond
       ((null crs--chat-id) (crs--chat-render buffer))
       (crs--chat-fetching (setq crs--chat-fetching 'again))
       (t
        (let ((ticket (crs--chat-ticket))
              (id crs--chat-id))
          (setq crs--chat-fetching t)
          (condition-case err
              (crs--send-request
               "RPCHandler.GetChat"
               (vector (list (cons 'ChatID id)))
               (lambda (result) (crs--chat-on-fetched buffer ticket id result)))
            (error (setq crs--chat-fetching nil)
                   (signal (car err) (cdr err))))))))))

(defun crs--chat-on-fetched (buffer ticket id result)
  "Show RESULT, GetChat's reply about chat ID, in BUFFER.
TICKET is the request's; a read asked for meanwhile runs now."
  (when (buffer-live-p buffer)
    (with-current-buffer buffer
      (let ((again (eq crs--chat-fetching 'again))
            (err (crs--chat-reply-error result)))
        (setq crs--chat-fetching nil)
        (cond
         ((and err (string-match-p "there is no chat" err))
          (when (equal crs--chat-id id)
            (crs--chat-reset)
            (message "That chat is gone: it was deleted")))
         (err (message "Could not read the chat: %s" err))
         (t (crs--chat-show ticket (cdr (assq 'chat result)))))
        (crs--chat-render buffer)
        (when again
          (crs--chat-fetch buffer))))))

(defun crs--chat-load (buffer open-recent)
  "Bring BUFFER up to date: the agents, then its chat and the PR's sessions.
With OPEN-RECENT, a buffer showing no chat opens the PR's most recent
session, unless a message was sent from it meanwhile."
  (crs--chat-with-backends
   (lambda ()
     (when (buffer-live-p buffer)
       (with-current-buffer buffer
         (crs--chat-render buffer)
         (when crs--chat-id
           (crs--chat-fetch buffer))
         (let ((ticket (crs--chat-ticket)))
           (crs--chat-list-sessions
            buffer
            (lambda (chats)
              (when (and open-recent
                         chats
                         (null crs--chat-id)
                         (not crs--chat-sending)
                         (> ticket crs--chat-shown))
                (crs--chat-open-id buffer (crs--chat-session-id (car chats))))))))))))

(defun crs--chat-open-id (buffer id)
  "Show chat ID in BUFFER, or a new chat when ID is nil."
  (with-current-buffer buffer
    (cond ((null id) (crs--chat-reset))
          ((not (equal id crs--chat-id))
           (setq crs--chat-id id
                 crs--chat nil
                 crs--chat-shown (crs--chat-ticket))))
    (crs--chat-render buffer)
    (crs--chat-fetch buffer)
    (crs--chat-sessions-redraw)))

(defun crs--chat-open-reply (buffer chat)
  "Show CHAT, a chat a reply carried, in BUFFER, leaving the one it showed."
  (with-current-buffer buffer
    (setq crs--chat-id (cdr (assq 'id chat))
          crs--chat nil)
    (crs--chat-show (crs--chat-ticket) chat)
    (crs--chat-render buffer)
    (crs--chat-list-sessions buffer)
    (crs--chat-sessions-redraw)))

(defun crs--chat-call (method params)
  "Call METHOD with PARAMS and wait for the reply, which this returns.
For a command that asks the user something with what it reads, which it
shouldn't do from a process filter."
  (let ((done nil)
        (reply nil)
        (deadline (+ (float-time) 10)))
    (crs--send-request method params (lambda (result) (setq reply result done t)))
    (while (and (not done) (< (float-time) deadline))
      (accept-process-output crs--process 0.05))
    (unless done
      (error "The server didn't answer %s" method))
    reply))

(defun crs--chat-on-notification (method data)
  "Follow a Server.ChatChanged notification, METHOD with DATA.
Each buffer showing the chat it names reads it again, and each chat or
session list buffer about its PR lists the PR's sessions again: one may
have started, ended, been renamed, forked or deleted."
  (when (equal method "Server.ChatChanged")
    (let ((id (cdr (assq 'chat_id data)))
          (owner (cdr (assq 'owner data)))
          (repo (cdr (assq 'repo data)))
          (number (cdr (assq 'number data))))
      (when (stringp id)
        (dolist (buffer (crs--chat-buffers))
          (when (equal (buffer-local-value 'crs--chat-id buffer) id)
            (crs--chat-fetch buffer))))
      (dolist (buffer (append (crs--chat-buffers) (crs--chat-sessions-buffers)))
        (when (with-current-buffer buffer (crs--chat-same-pr-p owner repo number))
          (crs--chat-list-sessions buffer))))))

(defun crs--chat-on-subscribed ()
  "Read every chat buffer's chat again, for a new server.
It may offer other agents, and replies the old one owed never come."
  (setq crs-chat-backends nil
        crs-chat-agent nil)
  (dolist (buffer (crs--chat-buffers))
    (with-current-buffer buffer
      (setq crs--chat-fetching nil
            crs--chat-sending nil
            crs--chat-listing nil
            crs--chat-list-callbacks nil
            ;; Its config may name another agent.
            crs--chat-backend nil))
    (crs--chat-load buffer nil))
  (dolist (buffer (crs--chat-sessions-buffers))
    (with-current-buffer buffer
      (setq crs--chat-listing nil
            crs--chat-list-callbacks nil))
    (crs--chat-list-sessions buffer)))

(add-hook 'crs-server-notification-functions #'crs--chat-on-notification)
(add-hook 'crs-server-subscribed-hook #'crs--chat-on-subscribed)


;;; Opening

(defun crs--chat-window-lines (window)
  "The height in lines of a chat window split off WINDOW."
  (max window-min-height
       (if (integerp crs-chat-window-height)
           crs-chat-window-height
         (round (* crs-chat-window-height (window-total-height window))))))

(defun crs--chat-display (buffer)
  "Show BUFFER in a window split off the bottom of the selected one, and select it.
The new window takes `crs-chat-window-height' of the selected one's
height.  Where BUFFER is already on screen, that window is selected
instead.  `quit-window' in the new window deletes it again."
  (let ((window (get-buffer-window buffer)))
    (if window
        (select-window window)
      (let ((origin (selected-window)))
        (pop-to-buffer buffer
                       `((display-buffer-in-direction)
                         (window . ,origin)
                         (direction . below)
                         (window-height . ,(crs--chat-window-lines origin))))))))

(defun crs--chat-create (owner repo number)
  "Make the chat buffer for OWNER/REPO #NUMBER and return it."
  (let ((buffer (get-buffer-create (crs--chat-buffer-name owner repo number))))
    (with-current-buffer buffer
      (crs-chat-mode)
      (setq crs--chat-owner owner
            crs--chat-repo repo
            crs--chat-number number)
      (crs--chat-render buffer))
    buffer))

(defun crs--chat-read-pr ()
  "The PR to chat about, as (OWNER REPO NUMBER).
The current chat buffer's or session list's, else as
`crs--diagram-read-pr' finds it: the review buffer's, the PR on the
current line, or one read from the minibuffer."
  (if (derived-mode-p 'crs-chat-mode 'crs-chat-sessions-mode)
      (list crs--chat-owner crs--chat-repo crs--chat-number)
    (crs--diagram-read-pr)))

;;;###autoload (autoload 'crs-chat "crs-client" nil t)
(defun crs-chat (owner repo number)
  "Chat with a local agent about PR OWNER/REPO #NUMBER.
From a review buffer, the chat opens below the review, which keeps the
top three quarters of its window (`crs-chat-window-height').  It shows
the PR's most recent conversation, or a new one; the agent the server's
config picks (Claude Code, say, or any ACP agent) reads the PR's
description and diff with your first message, and can read the
repository but not change it.

Write below the line and send with \\<crs-chat-mode-map>\\[crs-chat-send].  The answer streams in as
the agent writes it.  \\[crs-chat-stop] stops it, \\[crs-chat-select-model] picks the model for new
sessions, and \\[crs-chat-quit] closes the window.

A PR can have several sessions, each a context of its own: \\[crs-chat-new]
starts a new one (clearing the context), \\[crs-chat-fork] forks this one into a
new one, \\[crs-chat-previous-session] and \\[crs-chat-next-session] step between them, \\[crs-chat-switch] picks one,
and \\[crs-chat-sessions] lists them to open, fork, rename or delete.

Interactively, the PR is the current review's or chat's, the one on
the current line, or else one read from the minibuffer."
  (interactive (crs--chat-read-pr))
  (unless (and crs--process (process-live-p crs--process))
    (crs-start-server))
  (let* ((existing (get-buffer (crs--chat-buffer-name owner repo number)))
         (buffer (or existing (crs--chat-create owner repo number))))
    (crs--chat-display buffer)
    (with-current-buffer buffer
      (goto-char (point-max)))
    (when (and (eq (window-buffer) buffer) (fboundp 'evil-insert-state))
      (evil-insert-state))
    (crs--chat-load buffer (not existing))
    buffer))

(defun crs--chat-pr-buffer (owner repo number)
  "The chat buffer for OWNER/REPO #NUMBER, made when there is none."
  (or (get-buffer (crs--chat-buffer-name owner repo number))
      (let ((buffer (crs--chat-create owner repo number)))
        (crs--chat-load buffer nil)
        buffer)))


;;; Commands in the chat buffer

(defun crs--chat-check-buffer ()
  "Signal an error unless the current buffer is a chat buffer."
  (unless (derived-mode-p 'crs-chat-mode)
    (user-error "Not in a chat buffer")))

(defun crs--chat-input ()
  "The message being written, below the line."
  (buffer-substring-no-properties crs--chat-input-start (point-max)))

(defun crs--chat-clear-input (sent)
  "Take SENT, the message that went out, off the start of the input.
Whatever was written after it while it was on its way stays."
  (let ((start (marker-position crs--chat-input-start)))
    (when (string-prefix-p sent (crs--chat-input))
      (undo-boundary)
      (delete-region start (+ start (length sent)))
      (save-excursion
        (goto-char start)
        (when (looking-at "[ \t\n]+")
          (delete-region start (match-end 0))))
      (undo-boundary))))

(defun crs--chat-on-sent (buffer ticket sent result)
  "Handle RESULT, the reply to sending SENT from BUFFER.
TICKET is the request's.  A message the server took leaves the input;
one it refused stays there, and the echo area says why."
  (when (buffer-live-p buffer)
    (with-current-buffer buffer
      (setq crs--chat-sending nil)
      (let ((err (crs--chat-reply-error result)))
        (cond
         (err (message "Could not send the message: %s" err))
         (t
          (crs--chat-show ticket (cdr (assq 'chat result)))
          (if (eq (cdr (assq 'okay result)) t)
              (crs--chat-clear-input sent)
            (message "%s" (let ((why (cdr (assq 'message result))))
                            (if (and (stringp why) (not (string-empty-p why)))
                                why
                              "The server refused the message")))))))
      (crs--chat-render buffer))))

(put 'crs-chat-send :advertised-binding (kbd "C-c C-c"))

(defun crs-chat-send ()
  "Send the message written below the line.
The first message of a new chat starts it, with the agent and model
the header line names; later ones continue it.  The agent answers in
the background, and its answer streams into the buffer."
  (interactive)
  (crs--chat-check-buffer)
  (let ((raw (crs--chat-input))
        (buffer (current-buffer)))
    (when (string-empty-p (string-trim raw))
      (user-error "Nothing to send: write a message below the line"))
    (when crs--chat-sending
      (user-error "The last message is still on its way"))
    (when (crs--chat-running-p)
      (user-error "%s is still answering; %s stops it"
                  (crs--chat-agent-name) (crs--chat-keys 'crs-chat-stop)))
    (unless crs--chat-id
      (let ((backend (crs--chat-backend-shown)))
        (when (and crs-chat-backends (not (crs--chat-available-p backend)))
          (user-error "%s" (if backend
                               (format "%s isn't available: %s"
                                       (cdr (assq 'name backend))
                                       (or (cdr (assq 'reason backend)) ""))
                             "The server offers no chat agents")))))
    (let ((ticket (crs--chat-ticket))
          (text (string-trim raw)))
      (setq crs--chat-sending t)
      (crs--chat-update-prompt)
      (condition-case err
          (crs--send-request
           (if crs--chat-id "RPCHandler.SendChatMessage" "RPCHandler.StartChat")
           (vector (if crs--chat-id
                       (list (cons 'ChatID crs--chat-id)
                             (cons 'Message text))
                     (list (cons 'Owner crs--chat-owner)
                           (cons 'Repo crs--chat-repo)
                           (cons 'Number crs--chat-number)
                           (cons 'Model (or crs--chat-model ""))
                           (cons 'Message text))))
           (lambda (result) (crs--chat-on-sent buffer ticket raw result)))
        (error (setq crs--chat-sending nil)
               (crs--chat-update-prompt)
               (signal (car err) (cdr err)))))))

(defun crs-chat-stop ()
  "Stop the agent answering in this chat.
The chat notes that it was stopped once the agent has exited."
  (interactive)
  (crs--chat-check-buffer)
  (unless crs--chat-id
    (user-error "This chat hasn't started"))
  (let ((buffer (current-buffer)))
    (crs--send-request
     "RPCHandler.StopChat"
     (vector (list (cons 'ChatID crs--chat-id)))
     (lambda (result)
       (let ((err (crs--chat-reply-error result)))
         (cond
          (err (message "Could not stop the agent: %s" err))
          ((eq (cdr (assq 'stopped result)) t) (message "Stopping the agent…"))
          (t (message "Nothing is running in this chat")
             (crs--chat-fetch buffer))))))))

(defun crs-chat-new ()
  "Start a new session about the PR in this buffer: a fresh context.
The session it showed stays, to come back to (`crs-chat-sessions').
The message being written stays; sending it starts the session, which
gives the agent the PR's diff as it is now."
  (interactive)
  (crs--chat-check-buffer)
  (crs--chat-reset)
  (crs--chat-render (current-buffer))
  (crs--chat-sessions-redraw)
  (message "New session: %s sends its first message" (crs--chat-keys 'crs-chat-send)))

(defun crs--chat-choices (chats)
  "Completion candidates for CHATS, as (LABEL . ID), a new session (ID nil) first."
  (let ((seen (make-hash-table :test 'equal)))
    (cons (cons "New session" nil)
          (mapcar
           (lambda (chat)
             (let* ((id (or (cdr (assq 'id chat)) ""))
                    (title (string-trim (or (cdr (assq 'title chat)) "")))
                    (time (crs--chat-parse-time (cdr (assq 'updated_at chat))))
                    (turns (cdr (assq 'turns chat)))
                    (label (format "%s  (%s%s%s)"
                                   (if (string-empty-p title)
                                       "Untitled session"
                                     (truncate-string-to-width title 60 nil nil "…"))
                                   (if time (format-time-string "%Y-%m-%d %H:%M" time) "?")
                                   (if (integerp turns)
                                       (format ", %d turn%s" turns (if (= turns 1) "" "s"))
                                     "")
                                   (if (equal (cdr (assq 'status chat)) "running")
                                       ", answering"
                                     ""))))
               (when (gethash label seen)
                 (setq label (format "%s [%s]" label (substring id 0 (min 8 (length id))))))
               (puthash label t seen)
               (cons label id)))
           chats))))

(defun crs--chat-sessions-now ()
  "The buffer's PR's sessions, listed again and waited for."
  (let* ((result (crs--chat-call "RPCHandler.ListChats" (vector (crs--chat-pr-params))))
         (err (crs--chat-reply-error result)))
    (when err
      (user-error "Could not list the PR's sessions: %s" err))
    (setq crs--chat-sessions (append (cdr (assq 'chats result)) nil))))

(defun crs-chat-switch ()
  "Open another of the PR's sessions in this buffer, or a new one."
  (interactive)
  (crs--chat-check-buffer)
  (let* ((choices (crs--chat-choices (crs--chat-sessions-now)))
         (current (car (rassoc crs--chat-id choices)))
         (choice (completing-read "Session: " choices nil t nil nil current)))
    (crs--chat-open-id (current-buffer) (cdr (assoc choice choices)))))

(defun crs--chat-step-session (delta)
  "Open the session DELTA after this buffer's, in the order they started.
From a new session, the one before is the PR's latest."
  (crs--chat-check-buffer)
  (let* ((sessions (crs--chat-sessions-by-age (or crs--chat-sessions (crs--chat-sessions-now))))
         (n (length sessions))
         (pos (crs--chat-session-position crs--chat-id sessions))
         (target (cond (pos (+ pos delta))
                       ((< delta 0) (1- n)))))
    (cond
     ((zerop n) (user-error "This PR has no other sessions"))
     ((and target (<= 0 target) (< target n))
      (let ((session (nth target sessions)))
        (crs--chat-open-id (current-buffer) (crs--chat-session-id session))
        (message "Session %d/%d: %s" (1+ target) n
                 (let ((title (cdr (assq 'title session))))
                   (if (and (stringp title) (not (string-empty-p title))) title "Untitled session")))))
     ((< delta 0) (user-error "This is the PR's first session"))
     (t (user-error "This is the PR's latest session; %s starts a new one"
                    (crs--chat-keys 'crs-chat-new))))))

(defun crs-chat-previous-session ()
  "Open the PR's session started before this one."
  (interactive)
  (crs--chat-step-session -1))

(defun crs-chat-next-session ()
  "Open the PR's session started after this one."
  (interactive)
  (crs--chat-step-session 1))

(defun crs--chat-fork (owner repo number id)
  "Fork chat ID of OWNER/REPO #NUMBER, and show the fork in the PR's chat buffer.
The buffer is shown in the selected window when it isn't on screen."
  (crs--send-request
   "RPCHandler.ForkChat"
   (vector (list (cons 'ChatID id) (cons 'Model "")))
   (lambda (result)
     (let ((err (crs--chat-reply-error result))
           (chat (cdr (assq 'chat result))))
       (cond
        (err (message "Could not fork the session: %s" err))
        ((not (and (eq (cdr (assq 'okay result)) t) chat))
         (message "%s" (let ((why (cdr (assq 'message result))))
                         (if (and (stringp why) (not (string-empty-p why)))
                             why
                           "The server refused to fork the session"))))
        (t
         (let ((buffer (crs--chat-pr-buffer owner repo number)))
           (crs--chat-open-reply buffer chat)
           (message "Forked: %s sends the first message of the new session"
                    (crs--chat-keys 'crs-chat-send)))))))))

(defun crs-chat-fork ()
  "Fork this session: a new one carrying its messages, with a fresh context.
Your next message starts it, and the agent gets the conversation so far
with it, and the PR's description and diff as they are then; so a fork
is how a session follows a PR that has moved on.  This session stays,
to come back to."
  (interactive)
  (crs--chat-check-buffer)
  (unless crs--chat-id
    (user-error "This session hasn't started, so there is nothing to fork"))
  (when (crs--chat-running-p)
    (user-error "%s is still answering; %s stops it"
                (crs--chat-agent-name) (crs--chat-keys 'crs-chat-stop)))
  (crs--chat-fork crs--chat-owner crs--chat-repo crs--chat-number crs--chat-id))

(defun crs--chat-rename (buffer id title)
  "Retitle chat ID TITLE, then list the sessions of BUFFER's PR again."
  (crs--send-request
   "RPCHandler.RenameChat"
   (vector (list (cons 'ChatID id) (cons 'Title title)))
   (lambda (result)
     (let ((err (or (crs--chat-reply-error result)
                    (and (not (eq (cdr (assq 'okay result)) t))
                         (or (cdr (assq 'message result)) "the server refused the title")))))
       (if err
           (message "Could not rename the session: %s" err)
         (message "Renamed: %s" (cdr (assq 'title (cdr (assq 'chat result)))))
         (when (buffer-live-p buffer)
           (crs--chat-list-sessions buffer))
         (dolist (chat-buffer (crs--chat-buffers))
           (when (equal (buffer-local-value 'crs--chat-id chat-buffer) id)
             (crs--chat-fetch chat-buffer))))))))

(defun crs--chat-read-title (title)
  "Read a session's new title, starting from TITLE."
  (let ((new (string-trim (read-string "Session title: " (or title "")))))
    (when (string-empty-p new)
      (user-error "A session needs a title"))
    new))

(defun crs-chat-rename ()
  "Retitle this session, as the PR's list of sessions names it."
  (interactive)
  (crs--chat-check-buffer)
  (unless crs--chat-id
    (user-error "This session hasn't started, so it has no title yet"))
  (crs--chat-rename (current-buffer) crs--chat-id
                    (crs--chat-read-title (cdr (assq 'title crs--chat)))))

(defun crs-chat-select-model ()
  "Choose the model this buffer's next new chat asks its agent for.
The agent is the server config's choice ([Chat] Agent); the model is
one of those it offers.  The choice is remembered for the chats opened
after it this session."
  (interactive)
  (crs--chat-check-buffer)
  (unless crs-chat-backends
    (let* ((result (crs--chat-call "RPCHandler.ListChatBackends" (vector)))
           (err (crs--chat-reply-error result)))
      (when err
        (user-error "Could not list the chat agents: %s" err))
      (setq crs-chat-backends (or (cdr (assq 'backends result)) [])
            crs-chat-agent (cdr (assq 'agent result)))))
  (crs--chat-ensure-choice)
  (let ((backend (crs--chat-next-backend)))
    (unless backend
      (user-error "The server offers no chat agents"))
    (unless (crs--chat-available-p backend)
      (user-error "%s isn't available: %s"
                  (cdr (assq 'name backend)) (or (cdr (assq 'reason backend)) "")))
    (let* ((id (cdr (assq 'id backend)))
           (models (mapcar (lambda (m) (cons (cdr (assq 'name m)) (cdr (assq 'id m))))
                           (crs--chat-models backend))))
      (unless (cdr models)
        (user-error "%s offers no choice of model (its config lists none)" (cdr (assq 'name backend))))
      (let ((model (cdr (assoc (completing-read
                                (format "%s model: " (cdr (assq 'name backend)))
                                models nil t nil nil
                                (crs--chat-model-label backend crs--chat-model))
                               models))))
        (setq crs--chat-model (or model "")
              crs--chat-last-choice (cons id crs--chat-model))
        (crs--chat-render (current-buffer))
        (message "%s%s · %s"
                 (if crs--chat (format "New chats (%s) will use " (crs--chat-keys 'crs-chat-new)) "Using ")
                 (cdr (assq 'name backend))
                 (crs--chat-model-label backend crs--chat-model))))))

(defun crs-chat-refresh ()
  "Read the agents and this buffer's chat from the server again."
  (interactive)
  (crs--chat-check-buffer)
  (setq crs-chat-backends nil
        crs-chat-agent nil
        crs--chat-backend nil
        crs--chat-fetching nil)
  (crs--chat-load (current-buffer) nil))

(defun crs-chat-delete ()
  "Delete the session this buffer shows, its messages and its working directory.
Stops the agent first when it is answering."
  (interactive)
  (crs--chat-check-buffer)
  (unless crs--chat-id
    (user-error "This session hasn't started, so there is nothing to delete"))
  (when (yes-or-no-p "Delete this session and its messages? ")
    (let ((buffer (current-buffer))
          (id crs--chat-id))
      (crs--send-request
       "RPCHandler.DeleteChat"
       (vector (list (cons 'ChatID id)))
       (lambda (result)
         (let ((err (crs--chat-reply-error result)))
           (if err
               (message "Could not delete the chat: %s" err)
             (when (buffer-live-p buffer)
               (with-current-buffer buffer
                 (when (equal crs--chat-id id)
                   (crs--chat-reset)))
               (crs--chat-render buffer)
               (crs--chat-list-sessions buffer))
             (message "Session deleted"))))))))

(defun crs-chat-quit ()
  "Close the chat window, keeping the buffer and the message being written."
  (interactive)
  (quit-window))


;;; The session list

(defvar-keymap crs-chat-sessions-mode-map
  :doc "Keymap for `crs-chat-sessions-mode'.  Bindings are defined in crs-client.el."
  :parent tabulated-list-mode-map)

(define-derived-mode crs-chat-sessions-mode tabulated-list-mode "CRS-Sessions"
  "Major mode listing a PR's review chat sessions, the most recently active first.
The session the PR's chat buffer shows is marked ▸, and one whose agent
is answering ●.
\\{crs-chat-sessions-mode-map}"
  (setq tabulated-list-format
        [("" 2 nil)
         ("Active" 16 nil)
         ("Turns" 5 nil :right-align t)
         ("Session" 50 nil)
         ("Last reply" 0 nil)]
        tabulated-list-padding 1)
  (add-hook 'tabulated-list-revert-hook #'crs--chat-sessions-revert nil t)
  (tabulated-list-init-header))

(defun crs--chat-sessions-buffer-name (owner repo number)
  "Name of the buffer listing the sessions about OWNER/REPO #NUMBER."
  (format "* CRS Chat Sessions: %s/%s #%d *" owner repo number))

(defun crs--chat-sessions-chat-id ()
  "ID of the session the PR's chat buffer shows, or nil."
  (let ((chat (get-buffer (crs--chat-buffer-name crs--chat-owner crs--chat-repo crs--chat-number))))
    (and chat (buffer-local-value 'crs--chat-id chat))))

(defun crs--chat-sessions-entry (session current)
  "A tabulated list entry for SESSION; CURRENT is the ID of the one shown."
  (let* ((id (crs--chat-session-id session))
         (title (string-trim (or (cdr (assq 'title session)) "")))
         (time (crs--chat-parse-time (cdr (assq 'updated_at session))))
         (turns (cdr (assq 'turns session)))
         (preview (or (cdr (assq 'preview session)) ""))
         (running (equal (cdr (assq 'status session)) "running")))
    (list id
          (vector (concat (if (equal id current) "▸" " ")
                          (if running (propertize "●" 'face 'success 'help-echo "The agent is answering") " "))
                  (if time (format-time-string "%Y-%m-%d %H:%M" time) "")
                  (if (integerp turns) (number-to-string turns) "")
                  (propertize (if (string-empty-p title) "Untitled session" title)
                              'face (if (equal id current) 'bold 'default))
                  (propertize preview 'face 'shadow)))))

(defun crs--chat-sessions-print ()
  "Draw the buffer's sessions, keeping point on the session it was on."
  (let ((current (crs--chat-sessions-chat-id)))
    (setq tabulated-list-entries
          (mapcar (lambda (session) (crs--chat-sessions-entry session current))
                  crs--chat-sessions))
    (tabulated-list-print t)))

(defun crs--chat-sessions-redraw ()
  "Redraw the list of the chat buffer's PR's sessions, for a new one shown."
  (when (and (derived-mode-p 'crs-chat-mode) crs--chat-owner crs--chat-repo crs--chat-number)
    (let ((list (get-buffer (crs--chat-sessions-buffer-name
                             crs--chat-owner crs--chat-repo crs--chat-number))))
      (when list
        (with-current-buffer list
          (crs--chat-sessions-print))))))

(defun crs--chat-sessions-revert ()
  "List the buffer's sessions again (\\[revert-buffer])."
  (crs--chat-list-sessions (current-buffer)))

(defun crs--chat-sessions-check ()
  "Signal an error unless the current buffer is a session list."
  (unless (derived-mode-p 'crs-chat-sessions-mode)
    (user-error "Not in a session list")))

(defun crs--chat-sessions-at-point ()
  "The session on the current line, or an error."
  (crs--chat-sessions-check)
  (let ((id (tabulated-list-get-id)))
    (or (seq-find (lambda (s) (equal (crs--chat-session-id s) id)) crs--chat-sessions)
        (user-error "No session on this line"))))

(defun crs--chat-sessions-show-chat (then)
  "Show the PR's chat buffer in this window in place of the list.
THEN is called with the chat buffer first, to choose what it shows.
The window forgets it showed the list, so quitting the chat
\(`crs-chat-quit') closes the window, as it did before the list, rather
than bringing the list back."
  (let ((buffer (crs--chat-pr-buffer crs--chat-owner crs--chat-repo crs--chat-number))
        (list (current-buffer))
        (window (selected-window)))
    (funcall then buffer)
    (switch-to-buffer buffer nil t)
    (set-window-prev-buffers window (seq-remove (lambda (entry) (eq (car entry) list))
                                                (window-prev-buffers window)))
    (goto-char (point-max))
    buffer))

;;;###autoload (autoload 'crs-chat-sessions "crs-client" nil t)
(defun crs-chat-sessions (owner repo number)
  "List the review chat sessions about PR OWNER/REPO #NUMBER.
The list takes the chat's window: \\<crs-chat-sessions-mode-map>\\[crs-chat-sessions-open] opens the session on the
line there, \\[crs-chat-sessions-new] starts a new one, \\[crs-chat-sessions-fork] forks the one on the line,
\\[crs-chat-sessions-rename] renames it and \\[crs-chat-sessions-delete] deletes it; \\[quit-window] goes back to the chat.

Interactively, the PR is the current chat's or review's, the one on the
current line, or else one read from the minibuffer."
  (interactive (crs--chat-read-pr))
  ;; From the PR's chat, or its list already, the list takes this window;
  ;; from anywhere else, the chat opens first and the list takes its window.
  (unless (and (derived-mode-p 'crs-chat-mode 'crs-chat-sessions-mode)
               (crs--chat-same-pr-p owner repo number))
    (crs-chat owner repo number))
  (let ((buffer (get-buffer-create (crs--chat-sessions-buffer-name owner repo number))))
    (with-current-buffer buffer
      (unless (derived-mode-p 'crs-chat-sessions-mode)
        (crs-chat-sessions-mode))
      (setq crs--chat-owner owner
            crs--chat-repo repo
            crs--chat-number number)
      (crs--chat-sessions-print))
    (switch-to-buffer buffer nil t)
    (when (fboundp 'evil-normal-state)
      (evil-normal-state))
    (crs--chat-list-sessions buffer)
    buffer))

(defun crs-chat-sessions-open ()
  "Open the session on this line in the PR's chat buffer."
  (interactive)
  (let ((id (crs--chat-session-id (crs--chat-sessions-at-point))))
    (crs--chat-sessions-show-chat (lambda (buffer) (crs--chat-open-id buffer id)))))

(defun crs-chat-sessions-new ()
  "Start a new session about the PR, in its chat buffer: a fresh context."
  (interactive)
  (crs--chat-sessions-check)
  (crs--chat-sessions-show-chat
   (lambda (buffer) (with-current-buffer buffer (crs--chat-reset) (crs--chat-render buffer))))
  (crs--chat-sessions-redraw)
  (message "New session: %s sends its first message" (crs--chat-keys 'crs-chat-send)))

(defun crs-chat-sessions-fork ()
  "Fork the session on this line, and open the fork in the PR's chat buffer.
See `crs-chat-fork'."
  (interactive)
  (let ((session (crs--chat-sessions-at-point)))
    (when (equal (cdr (assq 'status session)) "running")
      (user-error "The agent is still answering in that session; wait for it, or stop it"))
    (let ((owner crs--chat-owner) (repo crs--chat-repo) (number crs--chat-number))
      (crs--chat-sessions-show-chat #'ignore)
      (crs--chat-fork owner repo number (crs--chat-session-id session)))))

(defun crs-chat-sessions-rename ()
  "Retitle the session on this line."
  (interactive)
  (let ((session (crs--chat-sessions-at-point)))
    (crs--chat-rename (current-buffer) (crs--chat-session-id session)
                      (crs--chat-read-title (cdr (assq 'title session))))))

(defun crs-chat-sessions-delete ()
  "Delete the session on this line, its messages and its working directory."
  (interactive)
  (let* ((session (crs--chat-sessions-at-point))
         (id (crs--chat-session-id session))
         (list (current-buffer)))
    (when (yes-or-no-p (format "Delete the session \"%s\" and its messages? "
                               (or (cdr (assq 'title session)) "")))
      (crs--send-request
       "RPCHandler.DeleteChat"
       (vector (list (cons 'ChatID id)))
       (lambda (result)
         (let ((err (crs--chat-reply-error result)))
           (if err
               (message "Could not delete the session: %s" err)
             (dolist (buffer (crs--chat-buffers))
               (with-current-buffer buffer
                 (when (equal crs--chat-id id)
                   (crs--chat-reset)
                   (crs--chat-render buffer))))
             (crs--chat-list-sessions list)
             (message "Session deleted"))))))))

(provide 'crs-chat)
;;; crs-chat.el ends here
