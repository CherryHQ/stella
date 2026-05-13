package resources

// Kind identifies a builtin resource category.
type Kind string

const (
	KindSkill    Kind = "skill"
	KindSoul     Kind = "soul"
	KindSubAgent Kind = "subagent"
	KindTemplate Kind = "template"
)

// AllKinds lists every supported kind.
func AllKinds() []Kind {
	return []Kind{KindSkill, KindSoul, KindSubAgent, KindTemplate}
}

// subdir maps a Kind to its embedded FS subdirectory.
func (k Kind) subdir() string {
	switch k {
	case KindSkill:
		return "skills"
	case KindSoul:
		return "souls"
	case KindSubAgent:
		return "subagents"
	case KindTemplate:
		return "templates"
	default:
		return ""
	}
}

// Resource is one builtin entry of a given Kind.
// Content is the markdown body with frontmatter stripped.
// Metadata holds kind-specific frontmatter fields (anything beyond name/description/tags).
type Resource struct {
	Kind        Kind
	ID          string
	Name        string
	Description string
	Tags        []string
	Metadata    map[string]any
	Content     string
	Hash        string
}
