package jql

import "testing"

func TestSplitOrderBy(t *testing.T) {
	cases := []struct {
		name, query, predicate, orderBy string
	}{
		{name: "with order by", query: `labels = "fullsend" ORDER BY updated DESC`, predicate: `labels = "fullsend" `, orderBy: `ORDER BY updated DESC`},
		{name: "without order by", query: `labels = "fullsend"`, predicate: `labels = "fullsend"`},
		{name: "lowercase with surrounding space", query: `  status = Open order  by created  `, predicate: `status = Open `, orderBy: `order  by created`},
		{name: "inside double quotes", query: `summary ~ "order by" AND status = Open`, predicate: `summary ~ "order by" AND status = Open`},
		{name: "inside single quotes", query: `summary ~ 'x order by y' ORDER BY key`, predicate: `summary ~ 'x order by y' `, orderBy: `ORDER BY key`},
		{name: "escaped quote", query: `summary ~ "a \" order by" ORDER BY key`, predicate: `summary ~ "a \" order by" `, orderBy: `ORDER BY key`},
		{name: "only order by", query: `ORDER BY updated DESC`, orderBy: `ORDER BY updated DESC`},
		{name: "unterminated quote", query: `summary ~ "order by`, predicate: `summary ~ "order by`},
		{name: "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			predicate, orderBy := SplitOrderBy(tc.query)
			if predicate != tc.predicate || orderBy != tc.orderBy {
				t.Errorf("SplitOrderBy(%q) = %q, %q; want %q, %q", tc.query, predicate, orderBy, tc.predicate, tc.orderBy)
			}
		})
	}
}
