# Session multi-select

Desktop `CoreSessionsPanel` and mobile `SessionSidebar` share `useSessionSelection`, `SessionMultiSelectButton` and `SessionBatchActions`. Do not put a list-level action in only one container.

- Clicking an eligible main row or pressing Space/Enter toggles its checkbox, without activating a conversation. Synthetic parents and SubAgents remain ineligible; their rows also do not navigate during selection.
- Shift adds the range in rendered group/sort order. All selects loaded main sessions matching the current search, excluding collapsed groups. A pagination hint explicitly states when more sessions are unloaded. This is not a server-wide search/select-all.
- Search, channel and category changes clear selection immediately. Removed or collapsed rows are pruned; revealing them does not reselect them. New pages are not automatically selected.
- Escape exits and Ctrl/Cmd+A selects all only from the list container, excluding editors, menus/dialogs, IME and repeated/handled events. No window-level listener.
- Explicit star/unstar avoids inverting mixed selections; read marks reuse the store's ordinary markRead persistence.
- Delete opens a confirmation containing count, labels and channels. Export chooses Native/OpenAI/Codex and makes one ZIP download with numbered, sanitized, channel-qualified names. The format conversion is shared with single exports; ZIP32 uses stored entries and UTF-8/CRC32 without a new package.
- Async operations capture confirmed rows and scope. Pending guards block duplicate requests and selection changes. Deletion inspects boolean success as well as rejected promises; partial failures remain selected and a count is announced. Scope changes cannot apply old results to a new selection. Deleted sessions reuse the ordinary store cache/unread/star/active-session cleanup.
- Selection is component-local and temporary, never persisted or synchronized as a user preference. No backend restart, new schema, bulk-stop, bulk-fork or rename templates.

Regression coverage: `SessionMultiSelect.test.tsx` renders both containers; `api.batchExport.test.ts` checks exact channel/ID requests, every format, duplicate names and partial/all failures. Browser integration is covered by `e2e/session-multiselect.spec.ts`.
