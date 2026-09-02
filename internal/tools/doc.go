// Package tools holds the code generators run by mise tasks, not anything the
// server links. It has no code of its own; each generator is a main package:
// toolgen (model-facing tool sources from api/spec), syncmodelcatalog,
// syncembeddedbinaries, and generatebuiltinmanifest.
package tools
