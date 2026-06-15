package llm

import (
	"errors"
	"strings"
	"testing"
)

func TestParseDataURL(t *testing.T) {
	tests := []struct {
		name     string
		dataURL  string
		wantMime string
		wantData string
		wantOK   bool
	}{
		{
			name:     "PNG data URL",
			dataURL:  "data:image/png;base64,iVBORw0KGgo=",
			wantMime: "image/png",
			wantData: "iVBORw0KGgo=",
			wantOK:   true,
		},
		{
			name:     "JPEG data URL",
			dataURL:  "data:image/jpeg;base64,/9j/4AAQ=",
			wantMime: "image/jpeg",
			wantData: "/9j/4AAQ=",
			wantOK:   true,
		},
		{
			name:     "not a data URL",
			dataURL:  "https://example.com/image.png",
			wantMime: "",
			wantData: "",
			wantOK:   false,
		},
		{
			name:     "empty mime",
			dataURL:  "data:;base64,SGVsbG8=",
			wantMime: "",
			wantData: "",
			wantOK:   false,
		},
		{
			name:     "empty data",
			dataURL:  "data:image/png;base64,",
			wantMime: "",
			wantData: "",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mime, data, ok := parseDataURL(tt.dataURL)
			if ok != tt.wantOK {
				t.Errorf("parseDataURL() ok = %v, want %v", ok, tt.wantOK)
			}
			if mime != tt.wantMime {
				t.Errorf("parseDataURL() mime = %q, want %q", mime, tt.wantMime)
			}
			if data != tt.wantData {
				t.Errorf("parseDataURL() data = %q, want %q", data, tt.wantData)
			}
		})
	}
}

func TestDataURLToAnthropicImage(t *testing.T) {
	t.Run("valid PNG", func(t *testing.T) {
		block, ok := dataURLToAnthropicImage("data:image/png;base64,iVBORw0KGgo=")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if block.Type != "image" {
			t.Errorf("expected type 'image', got %q", block.Type)
		}
		if block.Source.Type != "base64" {
			t.Errorf("expected source type 'base64', got %q", block.Source.Type)
		}
		if block.Source.MediaType != "image/png" {
			t.Errorf("expected media type 'image/png', got %q", block.Source.MediaType)
		}
		if block.Source.Data != "iVBORw0KGgo=" {
			t.Errorf("expected data 'iVBORw0KGgo=', got %q", block.Source.Data)
		}
	})

	t.Run("invalid URL", func(t *testing.T) {
		_, ok := dataURLToAnthropicImage("not-a-data-url")
		if ok {
			t.Error("expected ok=false for invalid URL")
		}
	})
}

func TestStripEmbeddedImages(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
	}{
		{
			name:  "no images",
			input: "Hello world",
			want:  "Hello world",
		},
		{
			name:  "single image removed",
			input: "Look at this ![image](data:image/png;base64,abc123) please",
			want:  "Look at this  please",
		},
		{
			name:  "multiple images removed",
			input: "![img1](data:image/png;base64,aaa) and ![img2](data:image/jpeg;base64,bbb)",
			want:  " and ",
		},
		{
			name:  "no data prefix",
			input: "![img](https://example.com/test.png)",
			want:  "![img](https://example.com/test.png)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripEmbeddedImages(tt.input)
			if got != tt.want {
				t.Errorf("stripEmbeddedImages() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsVisionUnsupportedError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"image not supported", errors.New("image type is not supported by this model"), true},
		{"vision not supported", errors.New("This model does not have vision capabilities"), true},
		{"multimodal error", errors.New("multimodal input not allowed"), true},
		{"invalid content", errors.New("invalid content type in request"), true},
		{"unrelated error", errors.New("rate limit exceeded"), false},
		{"network error", errors.New("connection refused"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isVisionUnsupportedError(tt.err)
			if got != tt.want {
				t.Errorf("isVisionUnsupportedError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMessagesHaveEmbeddedImages(t *testing.T) {
	tests := []struct {
		name     string
		messages []ChatMessage
		want     bool
	}{
		{
			name:     "no messages",
			messages: []ChatMessage{},
			want:     false,
		},
		{
			name: "text only",
			messages: []ChatMessage{
				{Role: "user", Content: "Hello"},
			},
			want: false,
		},
		{
			name: "with data URL image",
			messages: []ChatMessage{
				{Role: "user", Content: "![img](data:image/png;base64,abc)"},
			},
			want: true,
		},
		{
			name: "assistant message with data (ignored)",
			messages: []ChatMessage{
				{Role: "assistant", Content: "![img](data:image/png;base64,abc)"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := messagesHaveEmbeddedImages(tt.messages)
			if got != tt.want {
				t.Errorf("messagesHaveEmbeddedImages() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStripImagesFromMessages(t *testing.T) {
	original := []ChatMessage{
		{Role: "system", Content: "You are helpful"},
		{Role: "user", Content: "Look at ![img](data:image/png;base64,abc) this"},
		{Role: "assistant", Content: "I see it"},
		{Role: "user", Content: "Another ![img2](data:image/jpeg;base64,def) image"},
	}

	stripped := stripImagesFromMessages(original)

	// Original should be unchanged
	if original[1].Content != "Look at ![img](data:image/png;base64,abc) this" {
		t.Error("original message was modified")
	}

	// Stripped should have no data URLs
	for i, msg := range stripped {
		if strings.Contains(msg.Content, "data:image") {
			t.Errorf("message %d still contains data URL: %q", i, msg.Content)
		}
	}

	// System and assistant messages should be unchanged
	if stripped[0].Content != "You are helpful" {
		t.Error("system message was modified")
	}
	if stripped[2].Content != "I see it" {
		t.Error("assistant message was modified")
	}
}

func TestParseEmbeddedImages(t *testing.T) {
	t.Run("no images", func(t *testing.T) {
		parts := parseEmbeddedImages("Hello world")
		if len(parts) != 1 || parts[0].Type != "text" {
			t.Fatalf("expected 1 text part, got %d parts", len(parts))
		}
	})

	t.Run("with image", func(t *testing.T) {
		content := "Before ![alt](data:image/png;base64,abc) After"
		parts := parseEmbeddedImages(content)
		if len(parts) != 3 {
			t.Fatalf("expected 3 parts (text+image+text), got %d", len(parts))
		}
		if parts[0].Type != "text" || parts[0].Text != "Before" {
			t.Errorf("first part wrong: %+v", parts[0])
		}
		if parts[1].Type != "image" {
			t.Errorf("second part should be image, got %s", parts[1].Type)
		}
		if parts[2].Type != "text" || parts[2].Text != "After" {
			t.Errorf("third part wrong: %+v", parts[2])
		}
	})
}
