package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSkillEntry_UnmarshalYAML_StringForm(t *testing.T) {
	input := `- skills/pr-review`
	var entries []SkillEntry
	require.NoError(t, yaml.Unmarshal([]byte(input), &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, "skills/pr-review", entries[0].Source)
	assert.Nil(t, entries[0].Overrides)
	assert.False(t, entries[0].HasOverrides())
}

func TestSkillEntry_UnmarshalYAML_MapForm(t *testing.T) {
	input := `
- skills/pr-review:
    sub-agents/security.md: .fullsend/overrides/security.md
    sub-agents/supply-chain.md: .fullsend/overrides/supply-chain.md
`
	var entries []SkillEntry
	require.NoError(t, yaml.Unmarshal([]byte(input), &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, "skills/pr-review", entries[0].Source)
	assert.True(t, entries[0].HasOverrides())
	require.Len(t, entries[0].Overrides, 2)
	assert.Equal(t, ".fullsend/overrides/security.md", *entries[0].Overrides["sub-agents/security.md"])
	assert.Equal(t, ".fullsend/overrides/supply-chain.md", *entries[0].Overrides["sub-agents/supply-chain.md"])
}

func TestSkillEntry_UnmarshalYAML_NullRemoval(t *testing.T) {
	input := `
- skills/pr-review:
    sub-agents/cross-repo-contracts.md: null
`
	var entries []SkillEntry
	require.NoError(t, yaml.Unmarshal([]byte(input), &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, "skills/pr-review", entries[0].Source)
	require.Contains(t, entries[0].Overrides, "sub-agents/cross-repo-contracts.md")
	assert.Nil(t, entries[0].Overrides["sub-agents/cross-repo-contracts.md"])
}

func TestSkillEntry_UnmarshalYAML_MixedForms(t *testing.T) {
	input := `
- skills/code-review
- skills/pr-review:
    sub-agents/security.md: .fullsend/overrides/security.md
`
	var entries []SkillEntry
	require.NoError(t, yaml.Unmarshal([]byte(input), &entries))
	require.Len(t, entries, 2)
	assert.Equal(t, "skills/code-review", entries[0].Source)
	assert.False(t, entries[0].HasOverrides())
	assert.Equal(t, "skills/pr-review", entries[1].Source)
	assert.True(t, entries[1].HasOverrides())
}

func TestSkillEntry_MarshalYAML_RoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		entry SkillEntry
	}{
		{
			name:  "string form",
			entry: SkillEntry{Source: "skills/pr-review"},
		},
		{
			name: "map form with overrides",
			entry: SkillEntry{
				Source: "skills/pr-review",
				Overrides: map[string]*string{
					"sub-agents/security.md": strPtr(".fullsend/overrides/security.md"),
				},
			},
		},
		{
			name: "map form with null removal",
			entry: SkillEntry{
				Source: "skills/pr-review",
				Overrides: map[string]*string{
					"sub-agents/cross-repo.md": nil,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := yaml.Marshal([]SkillEntry{tt.entry})
			require.NoError(t, err)

			var result []SkillEntry
			require.NoError(t, yaml.Unmarshal(data, &result))
			require.Len(t, result, 1)
			assert.Equal(t, tt.entry.Source, result[0].Source)

			if tt.entry.HasOverrides() {
				assert.Equal(t, len(tt.entry.Overrides), len(result[0].Overrides))
				for k, v := range tt.entry.Overrides {
					rv, ok := result[0].Overrides[k]
					require.True(t, ok, "key %q missing", k)
					if v == nil {
						assert.Nil(t, rv)
					} else {
						require.NotNil(t, rv)
						assert.Equal(t, *v, *rv)
					}
				}
			} else {
				assert.Nil(t, result[0].Overrides)
			}
		})
	}
}

func TestSkillEntry_HarnessYAML_BackwardCompat(t *testing.T) {
	// Existing string-only skills lists must parse identically.
	input := `
agent: agents/test.md
role: test
skills:
  - skills/code-review
  - skills/pr-review
`
	var h Harness
	require.NoError(t, yaml.Unmarshal([]byte(input), &h))
	require.Len(t, h.Skills, 2)
	assert.Equal(t, "skills/code-review", h.Skills[0].Source)
	assert.Equal(t, "skills/pr-review", h.Skills[1].Source)
	assert.False(t, h.Skills[0].HasOverrides())
	assert.False(t, h.Skills[1].HasOverrides())
}

func TestSkillEntry_HarnessYAML_ObjectForm(t *testing.T) {
	input := `
agent: agents/test.md
role: test
skills:
  - skills/code-review
  - skills/pr-review:
      sub-agents/security.md: .fullsend/overrides/security.md
      sub-agents/cross-repo.md: null
`
	var h Harness
	require.NoError(t, yaml.Unmarshal([]byte(input), &h))
	require.Len(t, h.Skills, 2)
	assert.Equal(t, "skills/code-review", h.Skills[0].Source)
	assert.False(t, h.Skills[0].HasOverrides())
	assert.Equal(t, "skills/pr-review", h.Skills[1].Source)
	assert.True(t, h.Skills[1].HasOverrides())
	assert.Equal(t, ".fullsend/overrides/security.md", *h.Skills[1].Overrides["sub-agents/security.md"])
	assert.Nil(t, h.Skills[1].Overrides["sub-agents/cross-repo.md"])
}

func TestSkillSources(t *testing.T) {
	entries := []SkillEntry{
		{Source: "a"},
		{Source: "b"},
		{Source: "c"},
	}
	assert.Equal(t, []string{"a", "b", "c"}, SkillSources(entries))
	assert.Nil(t, SkillSources(nil))
}

func TestValidateSkillOverrides(t *testing.T) {
	t.Run("valid overrides", func(t *testing.T) {
		entries := []SkillEntry{
			{
				Source: "skills/pr-review",
				Overrides: map[string]*string{
					"sub-agents/security.md": strPtr("path/to/override.md"),
					"SKILL.md":               strPtr("path/to/skill.md"),
				},
			},
		}
		assert.NoError(t, ValidateSkillOverrides(entries))
	})

	t.Run("path traversal rejected", func(t *testing.T) {
		entries := []SkillEntry{
			{
				Source: "skills/pr-review",
				Overrides: map[string]*string{
					"../escape.md": strPtr("path/to/override.md"),
				},
			},
		}
		err := ValidateSkillOverrides(entries)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "path traversal")
	})

	t.Run("absolute path rejected", func(t *testing.T) {
		entries := []SkillEntry{
			{
				Source: "skills/pr-review",
				Overrides: map[string]*string{
					"/absolute/path.md": strPtr("path/to/override.md"),
				},
			},
		}
		err := ValidateSkillOverrides(entries)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "relative")
	})

	t.Run("empty key rejected", func(t *testing.T) {
		entries := []SkillEntry{
			{
				Source: "skills/pr-review",
				Overrides: map[string]*string{
					"": strPtr("path/to/override.md"),
				},
			},
		}
		err := ValidateSkillOverrides(entries)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "non-empty")
	})

	t.Run("empty source rejected", func(t *testing.T) {
		empty := ""
		entries := []SkillEntry{
			{
				Source: "skills/pr-review",
				Overrides: map[string]*string{
					"sub-agents/x.md": &empty,
				},
			},
		}
		err := ValidateSkillOverrides(entries)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "non-empty")
	})

	t.Run("null removal accepted", func(t *testing.T) {
		entries := []SkillEntry{
			{
				Source: "skills/pr-review",
				Overrides: map[string]*string{
					"sub-agents/x.md": nil,
				},
			},
		}
		assert.NoError(t, ValidateSkillOverrides(entries))
	})

	t.Run("no overrides accepted", func(t *testing.T) {
		entries := []SkillEntry{
			{Source: "skills/pr-review"},
		}
		assert.NoError(t, ValidateSkillOverrides(entries))
	})

	// Two keys must not name one file, so only the path.Clean form passes.
	for key, want := range map[string]string{
		"./sub-agents/x.md": `must be in clean form ("sub-agents/x.md")`,
		"sub-agents//x.md":  "must be in clean form",
		"sub-agents/":       "must be in clean form",
		"sub-agents/./x.md": "must be in clean form",
		".":                 "must name a file in the skill",
	} {
		t.Run("unclean key rejected "+key, func(t *testing.T) {
			err := ValidateSkillOverrides([]SkillEntry{{
				Source:    "skills/pr-review",
				Overrides: map[string]*string{key: nil},
			}})
			require.Error(t, err)
			assert.Contains(t, err.Error(), want)
		})
	}
}

