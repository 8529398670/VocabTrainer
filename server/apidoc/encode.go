package apidoc

import (
	bytes "bytes"
	json "encoding/json"
)

// Encode is how every generated JSON file is written: keys sorted, two-space
// indent, no HTML escaping, and a newline at the end. The Python template
// writes the same bytes.
func Encode( document any ) ( raw []byte , err error ) {
	buffer := &bytes.Buffer{}
	encoder := json.NewEncoder( buffer )
	encoder.SetEscapeHTML( false )
	encoder.SetIndent( "" , "  " )
	err = encoder.Encode( document )
	raw = buffer.Bytes()
	return
}

// Plain is a document as encoding/json reads it back: maps, []any, strings,
// float64 numbers and bools. Markdown works on this form.
func Plain( document any ) ( plain map[string]any , err error ) {
	raw , err := json.Marshal( document )
	if err != nil { return }
	err = json.Unmarshal( raw , &plain )
	return
}
