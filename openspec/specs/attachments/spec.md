# Attachments Specification

## Purpose
Defines how attachment bytes move between the mailbox and clients: inline content, signed links, inline uploads and upload pages.

## Requirements

### Requirement: Inline attachment content
The `attachment` tool SHALL accept a positive integer ID, fetch metadata first, refuse attachments larger than the configured inline limit with `too_large` (pointing to `attachment_link` over HTTP), and return a text summary plus one content block: an image block for PNG, JPEG, GIF and WebP, an audio block for audio, text for text-like types, and an embedded binary resource otherwise. The content type SHALL be sniffed from the bytes before the declared MIME type is used.

#### Scenario: PDF
- **WHEN** a PDF attachment is requested
- **THEN** the result contains a summary and an embedded resource with MIME type `application/pdf` and the file bytes

### Requirement: Inline uploads for drafts and comments
`draft` and `comment` SHALL accept `attachments` as a list of `{name, content_base64}`; `draft` SHALL also accept `attachment_ids`. For a draft, the server SHALL create or edit the draft first, read its ID from the output when creating, then stream each file with `draft --edit=<id> --attach-stream=<name>` through standard input. For a comment with a `message_id`, the server SHALL post any text first and then stream each file as its own comment, passing the team and users. File names SHALL be stripped of path separators and control characters. Files above the upload limit, invalid base64, and attachments combined with `unshare`, `delete` or a comment `edit` MUST be rejected before any CLI call.

#### Scenario: New draft with a file
- **WHEN** `draft` is called with recipients, body and one attachment
- **THEN** the CLI creates the draft, then receives the file bytes on stdin for that draft ID, and the tool returns the final output including the deep link

### Requirement: Local paths only for local clients
Over stdio, `attach` paths SHALL be read by the server and streamed like inline attachments. Over HTTP, `attach` MUST be absent from the schema and any call using it MUST fail with `invalid_argument` without running the CLI.

#### Scenario: Remote client names a local file
- **WHEN** a client connected over HTTP calls `draft` with `attach: ["/etc/hosts"]`
- **THEN** the call fails and the CLI never runs

### Requirement: Signed download links
Over HTTP, `attachment_link` SHALL return a URL of the form `<public-base>/attachments/<id>?exp=<unix>&sig=<hmac>` valid for the requested lifetime (default 15 minutes, at most 24 hours), after confirming the attachment exists. `GET /attachments/<id>` SHALL be served to a request with a valid token or a valid signature, streaming the bytes with the sniffed content type, a `Content-Disposition` carrying the file name (`attachment` when `download=1`), `X-Content-Type-Options: nosniff` and `Content-Security-Policy: sandbox`. Signatures MUST cover kind, path, bound parameters and expiry, and MUST be verified in constant time.

#### Scenario: Tampered link
- **WHEN** the ID in a signed link is changed
- **THEN** the server responds `403`

### Requirement: Upload pages and raw uploads
Over HTTP, `attachment_upload_link` SHALL take exactly one of `draft_id` or `message_id` (numeric), optionally a team for comments bound into the signature, and return a signed `/upload/draft/<id>` or `/upload/comment/<message-id>` URL. The page SHALL let a person pick one or more files and SHALL stream each to the target, reporting what was added and linking to Spark's deep link when available. `PUT /drafts/<id>/attachments/<name>` and `POST /comments/<message-id>/attachments/<name>` SHALL accept a raw body from token-authenticated clients. Every upload MUST respect the upload limit.

#### Scenario: Upload from a phone
- **WHEN** a person opens an upload link for draft 123 and submits two files
- **THEN** both files are attached to draft 123 and the page lists them
