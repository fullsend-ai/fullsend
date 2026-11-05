package repos

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// mergeLookupRoot decodes a single-document YAML string and returns its root
// mapping.
func mergeLookupRoot(t *testing.T, content string) *yaml.Node {
	t.Helper()
	docs, err := decodeGitLabDocuments([]byte(content))
	require.NoError(t, err)
	require.Len(t, docs, 1)
	return docs[0].Content[0]
}

// fanOutMergeYAML builds an acyclic alias DAG of the given number of levels
// where each level's "<<" list repeats the previous level's alias fanOut
// times. Level 0 holds only the key "base".
func fanOutMergeYAML(levels, fanOut int) string {
	var b strings.Builder
	b.WriteString("l0: &l0\n  base: found\n")
	for i := 1; i <= levels; i++ {
		aliases := make([]string, fanOut)
		for j := range aliases {
			aliases[j] = fmt.Sprintf("*l%d", i-1)
		}
		fmt.Fprintf(&b, "l%d: &l%d\n  <<: [%s]\n", i, i, strings.Join(aliases, ", "))
	}
	return b.String()
}

func TestEffectiveMappingValue_MergeSemantics(t *testing.T) {
	root := mergeLookupRoot(t, `
a: &a
  shared: from-a
  only_a: a
b: &b
  shared: from-b
  only_b: b
job:
  <<: [*a, *b]
  explicit: job
  shared_override: job
override:
  <<: *a
  shared: explicit
`)
	job := findMappingValue(root, "job")
	override := findMappingValue(root, "override")

	v, ok := effectiveMappingValue(job, "explicit")
	require.True(t, ok)
	assert.Equal(t, "job", v.Value)

	v, ok = effectiveMappingValue(job, "shared")
	require.True(t, ok)
	assert.Equal(t, "from-a", v.Value, "earlier merge source wins")

	v, ok = effectiveMappingValue(job, "only_b")
	require.True(t, ok)
	assert.Equal(t, "b", v.Value)

	v, ok = effectiveMappingValue(job, "absent")
	assert.True(t, ok)
	assert.Nil(t, v)

	v, ok = effectiveMappingValue(override, "shared")
	require.True(t, ok)
	assert.Equal(t, "explicit", v.Value, "explicit key wins over merge source")
}

func TestEffectiveMappingValue_UnevaluableMergeDefers(t *testing.T) {
	root := mergeLookupRoot(t, `
scalar: &scalar text
list: &list [1, 2]
bad_scalar:
  <<: *scalar
bad_in_list:
  <<: [*scalar]
`)
	for _, name := range []string{"bad_scalar", "bad_in_list"} {
		v, ok := effectiveMappingValue(findMappingValue(root, name), "absent")
		assert.False(t, ok, name)
		assert.Nil(t, v, name)
	}

	v, ok := effectiveMappingValue(findMappingValue(root, "list"), "absent")
	assert.False(t, ok, "non-mapping lookup target")
	assert.Nil(t, v)

	// A self-referential merge is cut off by the depth limit.
	cyclic := &yaml.Node{Kind: yaml.MappingNode}
	cyclic.Content = []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: "<<"},
		{Kind: yaml.AliasNode, Alias: cyclic},
	}
	v, ok = effectiveMappingValue(cyclic, "absent")
	assert.False(t, ok, "cyclic merge")
	assert.Nil(t, v)
}

func TestEffectiveMappingValue_DepthLimit(t *testing.T) {
	within := mergeLookupRoot(t, fanOutMergeYAML(maxMergeDepth, 1))
	v, ok := effectiveMappingValue(findMappingValue(within, fmt.Sprintf("l%d", maxMergeDepth)), "base")
	require.True(t, ok)
	assert.Equal(t, "found", v.Value)

	beyond := mergeLookupRoot(t, fanOutMergeYAML(maxMergeDepth+1, 1))
	v, ok = effectiveMappingValue(findMappingValue(beyond, fmt.Sprintf("l%d", maxMergeDepth+1)), "base")
	assert.False(t, ok)
	assert.Nil(t, v)
}

// A compact, acyclic alias DAG with heavily repeated merge sources would
// require fanOut^levels visits without memoization; it must resolve
// promptly and correctly, including for an absent key.
func TestEffectiveMappingValue_HighFanOutMergeDAG(t *testing.T) {
	const levels, fanOut = 15, 10
	root := mergeLookupRoot(t, fanOutMergeYAML(levels, fanOut))
	top := findMappingValue(root, fmt.Sprintf("l%d", levels))

	v, ok := effectiveMappingValue(top, "absent")
	assert.True(t, ok)
	assert.Nil(t, v)

	v, ok = effectiveMappingValue(top, "base")
	require.True(t, ok)
	assert.Equal(t, "found", v.Value)
}

func TestEffectiveMappingValue_WorkBudget(t *testing.T) {
	wideMerge := func(sources int) *yaml.Node {
		aliases := make([]string, sources)
		for i := range aliases {
			aliases[i] = "*base"
		}
		root := mergeLookupRoot(t, "base: &base\n  key: found\ntop:\n  <<: ["+strings.Join(aliases, ", ")+"]\n")
		return findMappingValue(root, "top")
	}

	v, ok := effectiveMappingValue(wideMerge(mergeLookupBudget), "absent")
	assert.True(t, ok, "graph within budget resolves")
	assert.Nil(t, v)

	v, ok = effectiveMappingValue(wideMerge(mergeLookupBudget+1), "absent")
	assert.False(t, ok, "graph beyond budget defers")
	assert.Nil(t, v)

	v, ok = effectiveMappingValue(wideMerge(mergeLookupBudget+1), "key")
	require.True(t, ok, "early match does not spend the budget")
	assert.Equal(t, "found", v.Value)
}

// A wide mapping lacking the key, merged through many repeated aliases, must
// be scanned once rather than once per alias.
func TestEffectiveMappingValue_RepeatedAliasesToWideMapping(t *testing.T) {
	const width, sources = 200000, mergeLookupBudget
	wide := &yaml.Node{Kind: yaml.MappingNode}
	for i := 0; i < width; i++ {
		wide.Content = append(wide.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprintf("key-%06d", i)},
			&yaml.Node{Kind: yaml.ScalarNode, Value: "v"})
	}
	list := &yaml.Node{Kind: yaml.SequenceNode}
	for i := 0; i < sources; i++ {
		list.Content = append(list.Content, &yaml.Node{Kind: yaml.AliasNode, Alias: wide})
	}
	top := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: "<<"}, list,
	}}

	start := time.Now()
	v, ok := effectiveMappingValue(top, "absent")
	elapsed := time.Since(start)
	assert.True(t, ok)
	assert.Nil(t, v)
	assert.Less(t, elapsed, time.Second, "mapping must not be rescanned per alias")

	v, ok = effectiveMappingValue(top, "key-199999")
	require.True(t, ok)
	assert.Equal(t, "v", v.Value)
}
