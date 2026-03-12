package main_test

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Fuzz test ---------------------------------------------------------------

func FuzzPostQuotes(f *testing.F) {
	// Seed corpus: valid JSON, invalid JSON, edge cases.
	f.Add([]byte(`{"request_id":"fuzz-1","coverage_lines":["auto"],"timeout_ms":5000}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"request_id":"","coverage_lines":[]}`))
	f.Add([]byte(`not json at all`))
	f.Add([]byte{0x00, 0xff, 0xfe})
	f.Add([]byte(`{"request_id":"fuzz-2","coverage_lines":["auto"],"timeout_ms":0}`))
	f.Add([]byte(`{"request_id":"fuzz-3","coverage_lines":["auto"],"timeout_ms":99}`))
	f.Add([]byte(`{"request_id":"fuzz-4","coverage_lines":["auto"],"timeout_ms":31000}`))
	f.Add([]byte(`{"request_id":"fuzz-5","coverage_lines":["bogus"]}`))
	f.Add([]byte(`{"request_id":"fuzz-6","coverage_lines":["auto","homeowners","umbrella"],"timeout_ms":200}`))

	srv := startTestServer(f)

	f.Fuzz(func(t *testing.T, data []byte) {
		resp, err := http.Post(srv.URL+"/quotes", "application/json",
			strings.NewReader(string(data)))
		if err != nil {
			t.Fatalf("POST /quotes transport error: %v", err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}

		// Invariant 1: status in expected set.
		switch resp.StatusCode {
		case 200, 400, 422, 500, 504:
			// expected
		default:
			t.Fatalf("unexpected status %d for input %q", resp.StatusCode, data)
		}

		// Invariant 2: Content-Type is application/json.
		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("expected application/json, got %q", ct)
		}

		// Invariant 3: body is valid JSON.
		if !json.Valid(body) {
			t.Fatalf("response body is not valid JSON: %q", body)
		}

		// Invariant 4: 200 responses have quotes array with valid structure.
		if resp.StatusCode == 200 {
			var result struct {
				RequestID string `json:"request_id"`
				Quotes    []struct {
					CarrierID    string `json:"carrier_id"`
					PremiumCents int64  `json:"premium_cents"`
					Currency     string `json:"currency"`
					LatencyMs    int64  `json:"latency_ms"`
				} `json:"quotes"`
			}
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatalf("200 response not decodable: %v", err)
			}
			for _, q := range result.Quotes {
				if q.CarrierID == "" || q.PremiumCents <= 0 || q.Currency != "USD" {
					t.Fatalf("invalid quote in 200 response: %+v", q)
				}
			}
		}

		// Invariant 5: 400 responses have error field.
		if resp.StatusCode == 400 {
			var errResp struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(body, &errResp); err != nil {
				t.Fatalf("400 response not decodable: %v", err)
			}
			if errResp.Error == "" {
				t.Fatal("400 response missing error field")
			}
		}
	})
}

// --- Random workload ---------------------------------------------------------

