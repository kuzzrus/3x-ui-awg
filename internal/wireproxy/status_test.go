package wireproxy

import (
	"strings"
	"testing"
)

// Structurally real: shaped exactly like the output of warpwp v1.3.9 --status-json, captured from the script run
// against a live install (the values are made up).
const sampleStatusJSON = `{
  "manager_version": "1.3.9",
  "native_version": "v1.2.6",
  "healthy": true,
  "scheduler": "systemd_timer",
  "installed": true,
  "manager_installed": true,
  "native_installed": true,
  "service": {"name": "wireproxy", "state": "active", "active": true},
  "socks5": {"host": "127.0.0.1", "port": 40000, "listening": true},
  "warp": {"endpoint": "162.159.192.1:2408", "endpoint_port": "2408", "ip": "104.28.203.9", "colo": "HEL", "loc": "FI", "status": "on", "on": true},
  "selection": {"time_total": "0.093969", "colo": "HEL", "loc": "FI", "checked_at": 1735689600, "probe_loss_percent": "0", "stable": "1", "scanner": "native"},
  "routing_guard": {"danger": false},
  "cron": {"file": "/etc/cron.d/warp-wireproxy-check", "installed": false, "active": false, "daemon": "cron", "uses_flock": false, "lock_file": "/var/lock/warpwp-check.lock", "schedule": ""},
  "timer": {"service_file": "/etc/systemd/system/warp-wireproxy-check.service", "timer_file": "/etc/systemd/system/warp-wireproxy-check.timer", "installed": true, "enabled": true, "active": true, "interval_minutes": 15, "log_file": "/var/log/warp-timer-check.log", "log_exists": true},
  "logs": {"cron_file": "/var/log/warp-check.log", "cron_exists": true, "timer_file": "/var/log/warp-timer-check.log", "timer_exists": true},
  "cache": {"good_file": "/etc/wireguard/warp-endpoints.good", "bad_file": "/etc/wireguard/warp-endpoints.bad"}
}`

func TestParseStatusRealShape(t *testing.T) {
	status, err := parseStatus([]byte(sampleStatusJSON))
	if err != nil {
		t.Fatalf("parseStatus: %v", err)
	}
	if !status.Healthy {
		t.Error("Healthy = false, want true")
	}
	if status.Scheduler != "systemd_timer" {
		t.Errorf("Scheduler = %q, want %q", status.Scheduler, "systemd_timer")
	}
	if !status.Service.Active {
		t.Error("Service.Active = false, want true")
	}
	if status.Socks5.Port != 40000 {
		t.Errorf("Socks5.Port = %d, want 40000", status.Socks5.Port)
	}
	if !status.Warp.On || status.Warp.IP != "104.28.203.9" || status.Warp.Colo != "HEL" || status.Warp.Loc != "FI" {
		t.Errorf("Warp = %+v, want on with ip=104.28.203.9 colo=HEL loc=FI", status.Warp)
	}
	if status.RoutingGuard.Danger {
		t.Error("RoutingGuard.Danger = true, want false for this sample")
	}
}

func TestParseStatusRoutingDanger(t *testing.T) {
	status, err := parseStatus([]byte(`{"routing_guard": {"danger": true}}`))
	if err != nil {
		t.Fatalf("parseStatus: %v", err)
	}
	if !status.RoutingGuard.Danger {
		t.Error("RoutingGuard.Danger = false, want true -- the UI's whole reason for surfacing this field")
	}
}

func TestParseStatusInvalidJSON(t *testing.T) {
	_, err := parseStatus([]byte("not json"))
	if err == nil {
		t.Fatal("parseStatus(not json) returned nil error")
	}
	if !strings.Contains(err.Error(), "status-json") {
		t.Errorf("parseStatus error = %q, want it to name --status-json for context", err.Error())
	}
}
