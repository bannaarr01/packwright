// Package hints supplies the non-AI field-hint catalogue and resolution rule
// defined by ADR-0051. The Field struct gains an optional `placeholder` key;
// when an author leaves it blank, Resolve falls back to the type-default
// table in Catalogue. The lookup is intentionally pure and offline — no LLM,
// no live AWS calls — so it works regardless of the user's AI / picker setup.
//
// Render-side wiring (TUI textinput Placeholder, GUI <input placeholder=...>)
// lives behind the form engine and is out of scope for this package: hints
// only exposes the data model and the resolver. Importers downstream of the
// canonical manifest package call Resolve once per field at render time.
package hints

// Catalogue maps a manifest field `type:` string to its default placeholder.
//
// Keys are the YAML type strings as written by manifest authors, not the
// FieldType Go constants, because the catalogue is consulted by string lookup
// from the resolver and from the scaffolder template. Entries deliberately
// cover types not yet declared by manifest.FieldType (e.g. "aws/region",
// "cidr"): authors can reference these in custom manifests, and the catalogue
// supplies the example even before a FieldType constant lands.
//
// Empty strings on the generic types ("string", "int", "bool", "enum") are
// not bugs — over-hinting on generic widgets was rated worse than under-hinting
// in ADR-0051's alternatives discussion. Enum widgets surface their `values:`
// list as the hint instead.
var Catalogue = map[string]string{
	"aws/vpc-id":        "vpc-0abc1234567890abcdef",
	"aws/subnet-id":     "subnet-0abc1234567890abcd",
	"aws/subnet-ids":    "subnet-aaaa,subnet-bbbb,subnet-cccc",
	"aws/sg-id":         "sg-0abc1234567890abcd",
	"aws/acm-arn":       "arn:aws:acm:eu-west-1:123456789012:certificate/…",
	"aws/region":        "eu-west-1",
	"aws/account-id":    "123456789012",
	"aws/instance-type": "t3.small",
	"cidr":              "10.0.0.0/16",
	"domain":            "api.example.com",
	"stack-name":        "alb-dev-stack",
	"string":            "",
	"int":               "",
	"bool":              "",
	"enum":              "",
}

// Lookup returns the catalogue's default placeholder for the given manifest
// type string, or "" if the type is not in the table. Returning "" for an
// unknown type matches the precedence rule in Resolve: an empty hint means
// "no hint shown".
func Lookup(fieldType string) string {
	return Catalogue[fieldType]
}