func TestE2E_RandomWorkload(t *testing.T) {
	srv := startTestServer(t)

	seed := uint64(time.Now().UnixNano())
	if s := os.Getenv("RANDOM_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("invalid RANDOM_SEED %q: %v", s, err)
		}
		seed = v
	}
	t.Logf("seed=%d (replay: RANDOM_SEED=%d)", seed, seed)
	rng := rand.New(rand.NewPCG(seed, seed^0xBEEFCAFE))

	validLines := []string{"auto", "homeowners", "umbrella"}

	const iterations = 100
	for i := range iterations {
		// ~10% invalid requests.
		invalid := rng.Float64() < 0.10

		var payload string
		if invalid {
			switch rng.IntN(3) {
			case 0: // empty request_id
				payload = `{"request_id":"","coverage_lines":["auto"]}`
			case 1: // bad coverage line
				payload = fmt.Sprintf(`{"request_id":"inv-%d","coverage_lines":["bogus"]}`, i)
			case 2: // timeout out of range
				payload = fmt.Sprintf(`{"request_id":"inv-%d","coverage_lines":["auto"],"timeout_ms":50}`, i)
			}
		} else {
			nLines := rng.IntN(3) + 1
			perm := rng.Perm(len(validLines))
			lines := make([]string, nLines)
			for j := range nLines {
				lines[j] = `"` + validLines[perm[j]] + `"`
			}
			reqID := fmt.Sprintf("rand-%d", i)
			timeoutPart := ""
			if rng.Float64() > 0.3 {
				ms := rng.IntN(1901) + 100 // 100-2000ms
				timeoutPart = fmt.Sprintf(`,"timeout_ms":%d`, ms)
			}
			payload = fmt.Sprintf(`{"request_id":%q,"coverage_lines":[%s]%s}`,
				reqID, strings.Join(lines, ","), timeoutPart)
		}

		resp, err := http.Post(srv.URL+"/quotes", "application/json",
			strings.NewReader(payload))
		if err != nil {
			t.Fatalf("iter %d: transport error: %v", i, err)
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("iter %d: expected application/json, got %q", i, ct)
		}
		if !json.Valid(body) {
			t.Fatalf("iter %d: invalid JSON in response: %q", i, body)
		}

		if invalid {
			if resp.StatusCode != 400 {
				t.Fatalf("iter %d: invalid request expected 400, got %d: %s", i, resp.StatusCode, body)
			}
		} else {
			if resp.StatusCode != 200 && resp.StatusCode != 422 {
				t.Fatalf("iter %d: valid request expected 200 or 422, got %d: %s", i, resp.StatusCode, body)
			}
			if resp.StatusCode == 200 {
				var result struct {
					RequestID string `json:"request_id"`
					Quotes    []struct {
						CarrierID    string `json:"carrier_id"`
						PremiumCents int64  `json:"premium_cents"`
						Currency     string `json:"currency"`
					} `json:"quotes"`
				}
				if err := json.Unmarshal(body, &result); err != nil {
					t.Fatalf("iter %d: decode error: %v", i, err)
				}

				// Sorted by premium ascending.
				for j := 1; j < len(result.Quotes); j++ {
					if result.Quotes[j].PremiumCents < result.Quotes[j-1].PremiumCents {
						t.Fatalf("iter %d: quotes not sorted at index %d", i, j)
					}
				}

				// No duplicate carrier IDs.
				seen := make(map[string]bool, len(result.Quotes))
				for _, q := range result.Quotes {
					if seen[q.CarrierID] {
						t.Fatalf("iter %d: duplicate carrier_id %q", i, q.CarrierID)
					}
					seen[q.CarrierID] = true
					if q.PremiumCents <= 0 {
						t.Fatalf("iter %d: premium_cents=%d, want >0", i, q.PremiumCents)
					}
					if q.Currency != "USD" {
						t.Fatalf("iter %d: currency=%q, want USD", i, q.Currency)
					}
				}

				if result.RequestID == "" {
					t.Fatalf("iter %d: missing request_id in response", i)
				}
			}
		}
	}

	// Concurrent burst: 20 parallel valid requests.
	t.Run("ConcurrentBurst", func(t *testing.T) {
		const burst = 20
		var wg sync.WaitGroup
		wg.Add(burst)
		for g := range burst {
			go func(g int) {
				defer wg.Done()
				payload := fmt.Sprintf(`{"request_id":"burst-%d","coverage_lines":["auto"],"timeout_ms":5000}`, g)
				resp, err := http.Post(srv.URL+"/quotes", "application/json",
					strings.NewReader(payload))
				if err != nil {
					t.Errorf("burst %d: transport error: %v", g, err)
					return
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()

				if resp.StatusCode != 200 && resp.StatusCode != 422 {
					t.Errorf("burst %d: expected 200/422, got %d: %s", g, resp.StatusCode, body)
					return
				}
				if !json.Valid(body) {
					t.Errorf("burst %d: invalid JSON", g)
				}
			}(g)
		}
		wg.Wait()
	})
}
