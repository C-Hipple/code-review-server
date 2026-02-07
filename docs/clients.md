# Clients

Code Review Server ships with a web client and an emacs client, but you can build your own using the [Protocol](protocol.md).

## Web Client (bun)

The web client is packaged with bun, and has a bun backend with a react frontend. If you build and run the bun backend, you'll get a working webserver on `localhost:3000` which lists all of your PRs.

### Installation

1. `cd` to `bun_client`
2. `bun install && bun run build`
3. `bun start`

## Emacs Client

The emacs client is in `client.el`.

### Installation

#### Spacemacs

```elisp
   ;; in dotspacemacs-additional-packages
   (code-review-server :location (recipe
                      :fetcher github
                      :repo "C-Hipple/gtdbot"
                      :files ("*.el")))
```

### Usage

1. Open `client.el` && evaluate the buffer
2. Run commands:

```elisp
(crs-start-server) ;; to start the processing
(crs-get-reviews)  ;; Load your required reviews into an ephermeral org-mode buffer

;; To start a review
(crs-start-review-at-point)  ;; when your cursor is on a github URL


(crs-get-review "C-Hipple" "code-review-server" 1)  ;; Start it directly.
```

Starting a review will then load a new code-review buffer which you can read the review, make comments, and submit your review.
