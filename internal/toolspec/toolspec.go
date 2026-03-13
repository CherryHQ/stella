package toolspec

// Definition describes a callable tool exposed to a model.
type Definition struct {
	Name        string
	Description string
	InputSchema map[string]any
}
