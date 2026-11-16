package tgbot

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"

	"github.com/nicksnyder/go-i18n/v2/i18n"
)

func psiphonRegionsEvent(regions ...eventbus.PsiphonRegion) eventbus.Event {
	return eventbus.Event{
		Type:   eventbus.EventPsiphonRegionsNew,
		Source: "psiphon",
		Data:   &eventbus.PsiphonRegionsData{Regions: regions},
	}
}

func TestANewPsiphonRegionIsNamedInTheAlert(t *testing.T) {
	draftLocalizer(t, &i18n.Message{ID: "tgbot.messages.eventPsiphonRegionsNew", Other: "New Psiphon region available: {{ .Regions }}"})

	got := (&Tgbot{}).formatEventMessage(psiphonRegionsEvent(
		eventbus.PsiphonRegion{Code: "DE", Name: "Germany"},
		eventbus.PsiphonRegion{Code: "JP", Name: "Japan"},
	))

	if !strings.Contains(got, "🌍 New Psiphon region available: Germany (DE), Japan (JP)") {
		t.Fatalf("alert = %q, want the regions named, with their codes", got)
	}
}

// The alert is sent with the HTML parse mode, so a name is escaped as the other texts of the bot are.
func TestTheNameOfAPsiphonRegionIsEscapedInTheAlert(t *testing.T) {
	draftLocalizer(t, &i18n.Message{ID: "tgbot.messages.eventPsiphonRegionsNew", Other: "New Psiphon region available: {{ .Regions }}"})

	got := (&Tgbot{}).formatEventMessage(psiphonRegionsEvent(eventbus.PsiphonRegion{Code: "XX", Name: "<b>Land</b>"}))

	if strings.Contains(got, "<b>Land</b>") || !strings.Contains(got, "&lt;b&gt;Land&lt;/b&gt; (XX)") {
		t.Fatalf("alert = %q, want the name escaped", got)
	}
}

func TestAnAlertOfNoPsiphonRegionIsNotSent(t *testing.T) {
	draftLocalizer(t, &i18n.Message{ID: "tgbot.messages.eventPsiphonRegionsNew", Other: "New Psiphon region available: {{ .Regions }}"})

	if got := (&Tgbot{}).formatEventMessage(psiphonRegionsEvent()); got != "" {
		t.Errorf("alert of no region = %q, want none", got)
	}
	if got := (&Tgbot{}).formatEventMessage(eventbus.Event{Type: eventbus.EventPsiphonRegionsNew}); got != "" {
		t.Errorf("alert of an event with no data = %q, want none", got)
	}
}
