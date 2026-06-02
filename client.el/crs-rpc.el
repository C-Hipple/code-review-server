;;; crs-rpc.el --- JSON-RPC process management and server lifecycle for crs. -*- lexical-binding: t; -*-

;;; Commentary:

;; JSON-RPC process management and server lifecycle for crs.

;;; Code:

(require 'json)
(require 'crs-vars)

;;;###autoload
(defun crs-start-server ()
  "Start the crs JSON-RPC server process.
Returns the process handle."
  (interactive)
  (if (and crs--process
           (process-live-p crs--process))
      (progn
        (message "Server is already running")
        crs--process)
    (progn
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
            (error "Server process died immediately. Stderr: %s" stderr-content)))))

    (message "Started crs JSON-RPC server")
    (crs-list-plugins)
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

(defun crs--process-filter (process output)
  "Filter function for processing JSON-RPC responses from the server."
  (setq crs--response-buffer
        (concat crs--response-buffer output))

  ;; Process complete lines (JSON-RPC uses newline-delimited JSON)
  (while (string-match "\n" crs--response-buffer)
    (let ((line (substring crs--response-buffer 0 (match-beginning 0))))
      (setq crs--response-buffer
            (substring crs--response-buffer (match-end 0)))

      (when (> (length line) 0)
        (crs--handle-response line)))))

(defun crs--handle-response (response-line)
  "Handle a single JSON-RPC response line."
  (message "DEBUG crs response: %s" response-line)
  (condition-case err
      (let ((response (json-read-from-string response-line)))
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
CALLBACK is a function to call with the result."
  (unless (and crs--process
               (process-live-p crs--process))
    (error "Server is not running. Call crs-start-server first"))

  (let ((id crs--request-id))
    (setq crs--request-id (1+ crs--request-id))

    (puthash id callback crs--pending-requests)

    (let ((request (json-encode `((jsonrpc . "2.0")
                                  (method . ,method)
                                  (params . ,params)
                                  (id . ,id)))))
      (process-send-string crs--process
                           (concat request "\n")))))

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
