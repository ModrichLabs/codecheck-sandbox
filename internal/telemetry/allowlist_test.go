package telemetry

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// planted holds values that must never reach PostHog (spec §11, "Never sent").
var planted = []string{
	"sk-ant-api03-PLANTEDKEYPLANTEDKEY",
	"/home/alice/secret-project/main.go",
	"https://github.com/acme/private-repo.git",
	"alice-laptop.corp.example",
	"alice",
	"10.20.30.40",
	"Error: 401 invalid x-api-key",
}

var valueType = reflect.TypeFor[value]()

// allowedField reports whether an event field type may exist at all.
func allowedField(t reflect.Type) bool {
	if t.Kind() == reflect.Bool || t.Implements(valueType) {
		return true
	}
	// Exception stacks: a slice of a struct whose own fields obey the rule.
	if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Struct {
		for i := range t.Elem().NumField() {
			if !allowedField(t.Elem().Field(i).Type) {
				return false
			}
		}
		return true
	}
	return false
}

func TestEveryEventFieldHasAnAllowedType(t *testing.T) {
	for _, e := range AllEvents() {
		typ := reflect.TypeOf(e)
		for i := range typ.NumField() {
			f := typ.Field(i)
			if !allowedField(f.Type) {
				t.Errorf("%s.%s has type %s: event fields must hold a bool or an enum, band or version type", typ.Name(), f.Name, f.Type)
			}
			if f.Type.Kind() == reflect.String && !f.Type.Implements(valueType) {
				t.Errorf("%s.%s holds a plain string", typ.Name(), f.Name)
			}
		}
	}
}

// fill sets every string-kind field (and every string field inside slices of
// structs) of a fresh copy of e to s, and every bool to true.
func fill(e Event, s string) Event {
	v := reflect.New(reflect.TypeOf(e)).Elem()
	fillValue(v, s)
	return v.Interface().(Event)
}

func fillValue(v reflect.Value, s string) {
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Struct:
		for i := range v.NumField() {
			fillValue(v.Field(i), s)
		}
	case reflect.Slice:
		el := reflect.New(v.Type().Elem()).Elem()
		fillValue(el, s)
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 3), el, el, el))
	}
}

func TestNoPlantedValueEverLeaves(t *testing.T) {
	// The surrounding context: a step error, config values and environment
	// details that a careless caller might convert into event fields.
	ctxErr := fmt.Errorf("step nft-rules: writing %s for %s on %s: %s",
		planted[1], planted[2], planted[3], planted[0])
	candidates := append([]string{ctxErr.Error()}, planted...)

	for _, e := range AllEvents() {
		for _, s := range candidates {
			ev := fill(e, s)
			props, err := Properties(ev)
			if err != nil {
				t.Fatalf("Properties(%T): %v", ev, err)
			}
			out, _ := json.Marshal(props)
			for _, p := range planted {
				if strings.Contains(string(out), p) {
					t.Errorf("%s leaked %q:\n%s", e.EventName(), p, out)
				}
			}
		}
	}
}
