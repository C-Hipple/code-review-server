;;; crs-rpc.el --- JSON-RPC process management and server lifecycle for crs. -*- lexical-binding: t; -*-

;;; Commentary:

;; JSON-RPC process management and server lifecycle for crs.

;;; Code:

(require 'json)
(require 'parse-time)
(require 'crs-vars)

;;;###autoload
(defun crs-start-server ()
  "Start the crs JSON-RPC server process.
With `crs-remote-address' set, connect to that shared server instead
\(see `crs--remote-connect').
Returns the process handle."
  (interactive)
  (if (and crs--process
           (process-live-p crs--process))
      (progn
        (message "Server is already running")
        crs--process)
    (if crs-remote-address
        (crs--remote-connect)
      (let ((stderr-buffer (get-buffer-create "*crs-client-stderr*")))
        (setq crs--process
              (make-process :name "crs-client"
                            :buffer "*crs-client*"
                            :command '("crs" "-server")
                            :connection-type 'pipe
                            :coding 'utf-8
                            :stderr stderr-buffer
                            :noquery t))

        (set-process-filter crs--process 'crs--process-filter)
        (set-process-sentinel crs--process 'crs--process-sentinel)

        ;; Give the process a moment to start up
        (sleep-for 0.2)

        ;; Check if process is still alive after startup
        (unless (process-live-p crs--process)
          (let ((stderr-content (with-current-buffer stderr-buffer (buffer-string))))
            (error "Server process died immediately. Stderr: %s" stderr-content))))
      (message "Started crs JSON-RPC server"))
    ;; A shared server's connect flow can fail before this, hanging up.
    (when crs--process
      (crs-list-plugins))
    crs--process))

;;;###autoload
(defun crs-shutdown-server ()
  "Stop the crs JSON-RPC server process if it is running."
  (interactive)
  (if (and crs--process
           (process-live-p crs--process))
      (progn
        (delete-process crs--process)
        (setq crs--process nil)
        (message "Stopped crs JSON-RPC server"))
    (message "Server is not running")))

;;;###autoload
(defun crs-restart-server ()
  "Restart the crs JSON-RPC server process.
Stops the existing server if running, then starts a new one.
Useful after recompiling the Go server binary."
  (interactive)
  (crs-shutdown-server)
  (sleep-for 0.2)  ; Give the process a moment to fully shut down
  (crs-start-server))

(defun crs--process-filter (_process output)
  "Filter function for processing JSON-RPC responses from the server.
OUTPUT arrives in chunks of `read-process-output-max' bytes, so a reply is
spread over many calls.  Chunks are pushed onto `crs--response-pending'
untouched and joined only once a newline actually arrives; scanning is
confined to the joined string.  Concatenating and rescanning the whole
accumulator on every chunk instead makes this quadratic in the reply size."
  (if (not (string-search "\n" output))
      (push output crs--response-pending)
    (let ((joined (apply #'concat (nreverse (cons output crs--response-pending))))
          (start 0)
          (lines nil)
          pos)
      (setq crs--response-pending nil)
      ;; JSON-RPC is newline-delimited: split out every complete line and
      ;; carry any trailing partial line over to the next chunk.
      (while (setq pos (string-search "\n" joined start))
        (let ((line (substring joined start pos)))
          (when (> (length line) 0)
            (push line lines)))
        (setq start (1+ pos)))
      (when (< start (length joined))
        (push (substring joined start) crs--response-pending))
      ;; Dispatch only after the pending state is settled, so a callback that
      ;; re-enters the filter cannot lose a buffered chunk.
      (dolist (line (nreverse lines))
        (crs--handle-response line)))))

(defun crs--parse-json (string)
  "Parse STRING as JSON, preferring the native parser.
The keyword arguments reproduce `json-read-from-string' semantics exactly
\(alists with symbol keys, arrays as vectors, null as nil, false as
`:json-false') so callers see no behavior change, but parsing is roughly
7x faster on the large replies this client receives."
  (if (fboundp 'json-parse-string)
      (json-parse-string string
                         :object-type 'alist
                         :array-type 'array
                         :null-object nil
                         :false-object :json-false)
    (json-read-from-string string)))

(defun crs--handle-response (response-line)
  "Handle a single JSON-RPC response line."
  (when crs-debug
    (message "DEBUG crs response: %s"
             (truncate-string-to-width response-line crs-debug-max-length nil nil t)))
  (condition-case err
      (let ((response (crs--parse-json response-line)))
        (let ((id (cdr (assq 'id response)))
              (result (cdr (assq 'result response)))
              (error (cdr (assq 'error response))))
          (if error
              (let ((callback (gethash id crs--pending-requests)))
                (message "JSON-RPC Error: %s" (if (stringp error) error (cdr (assq 'message error))))
                (when callback
                  (remhash id crs--pending-requests)
                  (funcall callback `((error . ,error)))))
            (let ((callback (gethash id crs--pending-requests)))
              (when callback
                (remhash id crs--pending-requests)
                (funcall callback result))))))
    (error
     (message "Error parsing JSON-RPC response: %s" err))))

(defun crs--process-sentinel (process event)
  "Sentinel function for the crs process."
  ;; A connection to a shared server (`crs-remote-address') went away.
  (when (memq (process-status process) '(closed failed))
    (crs--remote-connection-lost process event))
  (when (memq (process-status process) '(exit signal))
    (let ((buffer (process-buffer process))
          (stderr-buffer (get-buffer "*crs-client-stderr*")))
      (when buffer
        (with-current-buffer buffer
          (let ((output (buffer-string)))
            (when (> (length output) 0)
              (message "crs stdout: %s" output)))))
      (when stderr-buffer
        (with-current-buffer stderr-buffer
          (let ((stderr-output (buffer-string)))
            (when (> (length stderr-output) 0)
              (message "crs stderr: %s" stderr-output))))))
    (setq crs--process nil)
    (message "crs process %s" event)))

(defun crs--send-request (method params callback)
  "Send a JSON-RPC request to the server.
METHOD is the method name (e.g., 'RPCHandler.GetAllReviews').
PARAMS is the parameters array.
CALLBACK is a function to call with the result.

With `crs-remote-address' set, a dropped connection is reopened first, a
request made before the connection is authenticated waits for that, and
one the server refuses as not authenticated runs the connect flow again
and is sent once more."
  (when (and crs-remote-address
             (not (and crs--process (process-live-p crs--process))))
    (crs-start-server)
    (unless crs--process
      (error "Not connected to %s: %s" (crs--remote-where)
             (or crs--remote-last-error "the connect flow failed"))))
  (unless (and crs--process
               (process-live-p crs--process))
    (error "Server is not running. Call crs-start-server first"))

  (cond
   ((not crs-remote-address)
    (crs--send-request-now method params callback))
   (crs--remote-ready
    (crs--send-request-now method params
                           (crs--remote-retrying method params callback)))
   (t
    (push (list method params (crs--remote-retrying method params callback))
          crs--remote-queue))))

(defun crs--send-request-now (method params callback)
  "Write a request for METHOD with PARAMS to the server.
CALLBACK is called with the result, or with ((error . MESSAGE))."
  (let ((id crs--request-id))
    (setq crs--request-id (1+ crs--request-id))

    (puthash id callback crs--pending-requests)

    (let ((request (json-encode `((jsonrpc . "2.0")
                                  (method . ,method)
                                  (params . ,params)
                                  (id . ,id)))))
      (process-send-string crs--process
                           (concat request "\n")))))

;;; Remote mode
;;
;; With `crs-remote-address' set, the client connects to a shared server
;; started with `crs --listen' rather than running its own.  The wire is the
;; same newline-delimited JSON-RPC, through the same filter; what differs is
;; that a connection starts unauthenticated, so each one opens with the
;; connect flow (docs/protocol.md):
;;
;;   1. RPCHandler.Authenticate {Token} with the stored token, if any.
;;   2. When the server doesn't know the token (never registered there, or
;;      dropped after a stretch of inactivity) or there is none,
;;      RPCHandler.RegisterUser with the registration key and the workflows
;;      `crs-remote-user-config-command' prints, passing the stored token so
;;      a dropped user gets the same one back.  The reply's token is saved.
;;
;; Requests made meanwhile wait in `crs--remote-queue'.  A dropped connection
;; fails the requests in flight; the next request reconnects.

(defun crs--non-empty (string)
  "STRING trimmed, or nil when it is nil or blank."
  (when (stringp string)
    (let ((trimmed (string-trim string)))
      (unless (string-empty-p trimmed) trimmed))))

(defun crs--remote-parse-address (address)
  "Split ADDRESS, \"host:port\" or \"[v6addr]:port\", into (HOST . PORT)."
  (let ((port (and (stringp address)
                   (string-match "\\`\\(?:\\[\\([^]]+\\)\\]\\|\\([^]:[:space:][]+\\)\\):\\([0-9]+\\)\\'"
                                 address)
                   (string-to-number (match-string 3 address)))))
    (unless (and port (<= 1 port 65535))
      (user-error "`crs-remote-address' %S is not host:port" address))
    (cons (or (match-string 1 address) (match-string 2 address)) port)))

(defun crs--remote-where ()
  "The shared server, for messages."
  (format "crs at %s" crs-remote-address))

(defun crs--remote-connect ()
  "Connect to `crs-remote-address' and start the connect flow.
Returns the connection's process."
  (let* ((address (crs--remote-parse-address crs-remote-address))
         (process
          (condition-case err
              (open-network-stream "crs-client" "*crs-client*"
                                   (car address) (cdr address)
                                   :type (if crs-remote-tls 'tls 'plain)
                                   :coding 'utf-8)
            (error
             (error "Can't reach %s: %s" (crs--remote-where)
                    (error-message-string err))))))
    (set-process-query-on-exit-flag process nil)
    (set-process-filter process #'crs--process-filter)
    (set-process-sentinel process #'crs--process-sentinel)
    (setq crs--process process
          crs--remote-ready nil
          ;; A partial line from a connection that dropped is not this one's.
          crs--response-pending nil)
    (message "Connecting to %s..." (crs--remote-where))
    (crs--remote-authenticate)
    process))

(defun crs--remote-token ()
  "The token to authenticate with, and where it came from, as (TOKEN . SOURCE).
TOKEN is \"\" when there is none yet."
  (cond
   (crs--remote-issued-token
    (cons crs--remote-issued-token crs-remote-token-file))
   ((crs--non-empty (getenv "CRS_REMOTE_TOKEN"))
    (cons (crs--non-empty (getenv "CRS_REMOTE_TOKEN")) "CRS_REMOTE_TOKEN"))
   (t
    (cons (or (and (file-readable-p crs-remote-token-file)
                   (crs--non-empty
                    (with-temp-buffer
                      (insert-file-contents crs-remote-token-file)
                      (buffer-string))))
              "")
          crs-remote-token-file))))

(defun crs--remote-save-token (token)
  "Save TOKEN to `crs-remote-token-file', readable by its owner only."
  (let ((file (expand-file-name crs-remote-token-file)))
    (with-file-modes #o700
      (make-directory (file-name-directory file) t))
    (with-file-modes #o600
      (write-region (concat token "\n") nil file nil 'silent))
    ;; The modes above only apply to a file the write creates.
    (set-file-modes file #o600)))

(defun crs--remote-config-file ()
  "The local codereviewserver.toml."
  (expand-file-name "codereviewserver.toml"
                    (or (crs--non-empty (getenv "XDG_CONFIG_HOME")) "~/.config")))

(defun crs--remote-toml-login ()
  "The root GithubUsername of the local codereviewserver.toml, or nil.
Root keys are the ones before the first [table] header."
  (let ((file (crs--remote-config-file)))
    (when (file-readable-p file)
      (with-temp-buffer
        (insert-file-contents file)
        (goto-char (point-min))
        (let ((end (save-excursion
                     (if (re-search-forward "^[ \t]*\\[" nil t)
                         (match-beginning 0)
                       (point-max)))))
          (when (re-search-forward
                 "^[ \t]*GithubUsername[ \t]*=[ \t]*[\"']\\([^\"'\n]*\\)[\"']" end t)
            (crs--non-empty (match-string 1))))))))

(defun crs--remote-login ()
  "The GitHub login to register: `crs-remote-login', CRS_LOGIN, or the TOML's."
  (or (crs--non-empty crs-remote-login)
      (crs--non-empty (getenv "CRS_LOGIN"))
      (crs--remote-toml-login)))

(defun crs--remote-user-config ()
  "What registering sends from the local config file, parsed.
Runs `crs-remote-user-config-command', signalling an error that says
what went wrong when it fails."
  (let ((command crs-remote-user-config-command)
        (stderr (make-temp-file "crs-user-config")))
    (unwind-protect
        (with-temp-buffer
          (let ((status (condition-case err
                            (apply #'call-process (car command) nil
                                   (list t stderr) nil (cdr command))
                          (file-error
                           (error "Can't run %s: %s.  Install crs here, or set `crs-remote-user-config-command' to a command printing the same JSON"
                                  (car command) (error-message-string err))))))
            (unless (eql status 0)
              (error "%s failed (%s): %s" (string-join command " ") status
                     (string-trim (with-temp-buffer
                                    (insert-file-contents stderr)
                                    (buffer-string)))))
            (condition-case err
                (crs--parse-json (buffer-string))
              (error
               (error "%s printed no JSON: %s" (string-join command " ")
                      (error-message-string err))))))
      (delete-file stderr))))

(defun crs--remote-reply-error (reply)
  "The error message REPLY carries, if it is an error reply."
  (let ((err (and (consp reply) (cdr (assq 'error reply)))))
    (cond ((stringp err) err)
          (err (format "%S" err)))))

(defun crs--remote-authenticate ()
  "Run the connect flow on the current connection, then send what waited."
  (setq crs--remote-ready nil)
  (pcase-let ((`(,token . ,source) (crs--remote-token)))
    (if (string-empty-p token)
        (crs--remote-register token source)
      (crs--send-request-now
       "RPCHandler.Authenticate"
       (vector (list (cons 'Token token)))
       (lambda (reply)
         (let ((err (crs--remote-reply-error reply)))
           (cond
            (err
             (crs--remote-fail (format "%s answered Authenticate with an error: %s"
                                       (crs--remote-where) err)))
            ((eq (cdr (assq 'okay reply)) t)
             (crs--remote-ready (cdr (assq 'login reply)) reply))
            ((eq (cdr (assq 'known reply)) t)
             (crs--remote-fail (format "%s refused the token from %s: %s"
                                       (crs--remote-where) source
                                       (or (crs--non-empty (cdr (assq 'message reply)))
                                           "no reason given"))))
            (t
             (message "%s doesn't know the token from %s; registering again"
                      (crs--remote-where) source)
             (crs--remote-register token source)))))))))

(defun crs--remote-register (token source)
  "Register with the shared server, passing TOKEN (from SOURCE) if any."
  (let ((key (or (crs--non-empty crs-remote-registration-key)
                 (crs--non-empty (getenv "CRS_REGISTRATION_KEY"))))
        (login (crs--remote-login)))
    (cond
     ((null key)
      (crs--remote-fail
       (if (string-empty-p token)
           (format "Not registered with %s: set `crs-remote-registration-key' (or CRS_REGISTRATION_KEY) to the server's registration key, or put an existing token in %s"
                   (crs--remote-where) crs-remote-token-file)
         (format "%s doesn't know the token from %s, and no registration key is set to register again with it: set `crs-remote-registration-key' (or CRS_REGISTRATION_KEY)"
                 (crs--remote-where) source))))
     ((null login)
      (crs--remote-fail
       (format "Can't register with %s: no GitHub login.  Set `crs-remote-login', CRS_LOGIN, or GithubUsername in %s"
               (crs--remote-where) (crs--remote-config-file))))
     (t
      ;; The parsed config is an alist; a string is why it couldn't be read.
      (let ((config (condition-case err
                        (crs--remote-user-config)
                      (error (format "Can't register with %s: %s" (crs--remote-where)
                                     (error-message-string err))))))
        (if (stringp config)
            (crs--remote-fail config)
          (crs--send-request-now
           "RPCHandler.RegisterUser"
           (vector
            (list (cons 'RegistrationKey key)
                  (cons 'Login login)
                  (cons 'Token token)
                  (cons 'Workflows (or (cdr (assq 'Workflows config)) []))
                  (cons 'Repos (or (cdr (assq 'Repos config)) []))
                  ;; An empty JSON object parses to nil, which would go
                  ;; back out as null.
                  (cons 'SectionPriority (or (cdr (assq 'SectionPriority config))
                                             (make-hash-table)))
                  (cons 'SectionSorting (or (cdr (assq 'SectionSorting config))
                                            (make-hash-table)))))
           (lambda (reply)
             (crs--remote-registered login reply)))))))))

(defun crs--remote-problem (problem)
  "One entry of a RegisterUser reply's `errors', worded like the server's."
  (let ((workflow (cdr (assq 'workflow problem)))
        (field (cdr (assq 'field problem)))
        (message (cdr (assq 'message problem))))
    (cond ((and (integerp workflow) (>= workflow 0))
           (format "Workflows[%d].%s: %s" workflow field message))
          ((crs--non-empty field) (format "%s: %s" field message))
          (t (format "%s" message)))))

(defun crs--remote-registered (login reply)
  "Handle the RegisterUser REPLY for LOGIN."
  (let ((err (crs--remote-reply-error reply))
        (issued (crs--non-empty (cdr (assq 'token reply)))))
    (cond
     (err
      (crs--remote-fail (format "%s answered RegisterUser with an error: %s"
                                (crs--remote-where) err)))
     ((not (and (eq (cdr (assq 'okay reply)) t) issued))
      (let* ((reason (or (crs--non-empty (cdr (assq 'message reply))) "no reason given"))
             (problems (mapcar #'crs--remote-problem (cdr (assq 'errors reply)))))
        (crs--remote-fail
         (concat (format "%s refused to register %s: %s" (crs--remote-where) login reason)
                 (when problems (format " (%s)" (string-join problems "; ")))
                 (when (string-match-p "already registered" reason)
                   (format ".  The server holds a different token for %s: copy it from the machine that registered into %s (or CRS_REMOTE_TOKEN), or ask the server's administrator to delete the user"
                           login crs-remote-token-file))))))
     (t
      (condition-case err
          (crs--remote-save-token issued)
        (error
         (display-warning 'crs (format "Could not save the token to %s: %s"
                                       crs-remote-token-file
                                       (error-message-string err)))))
      (let ((env-token (crs--non-empty (getenv "CRS_REMOTE_TOKEN"))))
        (when (and env-token (not (equal issued env-token)))
          (setq crs--remote-issued-token issued)
          (display-warning
           'crs (format "%s issued a token other than CRS_REMOTE_TOKEN; it was saved to %s, but CRS_REMOTE_TOKEN wins in the next Emacs session: update or unset it"
                        (crs--remote-where) crs-remote-token-file))))
      (message "%s with %s as %s; token saved to %s"
               (if (eq (cdr (assq 'created reply)) t) "Registered" "Re-registered")
               (crs--remote-where) (or (cdr (assq 'login reply)) login)
               crs-remote-token-file)
      (crs--remote-ready (or (crs--non-empty (cdr (assq 'login reply))) login) reply)))))

(defun crs--remote-ready (login &optional reply)
  "Mark the connection authenticated as LOGIN and send what waited for it.
REPLY is the Authenticate or RegisterUser reply that signed it in, whose
GitHub link flags are kept in `crs--remote-session'."
  (setq crs--remote-ready t
        crs--remote-last-error nil
        crs--remote-session
        (list (cons 'login (or login ""))
              (cons 'is_admin (eq (cdr (assq 'is_admin reply)) t))
              (cons 'github_linked (eq (cdr (assq 'github_linked reply)) t))
              (cons 'github_link_available
                    (eq (cdr (assq 'github_link_available reply)) t))))
  (message "Connected to %s%s%s" (crs--remote-where)
           (if (crs--non-empty login) (format " as %s" login) "")
           ;; Reviews are refused until then, so say so up front.
           (if (and (cdr (assq 'github_link_available crs--remote-session))
                    (not (cdr (assq 'github_linked crs--remote-session))))
               "; link your GitHub account with M-x crs-link-github to submit reviews as yourself"
             ""))
  (let ((queued (reverse crs--remote-queue)))
    (setq crs--remote-queue nil)
    (dolist (request queued)
      (apply #'crs--send-request-now request))))

(defun crs--remote-drop (reason)
  "Forget the connection, failing every request it still owed with REASON."
  (let ((process crs--process)
        (callbacks (mapcar (lambda (request) (nth 2 request))
                           (reverse crs--remote-queue))))
    (maphash (lambda (_id callback) (push callback callbacks))
             crs--pending-requests)
    (clrhash crs--pending-requests)
    (setq crs--process nil
          crs--remote-ready nil
          crs--remote-queue nil
          crs--response-pending nil)
    (when (and process (process-live-p process))
      (delete-process process))
    (dolist (callback callbacks)
      (funcall callback `((error . ,reason))))))

(defun crs--remote-fail (reason)
  "Give up on the connect flow for REASON.
Says why, fails the requests that waited for it, and hangs up, so the
next request reconnects and tries again."
  (setq crs--remote-last-error reason)
  (display-warning 'crs reason :error)
  (crs--remote-drop reason))

(defun crs--not-authenticated-p (reply)
  "Non-nil when REPLY is the server refusing a request without a session."
  (let ((err (crs--remote-reply-error reply)))
    (and err (string-prefix-p "not authenticated:" err))))

(defun crs--remote-retrying (method params callback)
  "CALLBACK, except that a reply refusing METHOD as not authenticated
runs the connect flow again and sends METHOD with PARAMS once more.  A
second such refusal goes to CALLBACK like any other error."
  (lambda (reply)
    (if (not (crs--not-authenticated-p reply))
        (funcall callback reply)
      (push (list method params callback) crs--remote-queue)
      ;; Requests refused together wait for one run of the flow.
      (when crs--remote-ready
        (message "%s says this connection is not authenticated; connecting again"
                 (crs--remote-where))
        (crs--remote-authenticate)))))

(defun crs--remote-connection-lost (process event)
  "Fail what PROCESS, a connection to a shared server, still owed.
EVENT says how it closed.  The next request reconnects."
  ;; A connection this client hung up on itself is already forgotten.
  (when (eq process crs--process)
    (let ((reason (format "The connection to %s was lost (%s)"
                          (crs--remote-where) (string-trim event))))
      (message "%s" reason)
      (crs--remote-drop reason))))

;;; Linking a GitHub account
;;
;; On a shared server, a review goes to GitHub as the reviewer's own
;; account once they link it, and is refused with "refused: link-github:"
;; until then (docs/protocol.md).  Linking is GitHub's device flow:
;; `crs-link-github' asks the server for a code, shows it, copies it to the
;; kill ring and opens the page to enter it on, then polls GetGitHubLink at
;; the interval the server gives until the link ends.  The server does the
;; rest; nothing here holds a GitHub token.

(defconst crs--link-buffer-name "*crs GitHub link*"
  "Buffer `crs-link-github' shows the code in.")

(defconst crs--link-github-refusal "refused: link-github:"
  "Prefix of a refusal that linking a GitHub account answers.")

(defun crs--link-github-refusal-p (err)
  "Non-nil when ERR, an error message, is answered by linking a GitHub account."
  (and (stringp err) (string-prefix-p crs--link-github-refusal err)))

(defun crs--link-require-remote (what)
  "Signal a `user-error' saying WHAT is for a shared server, unless this is one."
  (unless crs-remote-address
    (user-error "%s is for a shared server (`crs-remote-address'); run locally, crs writes to GitHub with your own token"
                what)))

(defun crs--link-stop-polling ()
  "Stop waiting on a GitHub link."
  (when (timerp crs--link-poll-timer)
    (cancel-timer crs--link-poll-timer))
  (setq crs--link-poll-timer nil
        crs--link-pending nil))

(defun crs--link-time (rfc3339)
  "RFC3339 as a local time of day, \"17:05\", or nil when unreadable."
  (when (crs--non-empty rfc3339)
    (condition-case nil
        (format-time-string "%H:%M" (parse-iso8601-time-string rfc3339))
      (error nil))))

(defun crs--link-render (status)
  "Show the pending link's code in its buffer, with STATUS as the last line."
  (let ((code (plist-get crs--link-pending :code))
        (uri (plist-get crs--link-pending :uri))
        (expires (crs--link-time (plist-get crs--link-pending :expires)))
        (login (crs--non-empty (cdr (assq 'login crs--remote-session)))))
    (with-current-buffer (get-buffer-create crs--link-buffer-name)
      (let ((inhibit-read-only t))
        (erase-buffer)
        (insert (format "Link your GitHub account to %s\n\n" (crs--remote-where)))
        (when code
          (insert (format "  1. Open %s%s\n" uri
                          (if crs-link-github-browse "  (opened in your browser)" "")))
          (insert "  2. Enter the code   ")
          (insert (propertize code 'face '(:weight bold :height 1.5)))
          (insert "   (copied to the kill ring)\n")
          (insert (format "  3. Authorize the app, signed in to GitHub%s.\n"
                          (if login (format " as %s" login) "")))
          (when expires
            (insert (format "\nThe code expires at %s.\n" expires))))
        (insert "\n" status "\n"))
      (special-mode))))

(defun crs--link-outcome (reply)
  "How the GetGitHubLink or StartGitHubLink REPLY of an ended link reads."
  (let ((status (cdr (assq 'status reply)))
        (text (or (crs--non-empty (cdr (assq 'message reply))) "no reason given")))
    (pcase status
      ("linked"
       (format "Linked to GitHub as %s: your reviews now go out as you"
               (cdr (assq 'github_login reply))))
      ("denied"
       "The authorization was declined on GitHub, so nothing was linked; M-x crs-link-github to try again")
      ("expired"
       "The code expired before it was entered on GitHub; M-x crs-link-github for a new one")
      ("unavailable" (format "This server can't link GitHub accounts: %s" text))
      ("unlinked" "The link was cancelled; M-x crs-link-github to start again")
      (_ (format "%s; M-x crs-link-github to try again" text)))))

(defun crs--link-finish (reply)
  "Report the ended link REPLY describes and stop waiting on it."
  (let ((outcome (crs--link-outcome reply)))
    (when (get-buffer crs--link-buffer-name)
      (crs--link-render outcome))
    (crs--link-stop-polling)
    (when (equal (cdr (assq 'status reply)) "linked")
      (setf (alist-get 'github_linked crs--remote-session) t))
    (message "%s" outcome)))

;;;###autoload
(defun crs-link-github ()
  "Link your GitHub account on the shared server, so reviews go out as you.
A shared server submits reviews as the reviewer's own GitHub account, and
refuses them until it is linked.  This asks the server for a code, shows
it in a buffer, copies it to the kill ring and opens the page to enter it
on (see `crs-link-github-browse'); enter it there, signed in to GitHub as
the login you registered with, and authorize the app.  The outcome is
reported in the echo area once GitHub has answered.

Running it again while a code is waiting shows the same code."
  (interactive)
  (crs--link-require-remote "Linking a GitHub account")
  (crs--link-stop-polling)
  (message "Asking %s for a GitHub code..." (crs--remote-where))
  (crs--send-request "RPCHandler.StartGitHubLink" (vector) #'crs--link-started))

(defun crs--link-started (reply)
  "Show the code in the StartGitHubLink REPLY and start polling for the link."
  (let ((err (crs--remote-reply-error reply)))
    (cond
     (err (message "Could not start linking your GitHub account: %s" err))
     ((not (equal (cdr (assq 'status reply)) "pending"))
      (crs--link-finish reply))
     (t
      (let ((code (cdr (assq 'user_code reply)))
            (uri (cdr (assq 'verification_uri reply))))
        (setq crs--link-pending
              (list :code code :uri uri
                    :expires (cdr (assq 'expires_at reply))
                    :interval (cdr (assq 'interval reply))))
        (kill-new code)
        (crs--link-render "Waiting for GitHub...")
        (display-buffer crs--link-buffer-name)
        (when crs-link-github-browse
          (condition-case err
              (browse-url uri)
            (error (message "Could not open %s: %s" uri (error-message-string err)))))
        (message "Enter %s at %s (copied to the kill ring); waiting for GitHub..." code uri)
        (crs--link-schedule-poll))))))

(defun crs--link-schedule-poll ()
  "Poll for the pending link after the interval the server gave."
  (let ((interval (plist-get crs--link-pending :interval)))
    (setq crs--link-poll-timer
          (run-with-timer (if (and (numberp interval) (> interval 0)) (max 1 interval) 5)
                          nil #'crs--link-poll))))

(defun crs--link-expired-p ()
  "Non-nil once the pending link's code has expired."
  (let ((expires (plist-get crs--link-pending :expires)))
    (and (crs--non-empty expires)
         (ignore-errors
           (time-less-p (parse-iso8601-time-string expires) (current-time))))))

(defun crs--link-poll ()
  "Ask the server where the pending link stands."
  (setq crs--link-poll-timer nil)
  (when crs--link-pending
    (condition-case err
        (crs--send-request "RPCHandler.GetGitHubLink" (vector) #'crs--link-polled)
      (error (crs--link-poll-failed (error-message-string err))))))

(defun crs--link-polled (reply)
  "Handle a GetGitHubLink REPLY while a link is pending."
  (let ((err (crs--remote-reply-error reply)))
    (cond
     ((null crs--link-pending))         ; Stopped meanwhile.
     (err (crs--link-poll-failed err))
     ((equal (cdr (assq 'status reply)) "pending") (crs--link-schedule-poll))
     (t (crs--link-finish reply)))))

(defun crs--link-poll-failed (reason)
  "Keep polling after a poll failed for REASON, until the code expires.
The server carries on with the link meanwhile; a dropped connection is
reopened by the next poll."
  (if (crs--link-expired-p)
      (progn
        (crs--link-stop-polling)
        (message "Stopped waiting for the GitHub link (%s); M-x crs-github-link-status shows where it stands"
                 reason))
    (crs--link-schedule-poll)))

;;;###autoload
(defun crs-unlink-github ()
  "Unlink your GitHub account from the shared server.
Your reviews stop going out as you: the server refuses them until you
link again, or sends them as its own account where it is set up to."
  (interactive)
  (crs--link-require-remote "Unlinking a GitHub account")
  (when (y-or-n-p (format "Unlink your GitHub account from %s? " (crs--remote-where)))
    (crs--link-stop-polling)
    (crs--send-request
     "RPCHandler.UnlinkGitHub" (vector)
     (lambda (reply)
       (let ((err (crs--remote-reply-error reply)))
         (if err
             (message "Could not unlink your GitHub account: %s" err)
           (setf (alist-get 'github_linked crs--remote-session) nil)
           (message "%s" (cdr (assq 'message reply)))))))))

(defun crs--link-status-text (reply)
  "The GetGitHubLink REPLY, as `crs-github-link-status' reports it."
  (let ((err (crs--remote-reply-error reply))
        (status (cdr (assq 'status reply)))
        (text (cdr (assq 'message reply))))
    (cond
     (err (format "Could not read your GitHub link: %s" err))
     ((equal status "linked")
      (format "GitHub: linked as %s; your reviews go out as you" (cdr (assq 'github_login reply))))
     ((equal status "unlinked")
      "GitHub: not linked; M-x crs-link-github links your account so your reviews go out as you")
     ((equal status "pending")
      (format "GitHub: waiting for %s to be entered at %s"
              (cdr (assq 'user_code reply)) (cdr (assq 'verification_uri reply))))
     ((equal status "unavailable") (format "GitHub: this server can't link accounts (%s)" text))
     (t (format "GitHub: %s; M-x crs-link-github to try again" text)))))

;;;###autoload
(defun crs-github-link-status ()
  "Say whether your GitHub account is linked on the shared server."
  (interactive)
  (crs--link-require-remote "A GitHub link")
  (crs--send-request "RPCHandler.GetGitHubLink" (vector)
                     (lambda (reply) (message "%s" (crs--link-status-text reply)))))

(defun crs-list-plugins ()
  "Call the ListPlugins RPC method and store the result in `crs-plugins`."
  (interactive)
  (crs--send-request
   "RPCHandler.ListPlugins"
   (vector)
   (lambda (result)
     (let ((plugins-list (append (cdr (assq 'plugins result)) nil)))
       (setq crs-plugins-full plugins-list)
       (setq crs-plugins (mapcar (lambda (p) (cdr (assq 'Name p))) plugins-list))
       (message "Plugins updated: %d plugins found" (length crs-plugins))))))


;;;###autoload
(defun crs-get-rate-limit-status ()
  "Show the GitHub API rate limit status in the minibuffer."
  (interactive)
  (crs-start-server)
  (crs--send-request
   "RPCHandler.GetRateLimitStatus"
   (vector)
   (lambda (result)
     (if (cdr (assq 'error result))
         (message "Error fetching rate limit status: %s"
                  (cdr (assq 'error result)))
       (let* ((remaining (cdr (assq 'remaining result)))
              (limit (cdr (assq 'limit result)))
              (reset-at (cdr (assq 'reset_at result)))
              (total (cdr (assq 'total_requests result)))
              (throttled (cdr (assq 'throttled_count result)))
              (rate-limited (cdr (assq 'rate_limited_count result))))
         (message "Rate limit: %d/%d remaining (resets %s) | requests: %d, throttled: %d, rate-limited: %d"
                  remaining limit reset-at total throttled rate-limited))))))

(provide 'crs-rpc)
;;; crs-rpc.el ends here
