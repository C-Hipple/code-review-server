;;; crs-vars.el --- Shared state, custom options and faces for the crs client. -*- lexical-binding: t; -*-

;;; Commentary:

;; Shared state, custom options and faces for the crs client.

;;; Code:

(require 'seq)
(require 'subr-x)

(defgroup crs nil
  "Emacs client for the code review server."
  :group 'tools
  :prefix "crs-")

(defvar crs--process nil
  "The process handle for the crs JSON-RPC server.")

(defvar crs--request-id 1
  "Counter for JSON-RPC request IDs.")

(defvar crs--pending-requests (make-hash-table :test 'equal)
  "Hash table mapping request IDs to callback functions.")

(defvar crs--response-pending nil
  "Reversed list of output chunks not yet terminated by a newline.
Chunks are kept unjoined so that accumulating a large single-line reply
costs one concatenation when it completes rather than one per chunk.")

(defcustom crs-debug nil
  "When non-nil, log JSON-RPC responses to the *Messages* buffer.
Off by default: a reply can run to megabytes (a GetPR reply carries the
whole diff), and logging one costs both the echo-area redisplay and a
permanent copy in *Messages*.  Logged responses are truncated to
`crs-debug-max-length' characters."
  :type 'boolean
  :group 'crs)

(defcustom crs-debug-max-length 2000
  "Maximum number of characters of a response logged when `crs-debug' is non-nil."
  :type 'integer
  :group 'crs)

(defvar crs-reviews-buffer-name "* Reviews *"
  "Name of the buffer used to display the reviews list.")

(defvar crs-plugins nil
  "List of plugin names configured on the server.")

(defvar crs-plugins-full nil
  "List of full plugin objects from the server, including OnlyOnDemand flag.")

(defcustom crs-include-comments-tree nil
  "When non-nil, include a comments sub-tree for each PR in the GetAllReviews org output.
This is passed to the server as IncludeComments, so the comments are left
out of the reply entirely rather than stripped after transfer.  Disabled by
default because fetching and rendering comments slows down the reviews buffer."
  :type 'boolean
  :group 'crs)

(defcustom crs-shr-render-timeout 2
  "Seconds to wait for shr HTML rendering before falling back to plain text.
Set to nil to disable the timeout."
  :type '(choice (number :tag "Seconds")
                 (const :tag "No timeout" nil))
  :group 'crs)

(defvar crs--section-header-regexp
  "^\\(?:[^[:space:]].*?[[:space:]]\\)?\\(?:\\(?:\\.\\.\\.\\)?\\(?:modified\\|deleted\\|new file\\|renamed\\)[[:space:]:]+.*\\|Commits .*\\|Description\\|Conversation\\|Changes\\|Your Review Feedback\\|Files changed .*\\)$"
  "Regexp to match section headers in the code review buffer.")

(defconst crs--html-placeholder-regexp
  "<CRS-HTML\\(?: prefix=\"\\(.*?\\)\"\\)?>\\(.*?\\)</CRS-HTML>"
  "Regexp to match HTML placeholders for deferred rendering.")


;; Buffer-local state for review buffers and comment editing.
(defvar-local crs--comment-owner nil)
(defvar-local crs--comment-repo nil)
(defvar-local crs--comment-number nil)
(defvar-local crs--comment-filename nil)
(defvar-local crs--comment-position nil)
(defvar-local crs--comment-reply-to-id nil)
(defvar-local crs--comment-editing-id nil
  "When non-nil, we're editing an existing local comment with this ID.")
(defvar-local crs--comment-original-line nil
  "The line number in the review buffer where the comment was started.")
(defvar-local crs--comment-original-context nil
  "Context for restoring position: (filename position file-line).")

;; Buffer-local variables for storing PR data separately
(defvar-local crs--buffer-owner nil
  "The GitHub owner for the current PR buffer.")
(defvar-local crs--buffer-diff nil
  "The raw diff content for the current PR.")
(defvar-local crs--buffer-comments nil
  "The comments list for the current PR.")
(defvar-local crs--buffer-outdated-comments nil
  "The outdated comments list for the current PR.")
(defvar-local crs--buffer-metadata nil
  "The PR metadata for the current PR.")
(defvar-local crs--buffer-reviews nil
  "The reviews list for the current PR.")
(defvar-local crs--buffer-preamble nil
  "The preamble content (header + conversation) for the current PR.")
(defvar-local crs--buffer-show-comments t
  "Whether to show comments in the buffer. Toggle with `crs-toggle-comments'.")
(defvar-local crs--buffer-annotations nil
  "The plugin annotations for the current PR, from the GetPR reply.")
(defvar-local crs--buffer-show-annotations nil
  "Whether to show full plugin annotation blocks in the buffer.
Annotations start collapsed to compact indicators; uncollapsing comments
with `crs-toggle-comments' also uncollapses annotations, and
`crs-toggle-annotations' toggles them on their own.")
(defvar-local crs--buffer-review-feedback nil
  "The review feedback for the current PR.")
(defvar-local crs--buffer-commits nil
  "The commits list for the current PR.")

;; Buffer-local state for plugin-output buffers.
(defvar-local crs--plugin-owner nil
  "Owner of the PR for plugin output.")
(defvar-local crs--plugin-repo nil
  "Repo of the PR for plugin output.")
(defvar-local crs--plugin-number nil
  "PR number for plugin output.")

(defvar-local crs--plugin-output-map nil
  "Hash table mapping plugin names to their output data.")

(defvar-local crs--plugin-name nil
  "If non-nil, this buffer displays output only for this plugin.")

(provide 'crs-vars)
;;; crs-vars.el ends here
