package email

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
	"github.com/mhsanaei/3x-ui/v3/internal/web/locale"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

// draftLocalizer stands in for the translations of the bot for the length of a test.
func draftLocalizer(t *testing.T, msgs ...*goi18n.Message) {
	t.Helper()
	bundle := goi18n.NewBundle(language.MustParse("en-US"))
	bundle.RegisterUnmarshalFunc("json", json.Unmarshal)
	_ = bundle.AddMessages(language.MustParse("en-US"), msgs...)
	orig := locale.SwapBotLocalizer(goi18n.NewLocalizer(bundle, "en-US"))
	t.Cleanup(func() { locale.SwapBotLocalizer(orig) })
}

func psiphonRegionsMail(t *testing.T) {
	t.Helper()
	draftLocalizer(t,
		&goi18n.Message{ID: "tgbot.messages.eventPsiphonRegionsNew", Other: "New Psiphon region available: {{ .Regions }}"},
		&goi18n.Message{ID: "tgbot.messages.time", Other: "Time: {{ .Time }}"},
		&goi18n.Message{ID: "email.labelStatus", Other: "Status"},
		&goi18n.Message{ID: "email.statusNew", Other: "NEW"},
		&goi18n.Message{ID: "email.labelRegions", Other: "Regions"},
	)
}

func TestANewPsiphonRegionIsNamedInTheMail(t *testing.T) {
	psiphonRegionsMail(t)

	subject, body := (&Subscriber{}).formatMessage(eventbus.Event{
		Type: eventbus.EventPsiphonRegionsNew,
		Data: &eventbus.PsiphonRegionsData{Regions: []eventbus.PsiphonRegion{
			{Code: "DE", Name: "Germany"},
			{Code: "JP", Name: "Japan"},
		}},
	})

	if !strings.HasSuffix(subject, " New Psiphon region available: Germany (DE), Japan (JP)") {
		t.Errorf("subject = %q, want the regions named", subject)
	}
	for _, want := range []string{"<b>Regions:</b> Germany (DE), Japan (JP)", "NEW"} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %q, want it to contain %q", body, want)
		}
	}
}

// The subject is plain text and the body is HTML, so only the body escapes the name.
func TestTheNameOfAPsiphonRegionIsEscapedInTheBodyOfTheMail(t *testing.T) {
	psiphonRegionsMail(t)

	subject, body := (&Subscriber{}).formatMessage(eventbus.Event{
		Type: eventbus.EventPsiphonRegionsNew,
		Data: &eventbus.PsiphonRegionsData{Regions: []eventbus.PsiphonRegion{{Code: "XX", Name: "<i>Land</i>"}}},
	})

	if !strings.Contains(subject, "<i>Land</i> (XX)") {
		t.Errorf("subject = %q, want the name as it is", subject)
	}
	if strings.Contains(body, "<i>Land</i>") || !strings.Contains(body, "&lt;i&gt;Land&lt;/i&gt; (XX)") {
		t.Errorf("body = %q, want the name escaped", body)
	}
}

func TestAMailOfNoPsiphonRegionIsNotSent(t *testing.T) {
	psiphonRegionsMail(t)

	if subject, _ := (&Subscriber{}).formatMessage(eventbus.Event{Type: eventbus.EventPsiphonRegionsNew, Data: &eventbus.PsiphonRegionsData{}}); subject != "" {
		t.Errorf("subject of no region = %q, want none", subject)
	}
	if subject, _ := (&Subscriber{}).formatMessage(eventbus.Event{Type: eventbus.EventPsiphonRegionsNew}); subject != "" {
		t.Errorf("subject of an event with no data = %q, want none", subject)
	}
}
