package sqlite

// Column lists reused across SQL queries in session.go and user_llm_subscription.go.

// SessionMessageSelectCols lists the non-system columns read from session_messages
// by GetAllMessages, GetHistory, and related queries.
const sessionMessageSelectCols = "role, content, tool_call_id, tool_name, tool_arguments, tool_calls, detail, reasoning_content, created_at"

// UserLLMSubscriptionSelectCols lists the columns read from user_llm_subscriptions
// by List, ListAll, Get, GetSystemSubscription, and related queries.
const userLLMSubscriptionSelectCols = "id, sender_id, name, provider, base_url, api_key, model, enabled, max_context, max_output_tokens, thinking_mode, api_type, cached_models, created_at, updated_at, is_system"

// UserLLMSubscriptionInsertCols lists the non-auto columns inserted into user_llm_subscriptions.
const userLLMSubscriptionInsertCols = "id, sender_id, name, provider, base_url, api_key, model, enabled, max_context, max_output_tokens, thinking_mode, api_type, is_system, created_at, updated_at"
