/**
 * Shared constants for the xbot Web UI.
 * Centralises magic numbers to a single source of truth.
 */

/** Truncation length for reply previews (ReplyPreview component & replyTo reference) */
export const REPLY_PREVIEW_LENGTH = 80

/** Truncation length for the reply indicator bar above the input (narrower space) */
export const REPLY_INDICATOR_LENGTH = 60

/** Truncation length for notification message bodies */
export const NOTIFICATION_PREVIEW_LENGTH = 100

/** Truncation length for preset command tooltips */
export const PRESET_TOOLTIP_LENGTH = 50

/** Truncation length for preset content previews */
export const PRESET_CONTENT_PREVIEW_LENGTH = 40

/** Truncation length for error message previews (Mermaid etc.) */
export const ERROR_PREVIEW_LENGTH = 200

/** Virtual list row height estimates */
export const VIRTUAL_ROW_HEIGHT_USER = 80
export const VIRTUAL_ROW_HEIGHT_ASSISTANT = 200

/** Line count threshold for collapsing long messages */
export const COLLAPSE_LINE_THRESHOLD = 20

/** Line count threshold for collapsing long code blocks */
export const CODEBLOCK_COLLAPSE_LINES = 30
