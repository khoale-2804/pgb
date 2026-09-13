package core

// ListOpt carries the per-call list modifiers for generated List statics.
// The zero value applies nothing; later options win.
type ListOpt struct {
	Limit  int
	Offset int
	// Order appends explicit ORDER BY terms to the generated select. Without
	// it a List without LIMIT has no deterministic row order — and keyset
	// pagination on unordered results is unsound — so flows that care should
	// order on a unique column (ascending id, typically).
	Order []Order
}

// ApplyList applies the option to the select (Limit/Offset are emitted only
// when positive; Order terms are appended after any caller-built ordering).
func (s *Select) ApplyList(opts ...ListOpt) *Select {
	for _, o := range opts {
		if len(o.Order) > 0 {
			s = s.OrderBy(o.Order...)
		}
		if o.Limit > 0 {
			s = s.Limit(o.Limit)
		}
		if o.Offset > 0 {
			s = s.Offset(o.Offset)
		}
	}
	return s
}
