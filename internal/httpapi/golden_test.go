package httpapi_test

import (
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

var updateGolden = flag.Bool("update", false, "regenerate testdata/golden/*.json")

// TestHTTP_GoldenResponses captures the full body of /v1/health and
// /v1/anomalies?type=... for the deterministic seed dataset and compares it
// to the golden file. Run `go test ./internal/httpapi -run TestHTTP_Golden -update`
// to regenerate.
//
// Normalisation rules:
//   - arrays of items are sorted by transaction_id (stable order across runs)
//   - the body is round-tripped through json.Marshal with sorted maps so
//     iteration order doesn't leak in
func TestHTTP_GoldenResponses(t *testing.T) {
	exp := loadExpected(t)
	srv, repo := newTestServerSQLite(t, exp.Now)
	loadFixturesInto(t, repo)

	from := exp.WindowFrom.Format("2006-01-02T15:04:05Z")
	to := exp.WindowTo.Format("2006-01-02T15:04:05Z")
	q := "?from=" + from + "&to=" + to

	cases := []struct {
		name string
		path string
	}{
		{"health_full", "/v1/health" + q},
		{"anomalies_orphaned", "/v1/anomalies" + q + "&type=orphaned&limit=1000"},
		{"anomalies_ghost", "/v1/anomalies" + q + "&type=ghost&limit=1000"},
		{"anomalies_duplicate", "/v1/anomalies" + q + "&type=duplicate&limit=1000"},
		{"anomalies_pending_limbo", "/v1/anomalies" + q + "&type=pending_limbo&limit=1000"},
	}

	root := repoRoot(t)
	goldenDir := filepath.Join(root, "testdata", "golden")
	if err := os.MkdirAll(goldenDir, 0o755); err != nil {
		t.Fatalf("mkdir golden: %v", err)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + c.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, body)
			}
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			normalized := normalizeJSON(t, raw)
			path := filepath.Join(goldenDir, c.name+".json")
			if *updateGolden {
				if err := os.WriteFile(path, normalized, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update first): %v", err)
			}
			wantNorm := normalizeJSON(t, want)
			if string(wantNorm) != string(normalized) {
				t.Errorf("golden mismatch for %s.\nWANT:\n%s\nGOT:\n%s", c.name, wantNorm, normalized)
			}
		})
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for cur := wd; cur != "/" && cur != ""; cur = filepath.Dir(cur) {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur
		}
	}
	t.Fatal("go.mod not found")
	return ""
}

// normalizeJSON parses the body, sorts arrays of items/groups by
// transaction_id and re-emits with stable key ordering so diffs are clean.
func normalizeJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal for normalisation: %v\n%s", err, raw)
	}
	v = normalizeValue(v)
	out, err := marshalSorted(v)
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func normalizeValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			x[k] = normalizeValue(val)
		}
		return x
	case []any:
		for i, val := range x {
			x[i] = normalizeValue(val)
		}
		sort.SliceStable(x, func(i, j int) bool {
			return sortKey(x[i]) < sortKey(x[j])
		})
		return x
	default:
		return v
	}
}

// sortKey produces a stable sort key for an array element. For objects we
// look at common ID-like fields. For scalars, fmt them.
func sortKey(v any) string {
	if m, ok := v.(map[string]any); ok {
		for _, k := range []string{"transaction_id", "key", "name"} {
			if s, ok := m[k].(string); ok {
				return k + ":" + s
			}
		}
		// fall back to deterministic per-row signature
		b, _ := json.Marshal(m)
		return string(b)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// marshalSorted emits JSON with sorted map keys at every depth (encoding/json
// already sorts keys alphabetically by default for map[string]any, which is
// what we get from Unmarshal — but we run it through MarshalIndent for
// readable golden files).
func marshalSorted(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
