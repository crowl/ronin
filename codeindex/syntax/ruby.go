package syntax

import (
	"slices"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
)

// Ruby macros are syntactic declarations, not resolved Rails/Sorbet APIs. Keep
// their own kinds and source spans rather than inventing generated methods.
func rubyDeclarations(node, name *ts.Node, kind string, source []byte) []Declaration {
	if rubyInsideMethod(node) {
		return nil
	}
	if kind == "dsl" {
		return rubyDSL(node, source)
	}
	d := Declaration{
		Name: bounded(name.Utf8Text(source)), Kind: kind,
		Container: bounded(rubyContainer(node, source)),
		Signature: rubySignature(node, source), Span: span(node),
	}
	if kind == "method" {
		// Comments may separate a sig from its method, but intervening Ruby
		// statements must not. Visibility wrappers such as private def are
		// part of the method declaration for this purpose.
		statement := node
		if parent := node.Parent(); parent != nil && parent.Kind() == "argument_list" {
			if call := parent.Parent(); rubyCall(call, source) == "private" || rubyCall(call, source) == "protected" || rubyCall(call, source) == "public" {
				statement = call
			}
		}
		previous := statement.PrevNamedSibling()
		for previous != nil && previous.Kind() == "comment" {
			previous = previous.PrevNamedSibling()
		}
		if rubyCall(previous, source) == "sig" && rubySelfCall(previous, source) && previous.ChildByFieldName("block") != nil {
			// Put the method first so bounded tool rendering retains its name
			// and parameters even for long Sorbet signatures.
			d.Signature = bounded(d.Signature + "\n" + previous.Utf8Text(source))
			d.Span.StartByte = previous.StartByte()
			d.Span.StartLine = previous.StartPosition().Row + 1
		}
	}
	return []Declaration{d}
}

func rubyInsideMethod(node *ts.Node) bool {
	for p := node.Parent(); p != nil; p = p.Parent() {
		if p.Kind() == "method" || p.Kind() == "singleton_method" || p.Kind() == "lambda" {
			return true
		}
	}
	return false
}

func rubyContainer(node *ts.Node, source []byte) string {
	var parts []string
	for p := node.Parent(); p != nil; p = p.Parent() {
		switch p.Kind() {
		case "class", "module":
			if name := p.ChildByFieldName("name"); name != nil {
				parts = append(parts, name.Utf8Text(source))
			}
		case "singleton_class":
			if value := p.ChildByFieldName("value"); value != nil {
				parts = append(parts, "<singleton:"+value.Utf8Text(source)+">")
			}
		}
	}
	slices.Reverse(parts)
	owner := strings.Join(parts, "::")
	if object := node.ChildByFieldName("object"); object != nil {
		if owner != "" {
			owner += "."
		}
		owner += object.Utf8Text(source)
	}
	return owner
}

func rubySignature(node *ts.Node, source []byte) string {
	// Headers come from explicit syntax fields, so empty and endless method
	// bodies cannot accidentally leak into signatures.
	var last *ts.Node
	switch node.Kind() {
	case "method", "singleton_method":
		last = node.ChildByFieldName("parameters")
		if last == nil {
			last = node.ChildByFieldName("name")
		}
	case "class":
		last = node.ChildByFieldName("superclass")
		if last == nil {
			last = node.ChildByFieldName("name")
		}
	case "module":
		last = node.ChildByFieldName("name")
	}
	if last != nil {
		return bounded(strings.TrimSpace(string(source[node.StartByte():last.EndByte()])))
	}
	if block := node.ChildByFieldName("block"); block != nil {
		return bounded(strings.TrimSpace(string(source[node.StartByte():block.StartByte()])))
	}
	return bounded(node.Utf8Text(source))
}

func rubyCall(node *ts.Node, source []byte) string {
	if node != nil && node.Kind() == "call" {
		if method := node.ChildByFieldName("method"); method != nil {
			return method.Utf8Text(source)
		}
	}
	return ""
}

func rubySelfCall(node *ts.Node, source []byte) bool {
	receiver := node.ChildByFieldName("receiver")
	return receiver == nil || receiver.Utf8Text(source) == "self"
}

// Only unescaped, non-interpolated literal names are modeled. Dynamic names
// remain searchable source; evaluating or guessing them would be misleading.
func rubyLiteral(node *ts.Node, source []byte) string {
	if node == nil {
		return ""
	}
	switch node.Kind() {
	case "simple_symbol":
		return strings.TrimPrefix(node.Utf8Text(source), ":")
	case "hash_key_symbol":
		return node.Utf8Text(source)
	case "string", "delimited_symbol":
		var text strings.Builder
		for i := uint(0); i < node.NamedChildCount(); i++ {
			child := node.NamedChild(i)
			if child.Kind() != "string_content" {
				return ""
			}
			text.WriteString(child.Utf8Text(source))
		}
		return text.String()
	}
	return ""
}

func rubyArguments(node *ts.Node) []*ts.Node {
	var result []*ts.Node
	if args := node.ChildByFieldName("arguments"); args != nil {
		for i := uint(0); i < args.NamedChildCount(); i++ {
			child := args.NamedChild(i)
			if child.Kind() != "comment" {
				result = append(result, child)
			}
		}
	}
	return result
}

