package steps

import (
	"testing"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	messages "github.com/cucumber/messages/go/v21"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func TestParseDummyAgentTable_RequiresFixturesRoot(t *testing.T) {
	w := &world.World{}
	table := &godog.Table{
		Rows: []*messages.PickleTableRow{
			{Cells: []*messages.PickleTableCell{{Value: "description"}, {Value: "op"}, {Value: "args"}}},
			{Cells: []*messages.PickleTableCell{{Value: "x"}, {Value: "read_file"}, {Value: "foo"}}},
		},
	}
	err := parseDummyAgentTable(w, table)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FixturesRoot")
}

func TestFindModuleSubdir(t *testing.T) {
	dir, err := findModuleSubdir("pkg/behaviourtest/steps")
	require.NoError(t, err)
	assert.DirExists(t, dir)
}
