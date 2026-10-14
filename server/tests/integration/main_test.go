package integration

import (
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/warriorguo/ozx_apm/server/tests/integration/testutil"
)

// TestMain creates the test database and runs the server's migrations once,
// so the ClickHouse tests exercise the real schema instead of skipping
// because ozx_apm_test does not exist. Without a reachable ClickHouse the
// individual tests still skip as before.
func TestMain(m *testing.M) {
	flag.Parse()
	if !testing.Short() {
		client, err := testutil.SetupTestDatabase(testutil.TestClickHouseConfig())
		if err != nil {
			fmt.Fprintf(os.Stderr, "integration: ClickHouse setup failed, ClickHouse tests will skip: %v\n", err)
		} else {
			client.Close()
		}
	}
	os.Exit(m.Run())
}
