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

;; Remote mode: a shared server started with `crs --listen' (crs-rpc.el).
(defcustom crs-remote-address nil
  "Address (\"HOST:PORT\") of a shared crs server, or nil to run crs locally.
Nil, `crs-start-server' runs its own `crs -server' as it always has.
Set, it connects to a server started with `crs --listen' instead and
authenticates as you with the token in `crs-remote-token-file',
registering with `crs-remote-registration-key' the first time (and
again if the server has dropped you after a stretch of inactivity).
Anything that reads a path on the server's host, such as visiting a
file from a review or the images the server caches, works only where
the same path exists on this machine."
  :type '(choice (const :tag "Run crs locally" nil)
                 (string :tag "host:port"))
  :group 'crs)

(defcustom crs-remote-tls nil
  "When non-nil, speak TLS to `crs-remote-address'.
A shared server listens in plain TCP; turn this on when a proxy in
front of it terminates TLS."
  :type 'boolean
  :group 'crs)

(defcustom crs-remote-token-file
  (expand-file-name "remote_token" (or (getenv "CRS_HOME") "~/.crs"))
  "File holding the token a shared server issued you.
It is written, readable by you only, when the server registers you.
The environment variable CRS_REMOTE_TOKEN wins over it when set.  The
web client's bridge keeps its token in the same file by default, so
both clients connect as the same user."
  :type 'file
  :group 'crs)

(defcustom crs-remote-login nil
  "The GitHub login a shared server registers you under.
Nil means the environment variable CRS_LOGIN, else the root
GithubUsername of your codereviewserver.toml."
  :type '(choice (const :tag "CRS_LOGIN, or GithubUsername in the config file" nil)
                 string)
  :group 'crs)

(defcustom crs-remote-registration-key nil
  "A shared server's registration key, needed only to register.
Nil means the environment variable CRS_REGISTRATION_KEY."
  :type '(choice (const :tag "CRS_REGISTRATION_KEY" nil)
                 string)
  :group 'crs)

(defcustom crs-remote-user-config-command '("crs" "-print-user-config")
  "Command printing what registering with a shared server sends.
It reads your local codereviewserver.toml and prints its Workflows,
Repos, SectionPriority and SectionSorting as one JSON object, so this
client needs no TOML parser.  Any command printing that object works."
  :type '(repeat string)
  :group 'crs)

(defcustom crs-link-github-browse t
  "When non-nil, `crs-link-github' opens GitHub's code page with `browse-url'.
Turn it off where Emacs has no browser to hand; the page and the code
are shown in a buffer either way."
  :type 'boolean
  :group 'crs)

(defvar crs--remote-ready nil
  "Non-nil once the connection to `crs-remote-address' is authenticated.")

(defvar crs--remote-session nil
  "Who the last connect flow signed in as, from its Authenticate or
RegisterUser reply: an alist with `login', `is_admin', `github_linked'
and `github_link_available'.  Nil until a flow succeeds.")

(defvar crs--link-poll-timer nil
  "Timer polling GetGitHubLink while a GitHub link is pending, or nil.")

(defvar crs--link-pending nil
  "The GitHub link `crs-link-github' is waiting on, or nil.
A plist with :code, :uri, :expires (an RFC 3339 string) and :interval
\(seconds between polls), from the StartGitHubLink reply.")

(defvar crs--remote-queue nil
  "Requests waiting for the remote connect flow, newest first.
Each entry is a list (METHOD PARAMS CALLBACK).")

(defvar crs--remote-last-error nil
  "Why the last remote connect flow failed, or nil.")

(defvar crs--remote-issued-token nil
  "A token the shared server issued while CRS_REMOTE_TOKEN named another.
It wins over CRS_REMOTE_TOKEN for the rest of the session.")

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

;; AI features (crs-ai.el).
(defvar crs-ai-features nil
  "The AI features the server has enabled, as ListAIFeatures reports them.
Only those with a report to open: the ones the server applies itself
are left out.  Each entry is an alist with at least `id' and `name'.
Nil until fetched.")

(defcustom crs-ai-poll-interval 2
  "Seconds between polls of an AI feature run that is still pending."
  :type 'number
  :group 'crs)

(defcustom crs-ai-poll-timeout 360
  "Stop polling a pending AI feature run after this many seconds.
The server gives a run five minutes, so this leaves room for one to land."
  :type 'number
  :group 'crs)

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
(defvar-local crs--ai-poll-timer nil
  "Timer polling a pending run for this AI output buffer.")
(defvar-local crs--ai-poll-started nil
  "When the current round of polling began, as a float time.")
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
