package mail

import (
	netmail "net/mail"
	"strings"
	"testing"
)

func TestMessageHeaders(t *testing.T) {
	from, _ := netmail.ParseAddress("Digest <noreply@digest.example>")
	to, _ := netmail.ParseAddress("reader@example.org")
	msg := message(from, to, "Reset your Café Digest password", "Hello.\nLine two.")
	head, body, ok := strings.Cut(msg, "\r\n\r\n")
	if !ok || body != "Hello.\nLine two." {
		t.Fatalf("message:\n%s", msg)
	}
	for _, want := range []string{"From: \"Digest\" <noreply@digest.example>", "To: <reader@example.org>",
		"Subject: =?utf-8?q?", "Date: ", "Message-ID: <", "@digest.example>", "MIME-Version: 1.0", "charset=utf-8"} {
		if !strings.Contains(head, want) {
			t.Errorf("missing %q in headers:\n%s", want, head)
		}
	}
}
