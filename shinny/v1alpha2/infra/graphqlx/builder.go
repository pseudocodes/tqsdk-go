package graphqlx

import (
	"strconv"
	"strings"
)

type Arg struct {
	name  string
	value string
	valid bool
}

func EnumList(name string, values []string) Arg {
	tokens := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.ToUpper(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		tokens = append(tokens, v)
	}
	if len(tokens) == 0 {
		return Arg{}
	}
	return Arg{name: strings.TrimSpace(name), value: "[" + strings.Join(tokens, ",") + "]", valid: true}
}

func StringList(name string, values []string) Arg {
	tokens := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		tokens = append(tokens, strconv.Quote(v))
	}
	if len(tokens) == 0 {
		return Arg{}
	}
	return Arg{name: strings.TrimSpace(name), value: "[" + strings.Join(tokens, ",") + "]", valid: true}
}

func OptBool(name string, v *bool) Arg {
	if v == nil {
		return Arg{}
	}
	return Arg{name: strings.TrimSpace(name), value: strconv.FormatBool(*v), valid: true}
}

func Field(name string, args ...Arg) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	parts := make([]string, 0, len(args))
	for _, a := range args {
		if !a.valid || strings.TrimSpace(a.name) == "" {
			continue
		}
		parts = append(parts, a.name+": "+a.value)
	}
	if len(parts) == 0 {
		return name
	}
	return name + "(" + strings.Join(parts, ", ") + ")"
}

func QueryField(field string, selection string) string {
	field = strings.TrimSpace(field)
	selection = strings.TrimSpace(selection)
	if field == "" {
		return "query { }"
	}
	if selection == "" {
		return "query { " + field + " }"
	}
	return "query { " + field + " { " + selection + " } }"
}
