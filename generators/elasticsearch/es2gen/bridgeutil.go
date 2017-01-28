package es2gen

import (
	"fmt"
	"strconv"
	"strings"

	u "github.com/araddon/gou"
	"github.com/araddon/qlbridge/expr"
	"github.com/araddon/qlbridge/lex"
	"github.com/araddon/qlbridge/value"

	"github.com/araddon/qlbridge/generators/elasticsearch/gentypes"
)

var _ = u.EMPTY

type floatval interface {
	Float() float64
}

// scalar returns a JSONable representation of a scalar node type for use in ES
// filters.
//
// Does not support Null.
//
func scalar(node expr.Node) (interface{}, bool) {
	switch n := node.(type) {

	case *expr.StringNode:
		return n.Text, true

	case *expr.NumberNode:
		if n.IsInt {
			// ES supports string encoded ints
			return n.Int64, true
		}
		return n.Float64, true

	case *expr.ValueNode:
		// Make sure this is a scalar value node
		switch n.Value.Type() {
		case value.BoolType, value.IntType, value.StringType, value.TimeType:
			return n.String(), true
		case value.NumberType:
			nn, ok := n.Value.(floatval)
			if !ok {
				return nil, false
			}
			return nn.Float(), true
		}
	case *expr.IdentityNode:
		if _, err := strconv.ParseBool(n.Text); err == nil {
			return n.Text, true
		}

	}
	return "", false
}

// makeRange returns a range filter for Elasticsearch given the 3 nodes that
// make up a comparison.
func makeRange(lhs *gentypes.FieldType, op lex.TokenType, rhs expr.Node) (interface{}, error) {
	rhsval, ok := scalar(rhs)
	if !ok {
		return nil, fmt.Errorf("qlindex: unsupported type for comparison: %T", rhs)
	}

	// Convert scalars from strings to floats if lhs is numeric and rhs is a
	// float (ES handles ints as strings just fine).
	if lhs.Numeric() {
		if rhsstr, ok := rhsval.(string); ok {
			if rhsf, err := strconv.ParseFloat(rhsstr, 64); err == nil {
				// rhsval can be converted to a float!
				rhsval = rhsf
			}
		}
	}

	/*
		"nested": {
			"filter": {
			    "term": {
			        "map_actioncounts.k": "Web hit"
			    }
			},
			"path": "map_actioncounts"
		}

		"nested": {
			"filter": {
			    "and": [
			        {
			            "term": {
			                "mapvals_fields.k": "has_data"
			            }
			        },
			        {
			            "term": {
			                "mapvals_fields.b": true
			            }
			        }
			    ]
			},
			"path": "mapvals_fields"
		}
		"nested": {
			"filter": {
			    "and": [
			        {
			            "term": {
			                "k": "open"
			            }
			        },
			        {
			            "range": {
			                "f": {"gte": 7}
			            }
			        }
			    ]
			},
			"path": "map_events"
		}
		q = esMap{"nested": esMap{"path": parent, "filter": esMap{"and": []esMap{
					{"term": esMap{parent + ".k": child}},
					{"range": esMap{parent + valuePath: esMap{esRangeOps[seg.SegType]: rhsNum}}},
				}}}}
	*/

	fieldName := lhs.Field
	if lhs.Nested() {
		fieldName, rhsval = lhs.PrefixAndValue(rhsval)
	}
	r := &RangeFilter{}
	switch op {
	case lex.TokenGE:
		r.Range = map[string]RangeQry{fieldName: RangeQry{GTE: rhsval}}
	case lex.TokenLE:
		r.Range = map[string]RangeQry{fieldName: RangeQry{LTE: rhsval}}
	case lex.TokenGT:
		r.Range = map[string]RangeQry{fieldName: RangeQry{GT: rhsval}}
	case lex.TokenLT:
		r.Range = map[string]RangeQry{fieldName: RangeQry{LT: rhsval}}
	default:
		return nil, fmt.Errorf("qlindex: unsupported range operator %s", op)
	}
	if lhs.Nested() {
		return Nested(lhs, r), nil
	}
	return r, nil
}