func rubyOption(args []*ts.Node, key string, source []byte) string {
	for _, arg := range args {
		if arg.Kind() == "pair" && rubyLiteral(arg.ChildByFieldName("key"), source) == key {
			return rubyLiteral(arg.ChildByFieldName("value"), source)
		}
	}
	return ""
}

func rubyDSL(node *ts.Node, source []byte) []Declaration {
	if !rubySelfCall(node, source) {
		return nil
	}
	macro := rubyCall(node, source)
	args := rubyArguments(node)
	var names []string
	var kind string
	owner := rubyContainer(node, source)
	if routeOwner, ok := rubyRouteContainer(node, source); ok {
		kind = "route"
		owner = routeOwner
		switch macro {
		case "resources", "resource", "namespace", "scope", "concern", "concerns":
			for _, arg := range args {
				if name := rubyLiteral(arg, source); name != "" {
					names = append(names, name)
				}
			}
		case "get", "post", "put", "patch", "delete", "head", "options", "match", "root", "mount":
			name := rubyOption(args, "as", source)
			if name == "" && len(args) > 0 {
				name = rubyLiteral(args[0], source)
				// Legacy route syntax: get "login" => "sessions#new".
				if name == "" && args[0].Kind() == "pair" && rubyOption(args, "to", source) == "" {
					key := args[0].ChildByFieldName("key")
					if key != nil && key.Kind() == "string" {
						name = rubyLiteral(key, source)
					}
				}
			}
			if macro == "root" && rubyOption(args, "as", source) == "" {
				name = "root"
			}
			if name != "" {
				names = append(names, name)
			}
		default:
			return nil
		}
	} else {
		if !rubyMacroContext(node, source) {
			return nil
		}
		switch macro {
		case "attr_reader", "attr_writer", "attr_accessor":
			kind = "attribute"
		case "belongs_to", "has_one", "has_many", "has_and_belongs_to_many":
			kind = "association"
		case "scope":
			kind = "scope"
		case "before_validation", "after_validation", "before_save", "around_save", "after_save",
			"before_create", "around_create", "after_create", "before_update", "around_update", "after_update",
			"before_destroy", "around_destroy", "after_destroy", "after_commit", "after_rollback",
			"after_initialize", "after_find", "after_touch", "after_create_commit", "after_update_commit",
			"after_destroy_commit", "after_save_commit", "before_action", "after_action", "around_action",
			"prepend_before_action", "prepend_after_action", "prepend_around_action",
			"before_perform", "after_perform", "around_perform", "before_enqueue", "after_enqueue", "around_enqueue":
			kind = "callback"
		default:
			return nil
		}
		for _, arg := range args {
			if name := rubyLiteral(arg, source); name != "" {
				names = append(names, name)
			}
			if kind == "association" || kind == "scope" {
				break
			}
		}
	}
	var declarations []Declaration
	for _, name := range names {
		declarations = append(declarations, Declaration{
			Name: bounded(name), Kind: kind, Container: bounded(owner),
			Signature: rubySignature(node, source), Span: span(node),
		})
	}
	return declarations
}

// Macros in class/module bodies and ActiveSupport::Concern blocks are useful
// declarations. Calls in arbitrary blocks or nested expressions are not.
func rubyMacroContext(node *ts.Node, source []byte) bool {
	for p := node.Parent(); p != nil; p = p.Parent() {
		switch p.Kind() {
		case "class", "module", "singleton_class":
			return true
		case "call":
			if !rubySelfCall(p, source) {
				return false
			}
			switch rubyCall(p, source) {
			case "included", "class_methods":
			default:
				return false
			}
		case "argument_list", "assignment", "pair", "lambda":
			return false
		}
	}
	return false
}

// Route context is established by a routes.draw block, not merely by a method
// name like get or scope. This avoids classifying ordinary Ruby calls as routes.
func rubyRouteContainer(node *ts.Node, source []byte) (string, bool) {
	var parts []string
	for p := node.Parent(); p != nil; p = p.Parent() {
		switch p.Kind() {
		case "class", "module", "method", "singleton_method", "lambda", "argument_list":
			return "", false
		case "call":
			macro := rubyCall(p, source)
			if macro == "draw" && p.ChildByFieldName("block") != nil {
				receiver := p.ChildByFieldName("receiver")
				if rubyCall(receiver, source) == "routes" {
					slices.Reverse(parts)
					return strings.Join(parts, "/"), true
				}
				return "", false
			}
			if !rubySelfCall(p, source) {
				return "", false
			}
			switch macro {
			case "namespace", "resources", "resource", "scope":
				args := rubyArguments(p)
				name := ""
				if len(args) > 0 {
					name = rubyLiteral(args[0], source)
				}
				if name == "" {
					name = rubyOption(args, "module", source)
				}
				if name == "" {
					name = rubyOption(args, "path", source)
				}
				if name != "" {
					parts = append(parts, name)
				}
			case "member", "collection":
				parts = append(parts, macro)
			case "concern":
				name := "concern"
				if args := rubyArguments(p); len(args) > 0 {
					if literal := rubyLiteral(args[0], source); literal != "" {
						name += ":" + literal
					}
				}
				parts = append(parts, name)
			case "constraints", "defaults", "controller", "authenticate", "authenticated", "unauthenticated":
			default:
				return "", false
			}
		}
	}
	return "", false
}
