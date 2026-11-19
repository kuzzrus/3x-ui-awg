package job

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgnet"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type routedTaker struct {
	out   []amneziawgnet.RoutedTraffic
	calls int
}

func (t *routedTaker) TakeRoutedTraffic() []amneziawgnet.RoutedTraffic {
	t.calls++
	return t.out
}

type chargeRecorder struct {
	got   []*xray.ClientTraffic
	calls int
}

func (r *chargeRecorder) AddTraffic(_ []*xray.Traffic, clients []*xray.ClientTraffic) (bool, bool, error) {
	r.calls++
	r.got = clients
	return false, false, nil
}

func routed(email string, up, down int64) amneziawgnet.RoutedTraffic {
	return amneziawgnet.RoutedTraffic{Email: email, Up: up, Down: down}
}

func TestRoutedTrafficIsChargedToTheClientsItWentBetween(t *testing.T) {
	taker := &routedTaker{out: []amneziawgnet.RoutedTraffic{routed("a@x", 100, 5), routed("b@x", 0, 100)}}
	rec := &chargeRecorder{}

	collectRoutedTrafficWith(rec, taker)

	if rec.calls != 1 || len(rec.got) != 2 {
		t.Fatalf("recorder called %d time(s) with %d traffic(s), want once with 2", rec.calls, len(rec.got))
	}
	a, b := rec.got[0], rec.got[1]
	if a.Email != "a@x" || a.Up != 100 || a.Down != 5 {
		t.Errorf("a's charge = %+v, want a@x, up 100, down 5", a)
	}
	if b.Email != "b@x" || b.Up != 0 || b.Down != 100 {
		t.Errorf("b's charge = %+v, want b@x, up 0, down 100", b)
	}
}

// AddTraffic keeps one entry per email, so a client that shows twice (on two inbounds, or on an interface that was rebuilt)
// has to be summed first.
func TestRoutedTrafficOfOneClientIsChargedOnce(t *testing.T) {
	taker := &routedTaker{out: []amneziawgnet.RoutedTraffic{routed("a@x", 100, 5), routed("b@x", 1, 2), routed("a@x", 40, 3)}}
	rec := &chargeRecorder{}

	collectRoutedTrafficWith(rec, taker)

	if len(rec.got) != 2 {
		t.Fatalf("charged %d entries, want one per client (2): %+v", len(rec.got), rec.got)
	}
	a, b := rec.got[0], rec.got[1]
	if a.Email != "a@x" || a.Up != 140 || a.Down != 8 {
		t.Errorf("a's charge = %+v, want up 140 and down 8, the sum of both entries", a)
	}
	if b.Email != "b@x" || b.Up != 1 || b.Down != 2 {
		t.Errorf("b's charge = %+v, want up 1 and down 2", b)
	}
}

// The panel stops the embedded interfaces before it flushes, and what they routed in the last seconds must still be charged.
func TestShutdownFlushChargesWhatWasRoutedBetweenClients(t *testing.T) {
	rec := &chargeRecorder{}
	j := &AmneziaWGJob{routed: &routedTaker{out: []amneziawgnet.RoutedTraffic{routed("a@x", 10, 20)}}, recorder: rec}

	j.FlushKernelTraffic()

	if rec.calls != 1 || len(rec.got) != 1 || rec.got[0].Email != "a@x" || rec.got[0].Up != 10 || rec.got[0].Down != 20 {
		t.Errorf("the flush charged %d time(s): %+v, want a@x up 10 down 20 once", rec.calls, rec.got)
	}
}

func TestNothingRoutedChargesNothing(t *testing.T) {
	rec := &chargeRecorder{}
	collectRoutedTrafficWith(rec, &routedTaker{})
	if rec.calls != 0 {
		t.Errorf("the recorder was called %d time(s) with nothing routed: a panel that routes nothing must not touch the database", rec.calls)
	}
}

// What clients send each other counts against their limits, so that a client over its limit cannot go on using the server
// through another client.
func TestRoutedTrafficCountsAgainstTheLimitOfAClient(t *testing.T) {
	setupIntegrationDB(t)
	db := database.GetDB()
	inbound := &model.Inbound{UserId: 1, Tag: "awg-routed", Enable: true, Port: 45301, Protocol: model.AmneziaWG, Settings: `{"clients":[]}`}
	if err := db.Create(inbound).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	const email = "limited@example.test"
	if err := db.Create(&xray.ClientTraffic{InboundId: inbound.Id, Email: email, Enable: true, Total: 1000}).Error; err != nil {
		t.Fatalf("create client traffic: %v", err)
	}

	collectRoutedTrafficWith(&service.InboundService{}, &routedTaker{out: []amneziawgnet.RoutedTraffic{routed(email, 600, 600)}})

	var got xray.ClientTraffic
	if err := db.Where("email = ?", email).First(&got).Error; err != nil {
		t.Fatalf("reload client traffic: %v", err)
	}
	if got.Up != 600 || got.Down != 600 {
		t.Errorf("usage = up %d down %d, want 600/600", got.Up, got.Down)
	}
	if got.Enable {
		t.Error("the client is still enabled after the routed traffic took it past its limit of 1000")
	}
}
