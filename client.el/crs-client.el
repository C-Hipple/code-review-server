;;; crs-client.el --- JSON-RPC client for codereviewserver -*- lexical-binding: t; -*-

;; Author: Chris Hipple
;; Version: 0.1.0
;; Package-Requires: ((emacs "25.1"))

;; SPDX-License-Identifier: GPL-3.0+

;;; Commentary:

;; This package provides JSON-RPC client functionality for crs (codereviewserver).
;; It allows starting the server process and making RPC calls to it.

;;; Code:

(require 'crs-vars)
(require 'crs-html)
(require 'crs-rpc)
(require 'crs-render)
(require 'crs-list-mode)
(require 'crs-review)
(require 'crs-comments)
(require 'crs-review-actions)
(require 'crs-plugins)

(provide 'crs-client)

;;; crs-client.el ends here
