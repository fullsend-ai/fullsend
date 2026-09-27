package binary

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCrossCompileArgs_StripsSymbolsAndStampsVersion(t *testing.T) {
	args := crossCompileArgs("1.2.3-vendored", "/tmp/fullsend")

	var ldflags string
	for i, a := range args {
		if a == "-ldflags" {
			require.Less(t, i+1, len(args), "-ldflags has no value")
			ldflags = args[i+1]
		}
	}
	require.NotEmpty(t, ldflags, "build args carry no -ldflags")

	fields := strings.Fields(ldflags)
	assert.Contains(t, fields, "-s", "symbol table not stripped")
	assert.Contains(t, fields, "-w", "DWARF not stripped")
	assert.Contains(t, ldflags, "-X github.com/fullsend-ai/fullsend/internal/cli.version=1.2.3-vendored")
	assert.Equal(t, []string{"-o", "/tmp/fullsend", "./cmd/fullsend/"}, args[len(args)-3:])
}
