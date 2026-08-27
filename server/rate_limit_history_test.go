package server

import (
	"crs/config"
	"crs/database"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"testing"
	"time"
)

func seedCycle(t *testing.T, db *database.DB, at time.Time, total int64, remaining, limit int) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO APICallStats (recorded_at, pr_list, total, rate_limit_remaining, rate_limit_limit, rate_limit_reset_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		at.UTC().Format("2006-01-02 15:04:05"), total, total, remaining, limit, "",
	)
	if err != nil {
		t.Fatalf("seeding APICallStats: %v", err)
	}
}

func TestGetRateLimitHistoryDefaultsToThreeHours(t *testing.T) {
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db})
	now := time.Now().UTC().Truncate(time.Second)

	seedCycle(t, db, now.Add(-10*time.Minute), 30, 4700, 5000)
	seedCycle(t, db, now.Add(-20*time.Minute), 25, 4730, 5000)
	seedCycle(t, db, now.Add(-4*time.Hour), 99, 4900, 5000) // outside the default window

	h := &RPCHandler{}
	reply := &GetRateLimitHistoryReply{}
	if err := h.GetRateLimitHistory(&GetRateLimitHistoryArgs{}, reply); err != nil {
		t.Fatalf("GetRateLimitHistory: %v", err)
	}

	if reply.HoursBack != 3 {
		t.Errorf("HoursBack = %v, want the 3h default", reply.HoursBack)
	}
	if len(reply.Points) != 2 {
		t.Fatalf("expected 2 points inside the default window, got %d", len(reply.Points))
	}
	if reply.Points[0].Total != 25 || reply.Points[1].Total != 30 {
		t.Errorf("expected oldest-first points, got %d then %d", reply.Points[0].Total, reply.Points[1].Total)
	}
	if reply.Since == "" {
		t.Error("Since should report the start of the window")
	}
}

// The first point has no predecessor, so it reports no rate; later points
// spread their spend over the gap since the previous cycle.
func TestGetRateLimitHistoryComputesCallRate(t *testing.T) {
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db})
	now := time.Now().UTC().Truncate(time.Second)

	seedCycle(t, db, now.Add(-30*time.Minute), 40, 4800, 5000)
	seedCycle(t, db, now.Add(-20*time.Minute), 50, 4750, 5000)

	h := &RPCHandler{}
	reply := &GetRateLimitHistoryReply{}
	if err := h.GetRateLimitHistory(&GetRateLimitHistoryArgs{HoursBack: 1}, reply); err != nil {
		t.Fatalf("GetRateLimitHistory: %v", err)
	}
	if len(reply.Points) != 2 {
		t.Fatalf("expected 2 points, got %d", len(reply.Points))
	}
	if reply.Points[0].GapMinutes != 0 || reply.Points[0].CallsPerMinute != 0 {
		t.Errorf("first point should report no rate, got %+v", reply.Points[0])
	}
	if reply.Points[1].GapMinutes != 10 {
		t.Errorf("GapMinutes = %v, want 10", reply.Points[1].GapMinutes)
	}
	if reply.Points[1].CallsPerMinute != 5 {
		t.Errorf("CallsPerMinute = %v, want 50 calls over 10 minutes", reply.Points[1].CallsPerMinute)
	}
}

// Rows written before the rate limit columns existed carry -1, and a cycle
// whose budget lookup failed with nothing cached records 0. Neither is a real
// reading, so both come back as unknown rather than as an exhausted budget.
func TestGetRateLimitHistoryReportsMissingBudgetAsUnknown(t *testing.T) {
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db})
	now := time.Now().UTC().Truncate(time.Second)

	seedCycle(t, db, now.Add(-30*time.Minute), 10, -1, -1)
	seedCycle(t, db, now.Add(-20*time.Minute), 10, 0, 0)
	seedCycle(t, db, now.Add(-10*time.Minute), 10, 4900, 5000)

	h := &RPCHandler{}
	reply := &GetRateLimitHistoryReply{}
	if err := h.GetRateLimitHistory(&GetRateLimitHistoryArgs{HoursBack: 1}, reply); err != nil {
		t.Fatalf("GetRateLimitHistory: %v", err)
	}
	if len(reply.Points) != 3 {
		t.Fatalf("expected 3 points, got %d", len(reply.Points))
	}
	for i, p := range reply.Points[:2] {
		if p.Remaining != -1 || p.Limit != -1 {
			t.Errorf("point %d should be unknown, got remaining=%d limit=%d", i, p.Remaining, p.Limit)
		}
	}
	if reply.Points[2].Remaining != 4900 || reply.Points[2].Limit != 5000 {
		t.Errorf("a real reading should survive, got %+v", reply.Points[2])
	}
}

