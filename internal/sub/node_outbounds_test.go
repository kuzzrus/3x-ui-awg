package sub

import (
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// attachClient links the client with this email to more inbounds, as the panel does for a client on several of them.
func attachClient(t *testing.T, email string, inbounds ...*model.Inbound) model.ClientRecord {
	t.Helper()
	var record model.ClientRecord
	if err := database.GetDB().Where("email = ?", email).First(&record).Error; err != nil {
		t.Fatalf("client %s: %v", email, err)
	}
	for _, inbound := range inbounds {
		if err := database.GetDB().Create(&model.ClientInbound{ClientId: record.Id, InboundId: inbound.Id}).Error; err != nil {
			t.Fatalf("attach %s to %s: %v", email, inbound.Tag, err)
		}
	}
	return record
}

type nodeOutboundKey struct{ node, inbound int }

func keysOf(outbounds []service.NodeOutbound) []nodeOutboundKey {
	keys := make([]nodeOutboundKey, len(outbounds))
	for i, item := range outbounds {
		keys[i] = nodeOutboundKey{item.NodeID, item.InboundID}
	}
	return keys
}

// The panel dials a node as one of its clients: every enabled inbound the client has on the nodes gives an
// outbound, in the order of the node and the inbound, and the inbounds elsewhere give none.
func TestNodeOutboundsAreThoseOfTheInboundsTheClientHasOnTheNodes(t *testing.T) {
	seedSubDB(t)
	berlin := seedSubNode(t, "berlin")
	paris := seedSubNode(t, "paris")
	rome := seedSubNode(t, "rome")
	first := seedSubInbound(t, "relay", "relay", 4901, 1, wsTLSStream)
	second := seedSubInbound(t, "other", "second", 4902, 2, wsTLSStream)
	third := seedSubInbound(t, "other", "third", 4903, 3, wsTLSStream)
	panelOwn := seedSubInbound(t, "other", "own", 4904, 4, wsTLSStream)
	onRome := seedSubInbound(t, "other", "rome-in", 4905, 5, wsTLSStream)
	putOnNode(t, first, berlin)
	putOnNode(t, second, berlin)
	putOnNode(t, third, paris)
	putOnNode(t, onRome, rome)
	record := attachClient(t, "relay@e", second, third, panelOwn, onRome)

	got, err := NodeOutbounds("  RELAY@e ", []int{paris.Id, berlin.Id})
	if err != nil {
		t.Fatalf("NodeOutbounds: %v", err)
	}
	want := []nodeOutboundKey{{berlin.Id, first.Id}, {berlin.Id, second.Id}, {paris.Id, third.Id}}
	if !slices.Equal(keysOf(got), want) {
		t.Fatalf("outbounds = %v, want %v (rome is not asked for, the panel's own inbound is no node's)", keysOf(got), want)
	}
	for i, item := range got {
		if item.ClientID != record.Id {
			t.Errorf("outbound %d is for client %d, want %d", i, item.ClientID, record.Id)
		}
		settings, _ := item.Outbound["settings"].(map[string]any)
		if item.Outbound["protocol"] != "vless" || settings["id"] != record.UUID || settings["address"] == "" || settings["encryption"] != "none" {
			t.Errorf("outbound %d = %v, want a vless outbound that logs in as the client", i, item.Outbound)
		}
		stream, _ := item.Outbound["streamSettings"].(map[string]any)
		if stream["security"] != "tls" || stream["network"] != "ws" {
			t.Errorf("outbound %d keeps the transport of its inbound, got %v", i, stream)
		}
	}
	if port := got[1].Outbound["settings"].(map[string]any)["port"]; port != float64(4902) {
		t.Errorf("the second outbound goes to port %v, want 4902", port)
	}
}

func TestNodeOutboundsLeaveOutWhatAClientCannotUse(t *testing.T) {
	seedSubDB(t)
	berlin := seedSubNode(t, "berlin")
	live := seedSubInbound(t, "relay", "relay", 4911, 1, wsTLSStream)
	off := seedSubInbound(t, "other", "off", 4912, 2, wsTLSStream)
	odd := seedSubInbound(t, "other", "odd", 4913, 3, wsTLSStream)
	putOnNode(t, live, berlin)
	putOnNode(t, off, berlin)
	putOnNode(t, odd, berlin)
	record := attachClient(t, "relay@e", off, odd)
	db := database.GetDB()
	if err := db.Model(&model.Inbound{}).Where("id = ?", off.Id).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Inbound{}).Where("id = ?", odd.Id).Update("protocol", "mtproto").Error; err != nil {
		t.Fatal(err)
	}

	got, err := NodeOutbounds("relay@e", []int{berlin.Id})
	if err != nil {
		t.Fatal(err)
	}
	if want := []nodeOutboundKey{{berlin.Id, live.Id}}; !slices.Equal(keysOf(got), want) {
		t.Fatalf("outbounds = %v, want %v: an inbound that is off, and one with no outbound form, give none", keysOf(got), want)
	}

	for name, call := range map[string]func() ([]service.NodeOutbound, error){
		"a client nobody has":     func() ([]service.NodeOutbound, error) { return NodeOutbounds("nobody@e", []int{berlin.Id}) },
		"no node":                 func() ([]service.NodeOutbound, error) { return NodeOutbounds("relay@e", nil) },
		"a node with no inbounds": func() ([]service.NodeOutbound, error) { return NodeOutbounds("relay@e", []int{berlin.Id + 100}) },
		"no email":                func() ([]service.NodeOutbound, error) { return NodeOutbounds(" ", []int{berlin.Id}) },
	} {
		if out, err := call(); err != nil || len(out) != 0 {
			t.Errorf("%s: got %v, %v, want nothing", name, out, err)
		}
	}

	if err := db.Model(&model.ClientRecord{}).Where("id = ?", record.Id).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	if out, err := NodeOutbounds("relay@e", []int{berlin.Id}); err != nil || len(out) != 0 {
		t.Fatalf("a client that is off still gave %v, %v", out, err)
	}
}
