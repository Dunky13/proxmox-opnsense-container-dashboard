package dashboard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func dropHikyo(rows *[]Resource, ids ...string) {
	kept := []Resource{}
	for _, r := range *rows {
		drop := false
		for _, id := range ids {
			drop = drop || r.ID == id
		}
		if !drop {
			kept = append(kept, r)
		}
	}
	*rows = kept
}

// reservationOnly is a classification signal for the dashboard: an unowned
// container whose only provider record is one exact IP/MAC reservation.
func TestReservationOnlySignal(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*State, *[]Resource)
		want   bool
	}{
		{"complete importable configuration is not reservation only", func(*State, *[]Resource) {}, false},
		{"exact reservation with no domain or handle", func(_ *State, rows *[]Resource) {
			dropHikyo(rows, hikyoChild, hikyoHandle)
		}, true},
		{"exact reservation with a domain but no handle", func(_ *State, rows *[]Resource) {
			dropHikyo(rows, hikyoHandle)
		}, false},
		{"exact reservation with a domain and handle that cannot be imported", func(_ *State, rows *[]Resource) {
			changeHikyo(*rows, hikyoHandle, "ToDomain", "192.168.6.99")
		}, false},
		{"reservation matching only the IP", func(_ *State, rows *[]Resource) {
			dropHikyo(rows, hikyoChild, hikyoHandle)
			changeHikyo(*rows, hikyoReservation, "hw_address", "d0:cc:37:00:06:77")
		}, false},
		{"reservation matching only the MAC", func(_ *State, rows *[]Resource) {
			dropHikyo(rows, hikyoChild, hikyoHandle)
			changeHikyo(*rows, hikyoReservation, "ip_address", "192.168.6.77")
		}, false},
		{"a second colliding reservation", func(_ *State, rows *[]Resource) {
			dropHikyo(rows, hikyoChild, hikyoHandle)
			*rows = append(*rows, Resource{ID: "b1111111-1111-4111-8111-111111111609", Kind: "reservation", Fields: map[string]string{"subnet": hikyoSubnet, "ip_address": "192.168.6.9", "hw_address": "d0:cc:37:00:06:78", "hostname": "other"}})
		}, false},
		{"exact reservation in the wrong KEA subnet", func(_ *State, rows *[]Resource) {
			dropHikyo(rows, hikyoChild, hikyoHandle)
			changeHikyo(*rows, hikyoReservation, "subnet", "c2222222-2222-4222-8222-222222222222")
		}, false},
		{"no matching provider record at all", func(_ *State, rows *[]Resource) {
			dropHikyo(rows, hikyoReservation, hikyoChild, hikyoHandle)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, p, store := hikyoService(t)
			mutateHikyo(t, store, tc.mutate)
			item := hikyoItem(t, s)
			if item.ReservationOnly != tc.want {
				t.Fatalf("ReservationOnly = %v, want %v (existing %v, blockers %v)", item.ReservationOnly, tc.want, item.ExistingResources, item.ImportIssues)
			}
			raw, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(raw), `"reservationOnly":true`); got != tc.want {
				t.Fatalf("wire reservationOnly = %v, want %v: %s", got, tc.want, raw)
			}
			if tc.want {
				if item.Owned || item.ImportAvailable || item.State != "conflict" || len(item.ExistingResources) != 1 || item.ExistingResources[0].Kind != "reservation" {
					t.Fatalf("reservation-only item changed its other signals: %+v", item)
				}
				// Classification only: import stays refused and writes nothing.
				if _, err := s.Plan(context.Background(), PlanRequest{Action: "import", HostID: "main", VMID: 609}); err == nil {
					t.Fatal("import was planned for a reservation-only container")
				}
				if len(p.events) != 0 || len(store.Read().Plans) != 0 || len(store.Read().Owned) != 0 {
					t.Fatal("reservation-only classification wrote provider or ownership state", p.events)
				}
			}
		})
	}
}

func TestReservationOnlyIsNotSetForOwnedOrHealthyContainers(t *testing.T) {
	s, _, store := hikyoService(t)
	importHikyo(t, s)
	if item := hikyoItem(t, s); item.ReservationOnly {
		t.Fatal("owned container reported reservationOnly")
	}
	mutateHikyo(t, store, func(_ *State, rows *[]Resource) { dropHikyo(rows, hikyoChild, hikyoHandle) })
	if item := hikyoItem(t, s); item.ReservationOnly {
		t.Fatal("owned container with missing resources reported reservationOnly")
	}
}
