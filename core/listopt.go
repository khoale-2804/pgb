package core

// ListOpt carries the per-call list modifiers for generated List statics.
// The zero value applies nothing; later options win.
type ListOpt struct {
	Limit  int
	Offset int
}

// ApplyList applies the option to the select (Limit/Offset are emitted only
// when positive).
func (s *Select) ApplyList(opts ...ListOpt) *Select {
	for _, o := range opts {
		if o.Limit > 0 {
			s = s.Limit(o.Limit)
		}
		if o.Offset > 0 {
			s = s.Offset(o.Offset)
		}
	}
	return s
}
