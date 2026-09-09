// Package tools holds the code generators run by mise tasks, not anything the
// server links. It has no code of its own; each generator is a main package:
// toolgen (model-facing tool sources from api/spec), syncmodelcatalog,
// and syncembeddedbinaries. The builtin manifest composition root lives in
// cmd/generatebuiltinmanifest because it wires replaceable plugins together.
package tools
