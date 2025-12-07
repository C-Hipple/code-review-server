;;; codereviewserver-rpc.el --- JSON-RPC client for codereviewserver -*- lexical-binding: t; -*-

;; Author: Chris Hipple
;; Version: 0.1.0
;; Package-Requires: ((emacs "25.1"))

;; SPDX-License-Identifier: GPL-3.0+

;;; Commentary:

;; This package provides JSON-RPC client functionality for codereviewserver.
;; It allows starting the server process and making RPC calls to it.

;;; Code:

(require 'json)

(defvar codereviewserver-rpc--process nil
  "The process handle for the codereviewserver JSON-RPC server.")

(defvar codereviewserver-rpc--request-id 1
  "Counter for JSON-RPC request IDs.")

(defvar codereviewserver-rpc--pending-requests (make-hash-table :test 'equal)
  "Hash table mapping request IDs to callback functions.")

(defvar codereviewserver-rpc--response-buffer ""
  "Buffer for accumulating partial JSON-RPC responses.")

;;;###autoload
(defun codereviewserver-rpc-start-server ()
  "Start the codereviewserver JSON-RPC server process.
Returns the process handle."
  (interactive)
  (if (and codereviewserver-rpc--process
           (process-live-p codereviewserver-rpc--process))
      (progn
        (message "Server is already running")
        codereviewserver-rpc--process)
    (progn
      (let ((stderr-buffer (get-buffer-create "*codereviewserver-rpc-stderr*")))
        (setq codereviewserver-rpc--process
              (make-process :name "codereviewserver-rpc"
                            :buffer "*codereviewserver-rpc*"
                            :command '("codereviewserver" "-server")
                            :connection-type 'pipe
                            :coding 'utf-8
                            :stderr stderr-buffer
                            :noquery t))

        (set-process-filter codereviewserver-rpc--process 'codereviewserver-rpc--process-filter)
        (set-process-sentinel codereviewserver-rpc--process 'codereviewserver-rpc--process-sentinel)

        ;; Give the process a moment to start up
        (sleep-for 0.2)

        ;; Check if process is still alive after startup
        (unless (process-live-p codereviewserver-rpc--process)
          (let ((stderr-content (with-current-buffer stderr-buffer (buffer-string))))
            (error "Server process died immediately. Stderr: %s" stderr-content)))))

    (message "Started codereviewserver JSON-RPC server")
    codereviewserver-rpc--process))

(defun codereviewserver-rpc--process-filter (process output)
  "Filter function for processing JSON-RPC responses from the server."
  (setq codereviewserver-rpc--response-buffer
        (concat codereviewserver-rpc--response-buffer output))

  ;; Process complete lines (JSON-RPC uses newline-delimited JSON)
  (while (string-match "\n" codereviewserver-rpc--response-buffer)
    (let ((line (substring codereviewserver-rpc--response-buffer 0 (match-beginning 0))))
      (setq codereviewserver-rpc--response-buffer
            (substring codereviewserver-rpc--response-buffer (match-end 0)))

      (when (> (length line) 0)
        (codereviewserver-rpc--handle-response line)))))

(defun codereviewserver-rpc--handle-response (response-line)
  "Handle a single JSON-RPC response line."
  (condition-case err
      (let ((response (json-read-from-string response-line)))
        (let ((id (cdr (assq 'id response)))
              (result (cdr (assq 'result response)))
              (error (cdr (assq 'error response))))
          (if error
              (message "JSON-RPC Error: %s" (cdr (assq 'message error)))
            (let ((callback (gethash id codereviewserver-rpc--pending-requests)))
              (when callback
                (remhash id codereviewserver-rpc--pending-requests)
                (funcall callback result))))))
    (error
     (message "Error parsing JSON-RPC response: %s" err))))

(defun codereviewserver-rpc--process-sentinel (process event)
  "Sentinel function for the codereviewserver process."
  (when (memq (process-status process) '(exit signal))
    (let ((buffer (process-buffer process))
          (stderr-buffer (get-buffer "*codereviewserver-rpc-stderr*")))
      (when buffer
        (with-current-buffer buffer
          (let ((output (buffer-string)))
            (when (> (length output) 0)
              (message "codereviewserver stdout: %s" output)))))
      (when stderr-buffer
        (with-current-buffer stderr-buffer
          (let ((stderr-output (buffer-string)))
            (when (> (length stderr-output) 0)
              (message "codereviewserver stderr: %s" stderr-output))))))
    (setq codereviewserver-rpc--process nil)
    (message "codereviewserver process %s" event)))

(defun codereviewserver-rpc--send-request (method params callback)
  "Send a JSON-RPC request to the server.
METHOD is the method name (e.g., 'RPCHandler.GetAllReviews').
PARAMS is the parameters array.
CALLBACK is a function to call with the result."
  (unless (and codereviewserver-rpc--process
               (process-live-p codereviewserver-rpc--process))
    (error "Server is not running. Call codereviewserver-rpc-start-server first"))

  (let ((id codereviewserver-rpc--request-id))
    (setq codereviewserver-rpc--request-id (1+ codereviewserver-rpc--request-id))

    (puthash id callback codereviewserver-rpc--pending-requests)

    (let ((request (json-encode `((jsonrpc . "2.0")
                                  (method . ,method)
                                  (params . ,params)
                                  (id . ,id)))))
      (process-send-string codereviewserver-rpc--process
                           (concat request "\n")))))

;;;###autoload
(defun codereviewserver-rpc-get-reviews ()
  "Call the GetAllReviews RPC method and display the result in '* Reviews *' buffer."
  (interactive)
  (unless (and codereviewserver-rpc--process
               (process-live-p codereviewserver-rpc--process))
    (codereviewserver-rpc-start-server)
    ;; Give the server a moment to start
    (sleep-for 0.5))

  (codereviewserver-rpc--send-request
   "RPCHandler.GetAllReviews"
   (vector)
   (lambda (result)
     (let ((content (cdr (assq 'Content result)))
           (buffer (get-buffer-create "* Reviews *")))
       (with-current-buffer buffer
         (erase-buffer)
         (insert (or content ""))
         (goto-char (point-min))
         (org-mode))
       (display-buffer buffer)
       (message "Reviews loaded into '* Reviews *' buffer")))))

(provide 'codereviewserver-rpc)

;;; codereviewserver-rpc.el ends here
