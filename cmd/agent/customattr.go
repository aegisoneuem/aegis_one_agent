package main

import (
	"os"
	"strconv"
)

// Custom-attribute engine — the BigFix "retrieved property" / Tanium "sensor"
// equivalent. The server defines named properties with a DECLARATIVE provider +
// args; each agent evaluates them locally and streams a name→value map with its
// inventory. One unlimited map field, no per-attribute proto change, no CMDB.
//
// SECURITY: every provider is READ-ONLY and declarative — never arbitrary code —
// so a definition cannot execute anything on the endpoint. Definitions are also
// delivered SIGNED (Ed25519), like policy/commands (wiring in the next step).

const maxAttrValueLen = 2048 // cap a reported value to keep the stream bounded

// attrDefinition is one server-defined custom property.
type attrDefinition struct {
	Name     string            `json:"name"`
	Provider string            `json:"provider"` // env | file | registry | admin_tag
	Args     map[string]string `json:"args"`
}

// evalAttribute evaluates one definition to a string value. ok=false means an
// unknown/unsupported provider or a failed lookup — the caller omits the property
// rather than reporting a blank.
func evalAttribute(def attrDefinition) (string, bool) {
	var (
		v  string
		ok bool
	)
	switch def.Provider {
	case "env":
		v, ok = evalEnvAttr(def.Args["name"])
	case "file":
		v, ok = evalFileAttr(def.Args["path"], def.Args["attr"])
	case "registry":
		v, ok = evalRegistryAttr(def.Args["root"], def.Args["key"], def.Args["name"])
	case "admin_tag":
		v, ok = evalAdminTag(def.Args["name"])
	default:
		return "", false // unknown provider — refuse (never guess/exec)
	}
	if ok && len(v) > maxAttrValueLen {
		v = v[:maxAttrValueLen]
	}
	return v, ok
}

func evalEnvAttr(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	return os.LookupEnv(name)
}

func evalFileAttr(path, attr string) (string, bool) {
	if path == "" {
		return "", false
	}
	switch attr {
	case "", "exists":
		if _, err := os.Stat(path); err == nil {
			return "true", true
		}
		return "false", true // a definite "not present" is a valid answer
	case "size":
		fi, err := os.Stat(path)
		if err != nil {
			return "", false
		}
		return strconv.FormatInt(fi.Size(), 10), true
	case "version":
		return evalFileVersion(path) // platform-specific (Windows)
	default:
		return "", false
	}
}

// evalDefinitions evaluates a batch into a name→value map, skipping failures and
// unnamed definitions. Later duplicate names overwrite earlier ones.
func evalDefinitions(defs []attrDefinition) map[string]string {
	out := make(map[string]string, len(defs))
	for _, d := range defs {
		if d.Name == "" {
			continue
		}
		if v, ok := evalAttribute(d); ok {
			out[d.Name] = v
		}
	}
	return out
}