func TestValidateSkillOverrides_ValueTraversal(t *testing.T) {
	entries := []SkillEntry{
		{
			Source: "skills/pr-review",
			Overrides: map[string]*string{
				"sub-agents/security.md": strPtr("../../etc/passwd"),
			},
		},
	}
	err := ValidateSkillOverrides(entries)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal")
}

func TestValidateSkillOverrides_ValueNullByte(t *testing.T) {
	entries := []SkillEntry{
		{
			Source: "skills/pr-review",
			Overrides: map[string]*string{
				"sub-agents/security.md": strPtr("path/to\x00evil"),
			},
		},
	}
	err := ValidateSkillOverrides(entries)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "null bytes")
}

func TestValidateSkillOverrides_AbsoluteValueSkippedPostResolution(t *testing.T) {
	entries := []SkillEntry{
		{
			Source: "skills/pr-review",
			Overrides: map[string]*string{
				"sub-agents/security.md": strPtr("/cache/resolved/override.md"),
			},
		},
	}
	assert.NoError(t, ValidateSkillOverrides(entries))
}

func TestValidateOverrideValue_AbsoluteValueRejected(t *testing.T) {
	err := validateOverrideValue("skills[0]", "sub-agents/security.md", "/absolute/path/override.md")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "relative")
}

