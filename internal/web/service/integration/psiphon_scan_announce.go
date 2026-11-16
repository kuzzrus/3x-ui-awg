package integration

import (
	"slices"

	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
)

// EventBus is where the weekly scan tells of the regions it found; nil when the panel runs without one.
var EventBus *eventbus.Bus

// announceNewRegions tells the notifiers of the regions that a scan connected for the first time.
func announceNewRegions(codes []string) {
	if len(codes) == 0 || EventBus == nil {
		return
	}
	regions := make([]eventbus.PsiphonRegion, len(codes))
	for i, code := range codes {
		regions[i] = eventbus.PsiphonRegion{Code: code, Name: regionName(code)}
	}
	EventBus.Publish(eventbus.Event{
		Type:   eventbus.EventPsiphonRegionsNew,
		Source: "psiphon",
		Data:   &eventbus.PsiphonRegionsData{Regions: regions},
	})
}

// regionName is the name of a region of the picker, or its code for one that the picker does not list.
func regionName(code string) string {
	if i := slices.IndexFunc(isoCountries, func(r Region) bool { return r.Code == code }); i >= 0 {
		return isoCountries[i].Name
	}
	return code
}
