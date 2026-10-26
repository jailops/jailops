package conformance_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/repo/conformance"
	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/s3fake"
)

func fakeTarget(t *testing.T, opts s3fake.Options) (*s3fake.Server, conformance.Target) {
	t.Helper()
	srv, err := s3fake.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv, conformance.Target{Endpoint: srv.Endpoint(), Region: srv.Region(), AccessKey: srv.AccessKey(), SecretKey: srv.SecretKey()}
}

// The configured S3 service passes the whole suite. By default that is the
// in-process fake (every normal `go test` runs it, which keeps the fake honest);
// with JAILOPS_TEST_S3_ENDPOINT, _ACCESS_KEY and _SECRET_KEY it is that real service.
func TestConformance(t *testing.T) {
	tgt, external, err := conformance.TargetFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if external {
		t.Logf("conformance against the external service %s", tgt.Endpoint)
		conformance.RunConformance(t, tgt)
		return
	}
	_, tgt = fakeTarget(t, s3fake.Options{})
	conformance.RunConformance(t, tgt)
}

func TestSuiteSize(t *testing.T) {
	if n := len(conformance.Checks()); n < 36 {
		t.Fatalf("%d checks, the suite must have at least 36", n)
	}
	seen := map[string]bool{}
	for _, c := range conformance.Checks() {
		if seen[c.Name] {
			t.Fatalf("duplicate check %q", c.Name)
		}
		seen[c.Name] = true
	}
}

// The shared datastore contract of the repository, run against the S3 client.
func TestDatastoreContract(t *testing.T) {
	tgt, external, err := conformance.TargetFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if external && !tgt.MakeBuckets {
		t.Skip("JAILOPS_TEST_S3_MAKE_BUCKETS=1 is needed to run the datastore contract on an external service")
	}
	if !external {
		_, tgt = fakeTarget(t, s3fake.Options{})
	}
	datastore.RunConformance(t, conformance.Factory(tgt, nil))
}

// Mutation tests. A suite that cannot tell a broken server from a good one proves
// nothing. Each case switches ONE deliberate defect on in the fake and requires
// the targeted checks to FAIL, while an unrelated check still PASSES (so the
// failure is the suite noticing the defect, not a broken harness).
func TestSuiteFailsAgainstBrokenServers(t *testing.T) {
	cases := []struct {
		quirk  s3fake.Quirk
		failed []string
	}{
		{s3fake.QuirkIgnoreIfNoneMatch, []string{"put/if-none-match-refuses-existing", "put/if-none-match-keeps-the-original", "probe/passes", "put/if-none-match-is-atomic-under-concurrency", "put/if-none-match-empty-object-exists"}},
		{s3fake.QuirkNonAtomicPut, []string{"put/if-none-match-is-atomic-under-concurrency"}},
		{s3fake.QuirkAcceptAnySignature, []string{"auth/wrong-secret-refused", "auth/unknown-access-key-refused", "auth/anonymous-refused", "auth/tampered-request-refused", "auth/skewed-clock-refused"}},
		{s3fake.QuirkNoContinuation, []string{"list/over-1000-keys", "list/pagination-with-continuation-tokens"}},
		{s3fake.QuirkUnsortedListing, []string{"list/lexical-order", "list/prefix"}},
		{s3fake.QuirkIgnoreRange, []string{"object/range-middle", "object/range-open-ended"}},
		{s3fake.QuirkRangeOffByOne, []string{"object/range-middle"}},
		{s3fake.QuirkTornPutVisible, []string{"put/torn-upload-leaves-no-object"}},
		{s3fake.QuirkDeleteIgnored, []string{"object/delete-removes", "put/if-none-match-after-delete"}},
		{s3fake.QuirkIgnorePrefix, []string{"list/prefix", "list/prefix-is-not-component-aligned"}},
		{s3fake.QuirkIgnoreStartAfter, []string{"list/start-after"}},
		{s3fake.QuirkRepeatPageEdge, []string{"list/over-1000-keys"}},
		{s3fake.QuirkMissingKeyIs200, []string{"object/get-missing"}},
		{s3fake.QuirkHeadMissing200, []string{"object/head-missing"}},
		{s3fake.QuirkLosePutBody, []string{"object/put-get-roundtrip-sizes"}},
		{s3fake.QuirkListHidesRecent, []string{"put/read-your-write"}},
		{s3fake.QuirkIgnoreSkew, []string{"auth/skewed-clock-refused"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.quirk), func(t *testing.T) {
			_, tgt := fakeTarget(t, s3fake.Options{Quirks: []s3fake.Quirk{tc.quirk}})
			for _, name := range tc.failed {
				ch, ok := conformance.Lookup(name)
				if !ok {
					t.Fatalf("no check %q", name)
				}
				fails := conformance.Run(tgt, ch)
				if len(fails) == 0 {
					t.Errorf("check %q PASSED against a server with the %q defect: the suite does not notice it", name, tc.quirk)
				} else {
					t.Logf("ok: %q fails as it must: %s", name, strings.Split(fails[0], "\n")[0])
				}
			}
			ctl, _ := conformance.Lookup("bucket/head")
			if fails := conformance.Run(tgt, ctl); len(fails) != 0 {
				t.Errorf("control check failed on the broken server: %v", fails)
			}
		})
	}
}

// A service that answers 404 to the delete of a missing key is absorbed by the
// client (the delete is idempotent by contract), so the suite must still pass.
func TestClientAbsorbsDeleteOfMissingKey404(t *testing.T) {
	_, tgt := fakeTarget(t, s3fake.Options{Quirks: []s3fake.Quirk{s3fake.QuirkDeleteMissing404}})
	ch, _ := conformance.Lookup("object/delete-is-idempotent")
	if fails := conformance.Run(tgt, ch); len(fails) != 0 {
		t.Fatal(fails)
	}
}

// Every quirk of the fake is covered by a mutation case.
func TestEveryQuirkHasAMutationCase(t *testing.T) {
	covered := map[s3fake.Quirk]bool{}
	for _, q := range []s3fake.Quirk{s3fake.QuirkIgnoreIfNoneMatch, s3fake.QuirkNonAtomicPut, s3fake.QuirkAcceptAnySignature, s3fake.QuirkNoContinuation,
		s3fake.QuirkUnsortedListing, s3fake.QuirkIgnoreRange, s3fake.QuirkRangeOffByOne, s3fake.QuirkTornPutVisible,
		s3fake.QuirkDeleteIgnored, s3fake.QuirkIgnorePrefix, s3fake.QuirkIgnoreStartAfter, s3fake.QuirkRepeatPageEdge, s3fake.QuirkMissingKeyIs200,
		s3fake.QuirkHeadMissing200, s3fake.QuirkLosePutBody, s3fake.QuirkListHidesRecent, s3fake.QuirkIgnoreSkew} {
		covered[q] = true
	}
	// QuirkAcceptWrongPayload is exercised by the client tests (a flipped body byte must be refused).
	covered[s3fake.QuirkAcceptWrongPayload] = true
	covered[s3fake.QuirkDeleteMissing404] = true // TestClientAbsorbsDeleteOfMissingKey404
	for _, q := range s3fake.AllQuirks() {
		if !covered[q] {
			t.Errorf("quirk %q has no mutation case", q)
		}
	}
}
