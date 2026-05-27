// bundlespec resolves all $ref pointers in an OpenAPI spec and writes a single
// self-contained YAML file. Used at build time to produce the embedded spec
// served by the Scalar API docs page.
//
// Usage: go run ./cmd/bundlespec -in api/spec/openapi.yaml -out internal/server/docs_spec.yaml
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"
)

func main() {
	in := flag.String("in", "api/spec/openapi.yaml", "input OpenAPI spec")
	out := flag.String("out", "internal/server/docs_spec.yaml", "output bundled YAML")
	flag.Parse()

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true

	doc, err := loader.LoadFromFile(*in)
	if err != nil {
		log.Fatalf("load: %v", err)
	}

	if err := doc.Validate(context.Background()); err != nil {
		log.Fatalf("validate: %v", err)
	}

	node, err := doc.MarshalYAML()
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}

	data, err := yaml.Marshal(node)
	if err != nil {
		log.Fatalf("encode: %v", err)
	}

	if err := os.WriteFile(*out, data, 0o644); err != nil {
		log.Fatalf("write: %v", err)
	}

	fmt.Printf("bundled %s → %s\n", *in, *out)
}
