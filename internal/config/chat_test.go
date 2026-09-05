package config

import (
	"errors"
	"testing"
)

func TestValidateAcceptsChatProvider(t *testing.T) {
	d := validDoc()
	d.Providers["custom"] = Provider{Wire: WireOpenAIChat, BaseURL: "http://127.0.0.1:9000/v1", Models: []string{"m1"}}
	if err := d.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestValidateRejectsChatProviderWithoutBaseURL(t *testing.T) {
	d := validDoc()
	d.Providers["custom"] = Provider{Wire: WireOpenAIChat}
	if err := d.validate(); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("want ErrInvalidValue, got %v", err)
	}
}
