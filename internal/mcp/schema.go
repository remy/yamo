package mcp

// JSON Schema, written by hand.
//
// The schemas are the tool descriptions as far as a model is concerned: it
// picks a tool by its name and prose and fills it in by its property
// descriptions, so these are written for a reader rather than derived from a
// Go type. The helpers exist only to keep that prose visible above the
// punctuation it would otherwise be buried in.

// props is a schema's property map.
type props map[string]any

// schema builds an object schema. Unknown properties are refused so that a
// misspelled argument is reported rather than dropped — the same reason the
// HTTP handlers decode with DisallowUnknownFields.
func schema(p props, required ...string) map[string]any {
	s := map[string]any{
		"type":                 "object",
		"properties":           map[string]any(p),
		"additionalProperties": false,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func flag(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func strList(desc string) map[string]any {
	return map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "string"},
		"description": desc,
	}
}

func enum(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": desc}
}

// mapOfNullableStrings is the shape of an edit: field name to value, where
// null means "clear this field" rather than "leave it alone".
func mapOfNullableStrings(desc string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          desc,
		"additionalProperties": map[string]any{"type": []string{"string", "null"}},
	}
}

// merge combines property maps, later keys winning.
func merge(maps ...props) props {
	out := props{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// selectorProps is the selection every writing tool takes, described in terms
// of what that particular tool is about to do to it.
//
// The wording matters more here than anywhere else in these schemas. "all" is
// the difference between correcting four tracks and rewriting a hundred
// thousand, and "expectCount" is the only thing standing between a model whose
// count is stale and a library that quietly agrees with it.
func selectorProps(verb string) props {
	return props{
		"query": str("Search expression naming the tracks to " + verb +
			". The same language search_tracks takes."),
		"ids": strList("Specific track ids. When given, query is ignored."),
		"excludeIds": strList("Track ids to drop from a query selection — " +
			"\"all of these except these three\"."),
		"all": flag("Select the entire library. Must be set explicitly, so that an " +
			"empty selection can never be mistaken for everything."),
		"expectCount": integer("The number of tracks you expect to " + verb +
			". The server refuses if the selection has moved since you counted it, " +
			"which is what stops a stale count being applied to a different set. " +
			"Send it whenever you have quoted a number to the user."),
	}
}
