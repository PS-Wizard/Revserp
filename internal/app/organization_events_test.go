package app

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func mustUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := parseUUIDParam(value)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", value, err)
	}
	return id
}

func TestParseOrganizationEventCursor(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		lastEvent string
		want      int64
		wantHas   bool
		wantErr   bool
	}{
		{name: "absent", target: "/organizations/x/events", want: 0, wantHas: false},
		{name: "after value", target: "/organizations/x/events?after=5", want: 5, wantHas: true},
		{name: "after zero", target: "/organizations/x/events?after=0", want: 0, wantHas: true},
		{name: "after empty", target: "/organizations/x/events?after=", wantErr: true},
		{name: "after negative", target: "/organizations/x/events?after=-1", wantErr: true},
		{name: "after invalid", target: "/organizations/x/events?after=abc", wantErr: true},
		{name: "header fallback", target: "/organizations/x/events", lastEvent: "7", want: 7, wantHas: true},
		{name: "header invalid", target: "/organizations/x/events", lastEvent: "nope", wantErr: true},
		{name: "after wins over header", target: "/organizations/x/events?after=3", lastEvent: "9", want: 3, wantHas: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tt.target, nil)
			if tt.lastEvent != "" {
				r.Header.Set("Last-Event-ID", tt.lastEvent)
			}
			cursor, has, err := parseOrganizationEventCursor(r)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got cursor=%d has=%v", cursor, has)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cursor != tt.want || has != tt.wantHas {
				t.Fatalf("got (cursor=%d, has=%v), want (%d, %v)", cursor, has, tt.want, tt.wantHas)
			}
		})
	}
}

func TestOrganizationEventReadyFrameUsesStringCursor(t *testing.T) {
	if got := organizationEventReadyFrame(0); got != "event: ready\ndata: {\"cursor\":\"0\"}\n\n" {
		t.Fatalf("ready frame = %q", got)
	}
	// Above JS Number.MAX_SAFE_INTEGER, must stay a quoted string.
	const big int64 = 9007199254740993
	want := "event: ready\ndata: {\"cursor\":\"9007199254740993\"}\n\n"
	if got := organizationEventReadyFrame(big); got != want {
		t.Fatalf("ready frame = %q, want %q", got, want)
	}
}

func TestOrganizationEventHubRoutesByOrganization(t *testing.T) {
	hub := newOrganizationEventHub()
	orgA := mustUUID(t, "11111111-1111-1111-1111-111111111111")
	orgB := mustUUID(t, "22222222-2222-2222-2222-222222222222")

	chA, unsubA := hub.subscribe(orgA)
	defer unsubA()
	chB, unsubB := hub.subscribe(orgB)
	defer unsubB()

	hub.wake(orgA)
	if !receives(chA) {
		t.Fatal("org A subscriber did not receive its wake")
	}
	if receives(chB) {
		t.Fatal("org B subscriber received org A's wake")
	}

	hub.wakeAll()
	if !receives(chA) || !receives(chB) {
		t.Fatal("wakeAll did not reach both subscribers")
	}

	unsubA()
	hub.wake(orgA)
	if receives(chA) {
		t.Fatal("unsubscribed channel received a wake")
	}
}

func TestOrganizationEventHubSlowSubscriberDoesNotBlock(t *testing.T) {
	hub := newOrganizationEventHub()
	org := mustUUID(t, "33333333-3333-3333-3333-333333333333")
	ch, unsub := hub.subscribe(org)
	defer unsub()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			hub.wake(org)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("wake blocked on a full subscriber channel")
	}
	if !receives(ch) {
		t.Fatal("expected at least one buffered wake")
	}
}

func receives(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(100 * time.Millisecond):
		return false
	}
}
