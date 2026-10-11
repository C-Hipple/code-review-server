;;; crs-rpc.el --- JSON-RPC process management and server lifecycle for crs. -*- lexical-binding: t; -*-

;;; Commentary:

;; JSON-RPC process management and server lifecycle for crs.

;;; Code:

(require 'json)
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
             (crs--remote-ready (cdr (assq 'login reply))))
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
      (crs--remote-ready (cdr (assq 'login reply)))))))

(defun crs--remote-ready (login)
  "Mark the connection authenticated as LOGIN and send what waited for it."
  (setq crs--remote-ready t
        crs--remote-last-error nil)
  (message "Connected to %s%s" (crs--remote-where)
           (if (crs--non-empty login) (format " as %s" login) ""))
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