func TestGetRateLimitHistoryClampsAndDefaultsWindow(t *testing.T) {
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db})

	h := &RPCHandler{}
	for _, tc := range []struct {
		name  string
		hours float64
		want  float64
	}{
		{"negative falls back to the default", -5, 3},
		{"an explicit window is honoured", 12, 12},
		{"an absurd window is clamped", 1e9, maxRateLimitHistoryHours},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := &GetRateLimitHistoryReply{}
			if err := h.GetRateLimitHistory(&GetRateLimitHistoryArgs{HoursBack: tc.hours}, reply); err != nil {
				t.Fatalf("GetRateLimitHistory: %v", err)
			}
			if reply.HoursBack != tc.want {
				t.Errorf("HoursBack = %v, want %v", reply.HoursBack, tc.want)
			}
		})
	}
}

// An empty table is a normal state (a server that has not run a cycle yet), so
// it must come back as an empty series rather than an error or a null.
func TestGetRateLimitHistoryEmpty(t *testing.T) {
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db})

	h := &RPCHandler{}
	reply := &GetRateLimitHistoryReply{}
	if err := h.GetRateLimitHistory(&GetRateLimitHistoryArgs{}, reply); err != nil {
		t.Fatalf("GetRateLimitHistory: %v", err)
	}
	if reply.Points == nil {
		t.Error("Points should serialize as [], not null")
	}
	if len(reply.Points) != 0 {
		t.Errorf("expected no points, got %d", len(reply.Points))
	}
}

// The bun client reaches this over JSON-RPC by name, and net/rpc silently skips
// methods whose signature doesn't fit — a skipped method fails only at runtime,
// as "can't find method". Going over a real codec proves both that the method
// is registered and that the reply carries the snake_case keys the client's
// RateLimitHistoryReply interface reads.
func TestGetRateLimitHistoryOverJSONRPC(t *testing.T) {
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db})
	seedCycle(t, db, time.Now().UTC().Add(-10*time.Minute), 42, 4900, 5000)

	srv := rpc.NewServer()
	if err := srv.Register(&RPCHandler{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	serverConn, clientConn := net.Pipe()
	go srv.ServeCodec(jsonrpc.NewServerCodec(serverConn))
	defer clientConn.Close()

	client := jsonrpc.NewClient(clientConn)
	defer client.Close()

	// Decoded loosely so the assertion is about the wire keys themselves, not
	// about the Go struct being able to read back its own tags.
	var raw map[string]any
	if err := client.Call("RPCHandler.GetRateLimitHistory", &GetRateLimitHistoryArgs{}, &raw); err != nil {
		t.Fatalf("calling RPCHandler.GetRateLimitHistory over JSON-RPC: %v", err)
	}

	if raw["hours_back"] != 3.0 {
		t.Errorf("hours_back = %v, want 3", raw["hours_back"])
	}
	points, ok := raw["points"].([]any)
	if !ok || len(points) != 1 {
		t.Fatalf("points did not come back as a one-element array: %#v", raw["points"])
	}
	point, ok := points[0].(map[string]any)
	if !ok {
		t.Fatalf("point is not an object: %#v", points[0])
	}
	for _, key := range []string{
		"recorded_at", "total", "remaining", "limit", "reset_at",
		"gap_minutes", "calls_per_minute", "pr_list", "review_threads", "team_reviews",
	} {
		if _, present := point[key]; !present {
			t.Errorf("point is missing the %q key the client reads", key)
		}
	}
	if point["total"] != 42.0 || point["remaining"] != 4900.0 {
		t.Errorf("unexpected point values: %#v", point)
	}
}
