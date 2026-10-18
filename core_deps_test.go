package gas_test

import (
	"os"
	"strings"
	"testing"
)

// Modules meet only through core, so core must not depend on any of them,
// including config: config implements core's ConfigProvider, not the other
// way round.
func TestCoreRequiresNoGasModule(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "require" {
			fields = fields[1:]
		}
		if len(fields) > 0 && strings.HasPrefix(fields[0], "github.com/gasmod/gas/") {
			t.Errorf("core go.mod requires %s; core must not depend on a gas module", fields[0])
		}
	}
}
