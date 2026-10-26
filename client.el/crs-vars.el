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

(defvar crs--review-submits (make-hash-table :test 'equal)
  "Review submits awaiting the server's reply, keyed by PR (\"owner/repo #N\").
Each value is the server process the submit was sent to.  A PR takes one
submit at a time, since a second would post its pending comments again.")

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
(defvar-local crs--buffer-images nil
  "Images embedded in the current PR, from the GetPR reply's `images'.
Each entry carries the URL as it appears in a body and the path of the
copy the server downloaded, which is what makes a screenshot on a private
repository renderable here: GitHub serves it only to an authenticated
request, and the server holds the token.")

;; Filtered diffs (crs-filtered-diff.el).  The buffer-local ones are
;; permanent: rendering re-enters the review buffer's major mode, which
;; would otherwise reset them on every render.
(defvar crs-diff-filters nil
  "The diff filters the server offers, from ListDiffFilters.
Each is an alist with `id', `name' and `description'.")
(defvar-local crs--buffer-diff-filter nil
  "ID of the diff filter the review buffer shows, or nil for the full diff.
While one is shown, `crs--buffer-diff' holds the filtered diff.")
(put 'crs--buffer-diff-filter 'permanent-local t)
(defvar-local crs--buffer-full-diff nil
  "The PR's full diff, kept while the review buffer shows a filtered one.")
(put 'crs--buffer-full-diff 'permanent-local t)
(defvar-local crs--buffer-filtered-diff nil
  "The GetFilteredDiff reply the review buffer shows, if any.")
(put 'crs--buffer-filtered-diff 'permanent-local t)

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

(defvar-local crs--plugin-refreshing nil
  "Non-nil while this plugin output buffer is fetching its output again.
It is `again' when another refresh was asked for meanwhile, which runs
once the one in flight lands.")

(defvar crs--plugin-runs-requested (make-hash-table :test 'equal)
  "Plugin runs asked for from this Emacs, keyed \"OWNER/REPO#NUMBER:PLUGIN\".
Each is announced in the echo area when the server says it landed.")

;; Server notifications (crs-rpc.el).
(defvar crs-server-notification-functions nil
  "Functions called with each notification the server sends.
Each is called with two arguments: the notification's METHOD, such as
\"Server.PluginOutputChanged\", and its DATA, the notification's
params[0] as an alist.  The server sends them once `crs-start-server'
has subscribed (RPCHandler.Subscribe).")

(defvar crs-server-subscribed-hook nil
  "Hook run each time a newly started server accepts the subscription.
Whatever it did before then was never announced, so buffers showing its
output read it again.")

(defconst crs--heartbeat-seconds 300
  "The heartbeat interval asked for when subscribing.
This client does nothing with the server's heartbeat, so it asks for the
slowest one the server allows.")

;; AI features (crs-ai.el).
(defvar crs-ai-features nil
  "The AI features the server has enabled, as ListAIFeatures reports them.
Only those with a report to open: the ones the server applies itself
are left out.  Each entry is an alist with at least `id' and `name'.
Nil until fetched.")

;; Buffer-local state for AI output buffers.
(defvar-local crs--ai-owner nil
  "Owner of the PR an AI output buffer shows.")
(defvar-local crs--ai-repo nil
  "Repo of the PR an AI output buffer shows.")
(defvar-local crs--ai-number nil
  "Number of the PR an AI output buffer shows.")
(defvar-local crs--ai-feature nil
  "The feature (an alist from ListAIFeatures) an AI output buffer shows.")
(defvar-local crs--ai-output nil
  "The last output the AI output buffer rendered, from GetAIOutput.")
(defvar-local crs--ai-insert-function nil
  "Function inserting an output into this AI buffer, or nil for a report.
It takes one argument, the feature's entry of a GetAIOutput reply.  Nil
means `crs--insert-ai-output'; the change-diagram buffer inserts the
diagram's Mermaid source instead (crs-diagram.el).")

(defconst crs-change-diagram-feature-id "change-diagram"
  "ID of the server's change-diagram AI feature.
Its report is a diagram, which `crs-show-change-diagram' shows in a
mermaid-mode buffer of its own rather than as a markdown report.")

(provide 'crs-vars)
;;; crs-vars.el ends here