// makeBetween returns a range filter for Elasticsearch given the 3 nodes that
// make up a comparison.
func makeBetween(lhs *gentypes.FieldType, lower, upper interface{}) (interface{}, error) {
	/*
		"nested": {
			"filter": {
			    "and": [
			        {
			            "term": {
			                "k": "open"
			            }
			        },
			        {
			            "range": {
			                "f": {"gt": 7}
			            }
			        },
			        {
			            "range": {
			                "f": {"lt": 15}
			            }
			        }
			    ]
			},
			"path": "map_events"
		}

		"and": [
		    {
		        "range": {
		            "f": {"gt": 7}
		        }
		    },
		    {
		        "range": {
		            "f": {"lt": 15}
		        }
		    }
		]
	*/

	lr := &RangeFilter{Range: map[string]RangeQry{lhs.Field: RangeQry{GT: lower}}}
	ur := &RangeFilter{Range: map[string]RangeQry{lhs.Field: RangeQry{LT: upper}}}
	fl := []interface{}{lr, ur}

	if lhs.Nested() {
		fl = append(fl, Term("k", lhs.Field))
		return &nested{&NestedFilter{
			Filter: &and{fl},
			Path:   lhs.Path,
		}}, nil
	}
	return &and{fl}, nil
}

// makeWildcard returns a wildcard/like query
//  {"query": {"wildcard": {field: value}}}
func makeWildcard(lhs *gentypes.FieldType, value string) (interface{}, error) {
	/*
		"nested": {
			"filter": {
			    "and": [
			        {
			            "term": { "map_events.k": "open" }
			        },
			        { "wildcard": {"map_events.v": "hel"}
			        }
			    ]
			},
			"path": "map_events"
		}

		{"query": {"wildcard": {field: value}}}
	*/
	fieldName := lhs.Field

	if lhs.Nested() {
		fieldName = lhs.PathAndPrefix(value)
	}
	wc := Wildcard(fieldName, value)
	if lhs.Nested() {
		fl := []interface{}{wc, Term(fmt.Sprintf("%s.k", lhs.Path), lhs.Field)}
		return &nested{&NestedFilter{
			Filter: &and{fl},
			Path:   lhs.Path,
		}}, nil
	}
	return &wc, nil
}

// esName return the Elasticsearch field name for an identity node or an error.
func esName(m gentypes.FieldMapper, n expr.Node) (*gentypes.FieldType, error) {

	ident, ok := n.(*expr.IdentityNode)
	if !ok {
		return nil, fmt.Errorf("qlindex: expected an identity but found %T (%s)", n, n)
	}

	// This shotgun approach sucks, see https://github.com/lytics/lio/issues/7565
	ft, ok := m.Map(ident.Text)
	if ok {
		return ft, nil
	}

	//left, right, _ := expr.LeftRight(ident.Text)
	//u.Debugf("left:%q right:%q isNamespaced?%v   key=%v", left, right, ident.HasLeftRight(), ident.OriginalText())
	if ident.HasLeftRight() {
		ft, ok := m.Map(ident.OriginalText())
		if ok {
			return ft, nil
		}
	}

	// This is legacy crap, we stupidly used to allow this:
	//  ticket to remove https://github.com/lytics/lio/issues/7565
	//
	//   `key_name.field value` -> "key_name", "field value"
	//
	// check if key is left.right
	parts := strings.SplitN(ident.Text, ".", 2)
	if len(parts) == 2 {
		// Nested field lookup
		ft, ok = m.Map(parts[0])
		if ok {
			return ft, nil
		}
	}

	return nil, gentypes.MissingField(ident.OriginalText())
}

// makeTimeWindowQuery maps the provided threshold and window arguments to the indexed time buckets
func makeTimeWindowQuery(lhs *gentypes.FieldType, threshold, window, ts int64) (interface{}, error) {
	/*
		"nested": {
			"filter": {
				"and": [
					{
						"term": { "timebucket_visits.threshold": 1 }
					},
					{
						"term": { "timebucket_visits.window": 3 }
					},
					{
						"range": {
							"timebucket_visits.enter: { "lte": 16916 }
						}
					},
					{
						"range": {
							"timebucket_visits.exit: { "gte": 16916 }
						}
					},
				]
			}
			"path": "timebucket_visits"
		}
	*/

	fl := []interface{}{
		Term(lhs.Field+".threshold", strconv.FormatInt(threshold, 10)),
		Term(lhs.Field+".window", strconv.FormatInt(window, 10)),
		&RangeFilter{Range: map[string]RangeQry{lhs.Field + ".enter": RangeQry{LTE: ts}}},
		&RangeFilter{Range: map[string]RangeQry{lhs.Field + ".exit": RangeQry{GTE: ts}}},
	}

	return &nested{&NestedFilter{
		Filter: &and{fl},
		Path:   lhs.Field,
	}}, nil
}
