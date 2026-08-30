package sqlite

// Column lists reused across SQL queries in session.go and user_llm_subscription.go.

// UserLLMSubscriptionSelectCols lists the columns read from user_llm_subscriptions
// by List, ListAll, Get, and related queries.
const userLLMSubscriptionSelectCols = "id, sender_id, name, provider, base_url, api_key, model, enabled, max_context, max_output_tokens, thinking_mode, api_type, cached_models, created_at, updated_at"
