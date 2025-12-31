Creates a review on a specified pull request.

This endpoint triggers notifications. Creating content too quickly using this endpoint may result in secondary rate limiting. For more information, see "Rate limits for the API" and "Best practices for using the REST API."

Pull request reviews created in the PENDING state are not submitted and therefore do not include the submitted_at property in the response. To create a pending review for a pull request, leave the event parameter blank. For more information about submitting a PENDING review, see "Submit a review for a pull request."

Note

To comment on a specific line in a file, you need to first determine the position of that line in the diff. To see a pull request diff, add the application/vnd.github.v3.diff media type to the Accept header of a call to the Get a pull request endpoint.

The position value equals the number of lines down from the first "@@" hunk header in the file you want to add a comment. The line just below the "@@" line is position 1, the next line is position 2, and so on. The position in the diff continues to increase through lines of whitespace and additional hunks until the beginning of a new file.

This endpoint supports the following custom media types. For more information, see "Media types."

    application/vnd.github-commitcomment.raw+json: Returns the raw markdown body. Response will include body. This is the default if you do not pass any specific media type.
    application/vnd.github-commitcomment.text+json: Returns a text only representation of the markdown body. Response will include body_text.
    application/vnd.github-commitcomment.html+json: Returns HTML rendered from the body's markdown. Response will include body_html.
    application/vnd.github-commitcomment.full+json: Returns raw, text, and HTML representations. Response will include body, body_text, and body_html.

Fine-grained access tokens for "Create a review for a pull request"

This endpoint works with the following fine-grained token types:

    GitHub App user access tokens
    GitHub App installation access tokens
    Fine-grained personal access tokens

The fine-grained token must have the following permission set:

    "Pull requests" repository permissions (write)

Parameters for "Create a review for a pull request"
Headers
Name, Type, Description
accept string

Setting to application/vnd.github+json is recommended.
Path parameters
Name, Type, Description
owner string Required

The account owner of the repository. The name is not case sensitive.
repo string Required

The name of the repository without the .git extension. The name is not case sensitive.
pull_number integer Required

The number that identifies the pull request.
Body parameters
Name, Type, Description
commit_id string

The SHA of the commit that needs a review. Not using the latest commit SHA may render your review comment outdated if a subsequent commit modifies the line you specify as the position. Defaults to the most recent commit in the pull request when you do not specify a value.
body string

Required when using REQUEST_CHANGES or COMMENT for the event parameter. The body text of the pull request review.
event string

The review action you want to perform. The review actions include: APPROVE, REQUEST_CHANGES, or COMMENT. By leaving this blank, you set the review action state to PENDING, which means you will need to submit the pull request review when you are ready.

Can be one of: APPROVE, REQUEST_CHANGES, COMMENT
comments array of objects

Use the following table to specify the location, destination, and contents of the draft review comment.
Properties of comments
Name, Type, Description
path string Required

The relative path to the file that necessitates a review comment.
position integer

The position in the diff where you want to add a review comment. Note this value is not the same as the line number in the file. The position value equals the number of lines down from the first "@@" hunk header in the file you want to add a comment. The line just below the "@@" line is position 1, the next line is position 2, and so on. The position in the diff continues to increase through lines of whitespace and additional hunks until the beginning of a new file.
body string Required

Text of the review comment.
line integer
side string
start_line integer
start_side string
