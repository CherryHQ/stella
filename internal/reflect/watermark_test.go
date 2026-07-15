package reflect

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/pluginstate"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestParseWatermarkSeq(t *testing.T) {
	maxSafeFloat := math.Nextafter(float64(math.MaxInt64), 0)
	tests := []struct {
		name    string
		raw     any
		want    int64
		wantOK  bool
		wantErr bool
	}{
		{name: "nil is absent", raw: nil, wantOK: false},
		{name: "empty string is absent", raw: "", wantOK: false},
		{name: "non-negative int", raw: int(42), want: 42, wantOK: true},
		{name: "non-negative int64", raw: int64(42), want: 42, wantOK: true},
		{name: "integer float64", raw: float64(42), want: 42, wantOK: true},
		{name: "safe maximum float64", raw: maxSafeFloat, want: int64(maxSafeFloat), wantOK: true},
		{name: "non-negative decimal string", raw: "42", want: 42, wantOK: true},
		{name: "explicit integer zero", raw: 0, want: 0, wantOK: true},
		{name: "explicit string zero", raw: "0", want: 0, wantOK: true},
		{name: "negative int", raw: int(-1), wantErr: true},
		{name: "negative int64", raw: int64(-1), wantErr: true},
		{name: "negative float64", raw: float64(-1), wantErr: true},
		{name: "fractional float64", raw: 42.5, wantErr: true},
		{name: "nan", raw: math.NaN(), wantErr: true},
		{name: "positive infinity", raw: math.Inf(1), wantErr: true},
		{name: "negative infinity", raw: math.Inf(-1), wantErr: true},
		// float64(math.MaxInt64) rounds above the largest representable int64.
		{name: "float64 above int64 range", raw: float64(math.MaxInt64), wantErr: true},
		{name: "negative decimal string", raw: "-1", wantErr: true},
		{name: "fractional decimal string", raw: "42.5", wantErr: true},
		{name: "invalid string", raw: "not-a-sequence", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotOK, err := parseWatermarkSeq(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseWatermarkSeq(%#v) error = %v, wantErr %t", tt.raw, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseWatermarkSeq(%#v) value = %d, want %d", tt.raw, got, tt.want)
			}
			if gotOK != tt.wantOK {
				t.Errorf("parseWatermarkSeq(%#v) ok = %t, want %t", tt.raw, gotOK, tt.wantOK)
			}
		})
	}
}

func TestParseWatermarkValueRejectsMalformedSequenceState(t *testing.T) {
	mark, err := parseWatermarkValue("session", "key", map[string]any{"reviewed_seq": 42.5})
	if err == nil {
		t.Fatal("parseWatermarkValue accepted malformed sequence state")
	}
	if mark != (reviewWatermark{}) {
		t.Fatalf("parseWatermarkValue returned partial watermark %#v", mark)
	}
}

func newTestWatermarkStore(t *testing.T) (*watermarkStore, context.Context) {
	t.Helper()
	db := dbtest.New(t)

	return newWatermarkStore(testStateStore{store: pluginstate.New(db)}), context.Background()
}

type testStateStore struct {
	store *pluginstate.Store
}

func (s testStateStore) Get(ctx context.Context, scope pkgplugins.StateScope, key string) (map[string]any, bool, error) {
	return s.store.Get(ctx, "reflect", scope, key)
}

func (s testStateStore) Set(ctx context.Context, scope pkgplugins.StateScope, key string, value map[string]any) error {
	return s.store.Set(ctx, "reflect", scope, key, value)
}

func (s testStateStore) Delete(ctx context.Context, scope pkgplugins.StateScope, key string) error {
	return s.store.Delete(ctx, "reflect", scope, key)
}

func TestWatermarkStore_GetMissing(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)
	ts, err := ws.get(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("expected nil error for missing key, got %v", err)
	}
	if !ts.IsZero() {
		t.Errorf("expected zero time for missing key, got %v", ts)
	}
}

func TestWatermarkStore_SetAndGet(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	now := time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)
	if err := ws.set(ctx, "s1", now); err != nil {
		t.Fatal(err)
	}

	got, err := ws.get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(now) {
		t.Errorf("expected %v, got %v", now, got)
	}
}

func TestWatermarkStore_GlobalPreservesSubsecondBoundary(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	want := time.Date(2026, 7, 2, 10, 5, 0, 123456789, time.UTC)
	if err := ws.set(ctx, "s1", want); err != nil {
		t.Fatal(err)
	}
	got, err := ws.get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Fatalf("global watermark = %v, want exact boundary %v", got, want)
	}
}

func TestWatermarkStore_Upsert(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	t1 := time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 6, 13, 0, 0, 0, time.UTC)

	if err := ws.set(ctx, "s1", t1); err != nil {
		t.Fatal(err)
	}
	if err := ws.set(ctx, "s1", t2); err != nil {
		t.Fatal(err)
	}

	got, err := ws.get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(t2) {
		t.Errorf("expected upserted value %v, got %v", t2, got)
	}
}

func TestWatermarkStore_LineGetSeedsFromLegacy(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	legacy := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	if err := ws.set(ctx, "s1", legacy); err != nil {
		t.Fatal(err)
	}

	got, err := ws.getLine(ctx, "s1", reflectLineFact)
	if err != nil {
		t.Fatal(err)
	}
	if !got.At.Equal(legacy) {
		t.Fatalf("expected legacy seed %v, got %v", legacy, got)
	}
}

