package naiveproxy

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestInstanceFromInboundParsesSettings(t *testing.T) {
	ib := &model.Inbound{
		Id:       7,
		Listen:   "0.0.0.0",
		Port:     18443,
		Protocol: model.NaiveProxy,
		Settings: `{"domain":"naive1.example.com","certFile":"/etc/x.crt","keyFile":"/etc/x.key",` +
			`"routeThroughXray":true,"routeXrayPort":50000,` +
			`"clients":[` +
			`{"email":"alice","naiveProxyPassword":"pw-a","enable":true},` +
			`{"email":"bob","naiveProxyPassword":"","enable":true},` +
			`{"email":"","naiveProxyPassword":"pw-c","enable":true},` +
			`{"email":"dave","naiveProxyPassword":"pw-d","enable":false}]}`,
	}
	inst, ok := InstanceFromInbound(ib)
	if !ok {
		t.Fatal("InstanceFromInbound: ok = false")
	}
	if inst.Id != 7 {
		t.Errorf("Id = %d, want 7", inst.Id)
	}
	// Always 127.0.0.1 regardless of ib.Listen -- renderCaddyfile rejects any other host.
	if inst.ListenAddr != "127.0.0.1:18443" {
		t.Errorf("ListenAddr = %q, want 127.0.0.1:18443", inst.ListenAddr)
	}
	if inst.Domain != "naive1.example.com" {
		t.Errorf("Domain = %q, want naive1.example.com", inst.Domain)
	}
	if inst.CertFile != "/etc/x.crt" || inst.KeyFile != "/etc/x.key" {
		t.Errorf("CertFile/KeyFile = %q/%q, want /etc/x.crt//etc/x.key", inst.CertFile, inst.KeyFile)
	}
	if !inst.RouteThroughXray || inst.XrayRoutePort != 50000 {
		t.Errorf("RouteThroughXray/XrayRoutePort = %v/%d, want true/50000", inst.RouteThroughXray, inst.XrayRoutePort)
	}
	if len(inst.Clients) != 1 || inst.Clients[0].Email != "alice" || inst.Clients[0].Username != "alice" || inst.Clients[0].Password != "pw-a" {
		t.Errorf("Clients = %+v, want exactly alice/alice/pw-a (bob has no password, blank email, dave disabled)", inst.Clients)
	}
}

func TestInstanceFromInboundRejectsWrongProtocol(t *testing.T) {
	ib := &model.Inbound{Protocol: model.VLESS, Settings: `{}`}
	if _, ok := InstanceFromInbound(ib); ok {
		t.Error("InstanceFromInbound accepted a non-NaiveProxy inbound")
	}
}

func TestInstanceFromInboundRejectsMalformedSettings(t *testing.T) {
	ib := &model.Inbound{Protocol: model.NaiveProxy, Settings: `not json`}
	if _, ok := InstanceFromInbound(ib); ok {
		t.Error("InstanceFromInbound accepted malformed Settings JSON")
	}
}

func TestInstanceFromInboundOkWithNoEnabledClients(t *testing.T) {
	ib := &model.Inbound{Protocol: model.NaiveProxy, Port: 1, Settings: `{"domain":"d.example.com"}`}
	inst, ok := InstanceFromInbound(ib)
	if !ok {
		t.Fatal("InstanceFromInbound: ok = false for a client-less inbound, want true (Ensure decides whether to run)")
	}
	if len(inst.Clients) != 0 {
		t.Errorf("Clients = %+v, want empty", inst.Clients)
	}
}
