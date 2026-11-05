package tgbot

import (
	"context"
	"encoding/json"
	"io"
	"runtime"
	"sync"
	"testing"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"
)

// instantCaller answers every Bot API call at once, so the loops below spend
// their time on the connection handover and not on HTTP.
type instantCaller struct{}

func (instantCaller) Call(_ context.Context, _ string, data *telegoapi.RequestData) (*telegoapi.Response, error) {
	if data != nil && data.BodyStream != nil {
		_, _ = io.Copy(io.Discard, data.BodyStream)
	}
	return &telegoapi.Response{
		Ok:     true,
		Result: json.RawMessage(`{"message_id":7,"date":0,"chat":{"id":1,"type":"private"}}`),
	}, nil
}

// flipBot swaps the connection between a live bot and nil until the test ends,
// like a stop or a failed restart landing while a goroutine is mid-call.
func flipBot(t *testing.T) {
	t.Helper()
	live, err := telego.NewBot(testBotToken, telego.WithAPICaller(instantCaller{}), telego.WithDiscardLogger())
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	orig := liveBot()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				setTestBot(nil)
				setTestBot(live)
			}
		}
	})
	t.Cleanup(func() {
		close(stop)
		wg.Wait()
		setTestBot(orig)
	})
}

func recovered(call func()) (panicked any) {
	defer func() { panicked = recover() }()
	call()
	return nil
}

// Regression: a stop or a failed restart cleared the connection between a nil
// check and the call after it, and a nil *telego.Bot panics inside the library.
func TestConnectionClearedMidCallDoesNotPanic(t *testing.T) {
	if runtime.GOMAXPROCS(0) < 2 {
		t.Skip("needs a second CPU to clear the connection while a call is in flight")
	}
	tb := &Tgbot{}
	t.Cleanup(notices.reset)
	inlineCapability.mu.Lock()
	ok, known, at := inlineCapability.ok, inlineCapability.known, inlineCapability.at
	inlineCapability.mu.Unlock()
	t.Cleanup(func() {
		inlineCapability.mu.Lock()
		inlineCapability.ok, inlineCapability.known, inlineCapability.at = ok, known, at
		inlineCapability.mu.Unlock()
	})

	tests := []struct {
		name string
		call func()
	}{
		{"deleteOwnMessage", func() { tb.deleteOwnMessage(1, 7) }},
		{"deleteIncoming", func() { tb.deleteIncoming(&telego.Message{MessageID: 7, Chat: telego.Chat{ID: 1}}) }},
		{"deleteMessageTgBot", func() { tb.deleteMessageTgBot(1, 7) }},
		{"answerSilent", func() { tb.answerSilent("q") }},
		{"answerInline", func() { tb.answerInline("q", nil, "") }},
		{"botUsername", func() { botUsername() }},
		{"Capabilities", func() { _, _ = tb.Capabilities() }},
		{"refreshInlineCapability", func() { tb.refreshInlineCapability() }},
		{"liveNotice", func() { tb.liveNotice(1, "kind", "body") }},
		{"sendNoticeNoKeyboard", func() { tb.sendNoticeNoKeyboard(1, "text") }},
		{"sendNoticeReturningID", func() { tb.sendNoticeReturningID(1, "text") }},
		{"sendDocumentWithCaption", func() { tb.sendDocumentWithCaption(1, []byte("db"), "x-ui.db", "caption") }},
		{"sendTGPicker", func() { tb.sendTGPicker(1, 1) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flipBot(t)
			for i := range 2000 {
				if p := recovered(tt.call); p != nil {
					t.Fatalf("call %d panicked while the connection was cleared mid-call: %v", i, p)
				}
			}
		})
	}
}