func TestValidateSkillOverrides_ValueQueryFragmentRejected(t *testing.T) {
	for _, marker := range []string{"?", "#"} {
		entries := []SkillEntry{
			{
				Source: "skills/pr-review",
				Overrides: map[string]*string{
					"sub-agents/security.md": strPtr("path/to/override.md" + marker + "extra"),
				},
			},
		}
		err := ValidateSkillOverrides(entries)
		assert.Error(t, err, "marker %q should be rejected", marker)
		assert.Contains(t, err.Error(), "query or fragment")
	}
}

func TestValidateSkillOverrides_KeyNullByte(t *testing.T) {
	entries := []SkillEntry{
		{
			Source: "skills/pr-review",
			Overrides: map[string]*string{
				"sub-agents/\x00evil.md": strPtr("path/to/override.md"),
			},
		},
	}
	err := ValidateSkillOverrides(entries)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "null bytes")
}

func TestSkillEntry_UnmarshalYAML_EmptyStringValue(t *testing.T) {
	input := `
- skills/pr-review:
    sub-agents/security.md: ""
`
	var entries []SkillEntry
	require.NoError(t, yaml.Unmarshal([]byte(input), &entries))
	require.Len(t, entries, 1)
	require.Contains(t, entries[0].Overrides, "sub-agents/security.md")
	require.NotNil(t, entries[0].Overrides["sub-agents/security.md"])
	assert.Equal(t, "", *entries[0].Overrides["sub-agents/security.md"])
}

func TestSkillEntry_UnmarshalYAML_MultiKeyMap(t *testing.T) {
	input := `
- skills/a:
    x.md: y.md
  skills/b:
    x.md: y.md
`
	var entries []SkillEntry
	err := yaml.Unmarshal([]byte(input), &entries)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one key")
}

func TestSkillEntry_UnmarshalYAML_NonMapOverrides(t *testing.T) {
	input := `
- skills/pr-review: not-a-map
`
	var entries []SkillEntry
	err := yaml.Unmarshal([]byte(input), &entries)
	assert.Error(t, err)
}

func TestSkillEntry_UnmarshalYAML_SequenceNode(t *testing.T) {
	input := `
- - nested
  - list
`
	var entries []SkillEntry
	err := yaml.Unmarshal([]byte(input), &entries)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "string or a single-key map")
}

func TestMergeSkills_WithOverrides(t *testing.T) {
	base := []SkillEntry{
		{
			Source: "skills/pr-review",
			Overrides: map[string]*string{
				"sub-agents/security.md": strPtr("base/security.md"),
				"sub-agents/perf.md":     strPtr("base/perf.md"),
			},
		},
	}
	child := []SkillEntry{
		{
			Source: "skills/pr-review",
			Overrides: map[string]*string{
				"sub-agents/security.md": strPtr("child/security.md"),
			},
		},
	}

	result := mergeSkills(base, child)
	require.Len(t, result, 1)
	assert.Equal(t, "skills/pr-review", result[0].Source)
	// Child override wins for security.md
	assert.Equal(t, "child/security.md", *result[0].Overrides["sub-agents/security.md"])
	// Base override inherited for perf.md
	assert.Equal(t, "base/perf.md", *result[0].Overrides["sub-agents/perf.md"])
}

func TestMergeOverrides(t *testing.T) {
	t.Run("nil inputs", func(t *testing.T) {
		assert.Nil(t, mergeOverrides(nil, nil))
	})

	t.Run("hi wins on conflict", func(t *testing.T) {
		lo := map[string]*string{"a": strPtr("lo-a"), "b": strPtr("lo-b")}
		hi := map[string]*string{"b": strPtr("hi-b"), "c": strPtr("hi-c")}
		result := mergeOverrides(lo, hi)
		assert.Equal(t, "lo-a", *result["a"])
		assert.Equal(t, "hi-b", *result["b"])
		assert.Equal(t, "hi-c", *result["c"])
	})

	t.Run("nil override wins", func(t *testing.T) {
		lo := map[string]*string{"a": strPtr("lo-a")}
		hi := map[string]*string{"a": nil}
		result := mergeOverrides(lo, hi)
		assert.Nil(t, result["a"])
	})
}

func strPtr(s string) *string {
	return &s
}

func TestValidateOverrideKey_Exported(t *testing.T) {
	assert.NoError(t, ValidateOverrideKey("skills[0]", "sub-agents/security.md"))
	assert.ErrorContains(t, ValidateOverrideKey("skills[0]", "../x.md"), "skills[0]: override file path \"../x.md\" must not contain path traversal segments")
}
