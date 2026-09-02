package reflect

import (
	"context"
	"math"
	"testing"
	"time"

	pluginhost "github.com/CherryHQ/stella/internal/plugin/host"

	"github.com/CherryHQ/stella/internal/db/dbtest"
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

	return newWatermarkStore(testStateStore{store: pluginhost.NewStateStore(db)}), context.Background()
}

type testStateStore struct {
	store *pluginhost.StateStore
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

func TestWatermarkStore_LineGetMissing(t *testing.T) {
	ws, ctx := newTestWatermarkStore(t)
	mark, err := ws.getLine(ctx, "nonexistent", reflectLineFact)
	if err != nil {
		t.Fatalf("expected nil error for missing key, got %v", err)
	}
	if mark != (reviewWatermark{}) {
		t.Errorf("expected zero watermark for missing line, got %#v", mark)
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
