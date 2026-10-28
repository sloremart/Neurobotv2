package session

import (
	"testing"
	"time"
)

func TestSession_GetContext_NilMap(t *testing.T) {
	s := &Session{Context: nil}
	val := s.GetContext("any_key")
	if val != "" {
		t.Errorf("expected empty string for nil context, got %q", val)
	}
}

func TestSession_GetContext_ExistingKey(t *testing.T) {
	s := &Session{Context: map[string]string{"foo": "bar"}}
	if val := s.GetContext("foo"); val != "bar" {
		t.Errorf("expected 'bar', got %q", val)
	}
}

func TestSession_GetContext_MissingKey(t *testing.T) {
	s := &Session{Context: map[string]string{"foo": "bar"}}
	if val := s.GetContext("missing"); val != "" {
		t.Errorf("expected empty string for missing key, got %q", val)
	}
}

func TestSession_SetContext_NilMap(t *testing.T) {
	s := &Session{Context: nil}
	s.SetContext("key1", "value1")
	if s.Context == nil {
		t.Fatal("expected context to be initialized")
	}
	if s.Context["key1"] != "value1" {
		t.Errorf("expected 'value1', got %q", s.Context["key1"])
	}
}

func TestSession_SetContext_ExistingMap(t *testing.T) {
	s := &Session{Context: map[string]string{"existing": "val"}}
	s.SetContext("new", "value")
	if s.Context["existing"] != "val" {
		t.Error("expected existing key preserved")
	}
	if s.Context["new"] != "value" {
		t.Errorf("expected 'value', got %q", s.Context["new"])
	}
}

func TestEscalationExempt_QueuedUntilNextOpen(t *testing.T) {
	escalated := time.Date(2026, 9, 11, 22, 0, 0, 0, time.UTC)
	open := escalated.Add(10 * time.Hour)
	nextOpen := func(time.Time) time.Time { return open }
	queued := EscalatedSession{QueuedForNextDay: true, EscalatedAt: escalated}

	cases := []struct {
		name     string
		s        EscalatedSession
		now      time.Time
		nextOpen func(time.Time) time.Time
		want     bool
	}{
		{"encolada antes de abrir", queued, open.Add(-time.Minute), nextOpen, true},
		{"encolada al abrir ya no exime", queued, open, nextOpen, false},
		{"encolada tras abrir", queued, open.Add(time.Hour), nextOpen, false},
		{"sin marca nunca exime", EscalatedSession{EscalatedAt: escalated}, open.Add(-time.Minute), nextOpen, false},
		{"sin horario cableado exime sin caducar", queued, open.Add(48 * time.Hour), nil, true},
		{"sin escalated_at exime sin caducar", EscalatedSession{QueuedForNextDay: true}, open.Add(48 * time.Hour), nextOpen, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := escalationExempt(c.s, c.now, c.nextOpen); got != c.want {
				t.Errorf("escalationExempt = %v, want %v", got, c.want)
			}
		})
	}
}
