package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// CompileToolContract 复用标准 JSON Schema 实现；不允许 schema 通过远端 $ref 进行网络访问。
func CompileToolContract(t Tool) (*jsonschema.Schema, error) {
	if t.Contract.ResourceFencing && t.ResolveResources == nil {
		return nil, fmt.Errorf("%w: missing resource provider", ErrTaskContract)
	}
	if t.Contract.OutputSchema == nil {
		return nil, nil
	}
	raw, err := json.Marshal(t.Contract.OutputSchema)
	if err != nil {
		return nil, ErrTaskContract
	}
	schema, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		return nil, ErrTaskContract
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(localSchemaOnly{})
	const location = "urn:ihtml:tool-output"
	if err = compiler.AddResource(location, schema); err != nil {
		return nil, ErrTaskContract
	}
	resolved, err := compiler.Compile(location)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid output schema", ErrTaskContract)
	}
	return resolved, nil
}

// localSchemaOnly 禁止通过 schema 引用读取宿主文件或网络；内联 $defs 仍由标准编译器解析。
type localSchemaOnly struct{}

func (localSchemaOnly) Load(string) (any, error) { return nil, ErrTaskContract }

func validateToolOutput(schema *jsonschema.Schema, text string) error {
	if schema == nil {
		return nil
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrTaskContract
	}
	if schema.Validate(value) != nil {
		return ErrTaskContract
	}
	return nil
}
