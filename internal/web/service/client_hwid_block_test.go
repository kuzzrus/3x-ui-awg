package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestClientHwidBanIndependentOfLimit(t *testing.T) {
	for _, limit := range []int{0, 1} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			initClientHwidTestDB(t)
			rec := seedHwidClient(t, limit)
			svc := &ClientService{}
			const hwid = "device-banned"
			if r, err := svc.EnforceHwidForSubID(rec.SubID, HwidRequest{Hwid: hwid}); err != nil || !r.Allowed {
				t.Fatalf("register: %+v, %v", r, err)
			}
			list, err := svc.ListClientHwids(rec.Email)
			if err != nil || len(list) != 1 {
				t.Fatalf("list: %+v, %v", list, err)
			}
			id := list[0].Id
			if err := svc.SetClientHwidBlocked(rec.Email, id, true); err != nil {
				t.Fatal(err)
			}
			assertBanned := func() {
				t.Helper()
				r, err := svc.EnforceHwidForSubID(rec.SubID, HwidRequest{Hwid: " " + hwid + " "})
				if err != nil || r.Allowed || !r.Blocked || r.MaxDevicesReached || r.NotSupported {
					t.Fatalf("banned device: %+v, %v", r, err)
				}
			}
			assertBanned()
			// A ban must survive both automatic trimming and manual list clearing.
			for _, newLimit := range []int{2, 1, 0} {
				if err := svc.setClientLimitHwidByEmail(rec.Email, newLimit); err != nil {
					t.Fatal(err)
				}
				assertBanned()
			}
			if err := svc.DeleteClientHwid(rec.Email, id); err == nil || err.Error() != "device not found or blocked; unblock it before removing" {
				t.Fatalf("deleting a banned row: err = %v, want the unblock-first refusal", err)
			}
			if err := svc.ClearClientHwids(rec.Email); err != nil {
				t.Fatal(err)
			}
			assertBanned()
			list, err = svc.ListClientHwids(rec.Email)
			if err != nil || len(list) != 1 || !list[0].Blocked {
				t.Fatalf("ban missing from panel: %+v, %v", list, err)
			}
			if err := svc.SetClientHwidBlocked(rec.Email, id, false); err != nil {
				t.Fatal(err)
			}
			if r, err := svc.EnforceHwidForSubID(rec.SubID, HwidRequest{Hwid: hwid}); err != nil || !r.Allowed || r.Blocked {
				t.Fatalf("unblocked: %+v, %v", r, err)
			}
		})
	}
}

func TestClientHwidBanScopeAndSlots(t *testing.T) {
	initClientHwidTestDB(t)
	rec := seedHwidClient(t, 1)
	svc := &ClientService{}
	if _, err := svc.EnforceHwidForSubID(rec.SubID, HwidRequest{Hwid: "shared-device"}); err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListClientHwids(rec.Email)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v, %v", list, err)
	}
	other := &model.ClientRecord{Email: "other-ban@example.com", SubID: "other-ban-sub", Enable: true}
	if err := database.GetDB().Create(other).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.SetClientHwidBlocked(other.Email, list[0].Id, true); err == nil || err.Error() != "device not found" {
		t.Fatalf("banning a foreign subscription's device: err = %v, want \"device not found\"", err)
	}
	if err := svc.SetClientHwidBlocked(rec.Email, list[0].Id, true); err != nil {
		t.Fatal(err)
	}
	status, found, err := svc.HwidSlotStatusForSubID(rec.SubID)
	if err != nil || !found || status.Registered != 0 || status.Remaining != 1 || status.Full {
		t.Fatalf("ban must free a slot: %+v, %v", status, err)
	}
	if r, err := svc.EnforceHwidForSubID(rec.SubID, HwidRequest{Hwid: "another-device"}); err != nil || !r.Allowed {
		t.Fatalf("another device should take freed slot: %+v, %v", r, err)
	}
	if r, err := svc.EnforceHwidForSubID(other.SubID, HwidRequest{Hwid: "shared-device"}); err != nil || !r.Allowed {
		t.Fatalf("same HWID in other subscription: %+v, %v", r, err)
	}
	if err := svc.SetClientHwidBlocked(other.Email, list[0].Id, false); err == nil || err.Error() != "device not found" {
		t.Fatalf("unblocking a foreign subscription's device: err = %v, want \"device not found\"", err)
	}
}

// Unblocking hands a banned device its slot back, so it must not push the subscription over its limit.
func TestClientHwidUnblockNeedsAFreeSlot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
	}{{"limited", 1}, {"unlimited", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			initClientHwidTestDB(t)
			rec := seedHwidClient(t, tc.limit)
			svc := &ClientService{}
			register := func(hwid, label string) {
				t.Helper()
				if r, err := svc.EnforceHwidForSubID(rec.SubID, HwidRequest{Hwid: hwid, UserAgent: label}); err != nil || !r.Allowed {
					t.Fatalf("register %s: %+v, %v", label, r, err)
				}
			}
			idOf := func(label string) int {
				t.Helper()
				list, err := svc.ListClientHwids(rec.Email)
				if err != nil {
					t.Fatal(err)
				}
				for _, d := range list {
					if d.UserAgent == label {
						return d.Id
					}
				}
				t.Fatalf("no device %q in %+v", label, list)
				return 0
			}

			register("device-aaaa", "A")
			a := idOf("A")
			if err := svc.SetClientHwidBlocked(rec.Email, a, true); err != nil {
				t.Fatal(err)
			}
			register("device-bbbb", "B") // takes the slot the ban freed
			err := svc.SetClientHwidBlocked(rec.Email, a, false)
			if tc.limit == 0 {
				if err != nil {
					t.Fatalf("unblocking on an unlimited subscription: %v", err)
				}
				return
			}
			if !errors.Is(err, errHwidNoFreeSlot) {
				t.Fatalf("unblocking over the limit: err = %v, want errHwidNoFreeSlot", err)
			}
			if r, err := svc.EnforceHwidForSubID(rec.SubID, HwidRequest{Hwid: "device-aaaa"}); err != nil || !r.Blocked {
				t.Fatalf("the refused unblock must leave the ban in place: %+v, %v", r, err)
			}
			if err := svc.SetClientHwidBlocked(rec.Email, idOf("B"), false); err != nil {
				t.Fatalf("unblocking a device that is not blocked changes nothing, got: %v", err)
			}
			if err := svc.DeleteClientHwid(rec.Email, idOf("B")); err != nil {
				t.Fatal(err)
			}
			if err := svc.SetClientHwidBlocked(rec.Email, a, false); err != nil {
				t.Fatalf("unblocking with a free slot: %v", err)
			}
			if r, err := svc.EnforceHwidForSubID(rec.SubID, HwidRequest{Hwid: "device-aaaa"}); err != nil || !r.Allowed || r.Blocked {
				t.Fatalf("the unblocked device: %+v, %v", r, err)
			}
		})
	}
}
