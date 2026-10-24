package push

import (
	"regexp"
	"testing"
)

func TestWebPushTopicIsRFC8030Valid(t *testing.T) {
	key := "web:chat_18E098316EE7ACF5:1729560000"
	topic := webPushTopic(key)
	if len(topic) != 32 {
		t.Fatalf("topic length = %d (%q), want 32", len(topic), topic)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`).MatchString(topic) {
		t.Fatalf("topic %q is not base64url / <=32 chars", topic)
	}
	if got := webPushTopic(key); got != topic {
		t.Fatalf("topic is not deterministic: %q != %q", got, topic)
	}
	if got := webPushTopic("web:chat_other:1729560000"); got == topic {
		t.Fatalf("different collapse keys produced the same topic %q", got)
	}
	if got := webPushTopic(""); got != "" {
		t.Fatalf("empty collapse key should keep an empty topic, got %q", got)
	}
}