func TestWatermarkStore_LineSetWritesRFC3339(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	at := time.Date(2026, 7, 2, 10, 5, 0, 0, time.UTC)
	if err := ws.setLine(ctx, "s1", reflectLineSkill, reviewWatermark{At: at}); err != nil {
		t.Fatal(err)
	}

	got, err := ws.getLine(ctx, "s1", reflectLineSkill)
	if err != nil {
		t.Fatal(err)
	}
	if !got.At.Equal(at) {
		t.Fatalf("expected %v, got %v", at, got)
	}

	raw, ok, err := ws.store.Get(ctx, pkgplugins.StateScope{
		Kind: pkgplugins.StateScopeSession,
		ID:   "s1",
	}, lineWatermarkKey(reflectLineSkill))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected line watermark key to be written")
	}
	if raw["reviewed_at"] != at.Format(time.RFC3339) {
		t.Fatalf("expected RFC3339 value %q, got %#v", at.Format(time.RFC3339), raw["reviewed_at"])
	}
}

func TestWatermarkStore_LinePrefersLineValueOverLegacy(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	legacy := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	line := time.Date(2026, 7, 2, 11, 0, 0, 0, time.UTC)
	if err := ws.set(ctx, "s1", legacy); err != nil {
		t.Fatal(err)
	}
	if err := ws.setLine(ctx, "s1", reflectLineFact, reviewWatermark{At: line}); err != nil {
		t.Fatal(err)
	}

	got, err := ws.getLine(ctx, "s1", reflectLineFact)
	if err != nil {
		t.Fatal(err)
	}
	if !got.At.Equal(line) {
		t.Fatalf("expected line value %v, got %v", line, got)
	}
}

func TestWatermarkStore_LineAdvancesToNewerLegacyAndClearsSequence(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	line := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	legacy := line.Add(time.Hour)
	if err := ws.setLine(ctx, "s1", reflectLineFact, reviewWatermark{At: line, Seq: 42}); err != nil {
		t.Fatal(err)
	}
	if err := ws.set(ctx, "s1", legacy); err != nil {
		t.Fatal(err)
	}

	got, err := ws.getLine(ctx, "s1", reflectLineFact)
	if err != nil {
		t.Fatal(err)
	}
	if !got.At.Equal(legacy) || got.Seq != 0 {
		t.Fatalf("line watermark = %#v, want legacy time with cleared sequence", got)
	}
	raw, ok, err := ws.store.Get(ctx, pkgplugins.StateScope{Kind: pkgplugins.StateScopeSession, ID: "s1"}, lineWatermarkKey(reflectLineFact))
	if err != nil || !ok {
		t.Fatalf("read persisted line: ok=%t err=%v", ok, err)
	}
	if _, exists := raw["reviewed_seq"]; exists {
		t.Fatalf("persisted watermark kept stale sequence: %#v", raw)
	}
}

func TestWatermarkStore_GetLegacyAdvancesToOlderStructuredLine(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	legacy := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	fact := legacy.Add(2 * time.Hour)
	skill := legacy.Add(time.Hour)
	if err := ws.set(ctx, "s1", legacy); err != nil {
		t.Fatal(err)
	}
	if err := ws.setLine(ctx, "s1", reflectLineFact, reviewWatermark{At: fact}); err != nil {
		t.Fatal(err)
	}
	if err := ws.setLine(ctx, "s1", reflectLineSkill, reviewWatermark{At: skill}); err != nil {
		t.Fatal(err)
	}

	got, err := ws.getLegacy(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(skill) {
		t.Fatalf("legacy watermark = %v, want older structured line %v", got, skill)
	}
	persisted, err := ws.get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.Equal(skill) {
		t.Fatalf("persisted legacy watermark = %v, want %v", persisted, skill)
	}
}

func TestWatermarkStore_GetLegacyRequiresBothStructuredLinesAndNeverRewinds(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	legacy := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	if err := ws.set(ctx, "missing", legacy); err != nil {
		t.Fatal(err)
	}
	if err := ws.setLine(ctx, "missing", reflectLineFact, reviewWatermark{At: legacy.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	got, err := ws.getLegacy(ctx, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(legacy) {
		t.Fatalf("legacy with one line = %v, want unchanged %v", got, legacy)
	}

	if err := ws.setLine(ctx, "older", reflectLineFact, reviewWatermark{At: legacy.Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := ws.setLine(ctx, "older", reflectLineSkill, reviewWatermark{At: legacy.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := ws.set(ctx, "older", legacy); err != nil {
		t.Fatal(err)
	}
	got, err = ws.getLegacy(ctx, "older")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(legacy) {
		t.Fatalf("legacy watermark rewound to %v, want %v", got, legacy)
	}
}

func TestWatermarkStore_LineSetWritesSeq(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	at := time.Date(2026, 7, 2, 10, 5, 0, 0, time.UTC)
	if err := ws.setLine(ctx, "s1", reflectLineFact, reviewWatermark{Seq: 42, At: at}); err != nil {
		t.Fatal(err)
	}

	got, err := ws.getLine(ctx, "s1", reflectLineFact)
	if err != nil {
		t.Fatal(err)
	}
	if got.Seq != 42 || !got.At.Equal(at) {
		t.Fatalf("expected seq 42 and at %v, got %#v", at, got)
	}
}

func TestWatermarkStore_LineParsesLegacyLayout(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)

	at := time.Date(2026, 7, 2, 10, 5, 0, 0, time.UTC)
	if err := ws.store.Set(ctx, pkgplugins.StateScope{
		Kind: pkgplugins.StateScopeSession,
		ID:   "s1",
	}, lineWatermarkKey(reflectLineFact), map[string]any{
		"reviewed_at": at.Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := ws.getLine(ctx, "s1", reflectLineFact)
	if err != nil {
		t.Fatal(err)
	}
	if !got.At.Equal(at) {
		t.Fatalf("expected fallback parse %v, got %v", at, got)
	}
}
