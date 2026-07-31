package llm

import (
	"errors"
	"testing"
)

func TestIsMaxTokensParamError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "max_tokens rejected, suggests max_completion_tokens",
			err:  errors.New(`POST "url": 400 Bad Request {"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","code":"invalid_request_body"}`),
			want: "use_new",
		},
		{
			name: "max_completion_tokens rejected, no suggestion",
			err:  errors.New(`POST "url": 400 Bad Request {"message":"Unsupported parameter: 'max_completion_tokens' is not supported with this model.","code":"invalid_request_body"}`),
			want: "use_legacy",
		},
		{
			name: "max_completion_tokens rejected, suggests max_tokens",
			err:  errors.New(`POST "url": 400 Bad Request {"message":"Unsupported parameter: 'max_completion_tokens' is not supported with this model. Use 'max_tokens' instead.","code":"invalid_request_body"}`),
			want: "use_legacy",
		},
		{
			name: "unrelated 400 error",
			err:  errors.New(`POST "url": 400 Bad Request {"message":"invalid model","code":"invalid_request_body"}`),
			want: "",
		},
		{
			name: "nil error",
			err:  nil,
			want: "",
		},
		{
			name: "max_tokens mentioned but not unsupported",
			err:  errors.New(`max_tokens must be a positive integer`),
			want: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isMaxTokensParamError(c.err)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestShouldFallbackToStreamForNonStreamResponse(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "openai-go SSE content type mismatch",
			err:  errors.New(`expected destination type of 'string' or '[]byte' for responses with content-type 'text/event-stream' that is not 'application/json'`),
			want: true,
		},
		{
			name: "SSE with charset",
			err:  errors.New(`content-type 'text/event-stream; charset=utf-8' that is not 'application/json'`),
			want: true,
		},
		{
			name: "plain JSON API error",
			err:  errors.New(`400 Bad Request: invalid_request_error`),
			want: false,
		},
		{
			name: "SSE mentioned without JSON mismatch",
			err:  errors.New(`upstream returned text/event-stream but stream ended unexpectedly`),
			want: false,
		},
		{
			name: "nil",
			err:  nil,
			want: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shouldFallbackToStreamForNonStreamResponse(c.err)
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
