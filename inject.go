// Package inject provides utilities for mapping and injecting dependencies in various ways.
package inject

import (
	"fmt"
	"reflect"
)

// Injector represents an interface for mapping and injecting dependencies into structs
// and function arguments.
type Injector interface {
	Applicator
	Invoker
	TypeMapper
	// SetParent sets the parent of the injector. If the injector cannot find a
	// dependency in its Type map it will check its parent before returning an
	// error.
	SetParent(Injector)
}

// Applicator represents an interface for mapping dependencies to a struct.
type Applicator interface {
	// Maps dependencies in the Type map to each field in the struct
	// that is tagged with 'inject'. Returns an error if the injection
	// fails.
	Apply(interface{}) error
}

// Invoker represents an interface for calling functions via reflection.
type Invoker interface {
	// Invoke attempts to call the interface{} provided as a function,
	// providing dependencies for function arguments based on Type. Returns
	// a slice of reflect.Value representing the returned values of the function.
	// Returns an error if the injection fails.
	Invoke(interface{}) ([]reflect.Value, error)
}

// TypeMapper represents an interface for mapping interface{} values based on type.
type TypeMapper interface {
	// Maps the interface{} value based on its immediate type from reflect.TypeOf.
	Map(interface{}) TypeMapper
	// Maps the interface{} value based on the pointer of an Interface provided.
	// This is really only useful for mapping a value as an interface, as interfaces
	// cannot at this time be referenced directly without a pointer.
	MapTo(interface{}, interface{}) TypeMapper
	// Maps the interface{} function as a provider of its return types. This
	// includes error returns, so the provider must either not fail or panic on
	// error. The provider will be invoked by Invoker.Invoke, so it may have
	// any number of arguments of injectable types. Injector failure will
	// result in panic.
	//
	// Values returned by the provider will not be cached: the provider will be
	// called every time a value of one of its provided types is required. This
	// is to enable predictable variable output based on other mapped values
	// (e.g., a service provider function which injects the context of the
	// current request into the service).
	//
	// You must be careful to not construct circular dependencies when defining
	// providers. For example, a provider of type A which takes an argument of
	// type B, and a provider of type B which takes an argument of type A.
	// Attempting to retrieve either type A or B from the mapper will result in
	// an infinite loop.
	MapProvider(interface{}) TypeMapper
	// Provides a possibility to directly insert a mapping based on type and value.
	// This makes it possible to directly map type arguments not possible to instantiate
	// with reflect like unidirectional channels.
	Set(reflect.Type, reflect.Value) TypeMapper
	// Returns the Value that is mapped to the current type. Returns a zeroed Value if
	// the Type has not been mapped.
	//
	// The options permit implementation-specific variations in value retrieval
	// behaviour. These options may not necessarily be user-facing, and the
	// function may panic if provided unrecognized/inappropriate options.
	Get(t reflect.Type, options ...interface{}) reflect.Value
}

// mappedValue is a value which can be injected via TypeMapper.Get. It may be a
// static value provided at mapping time, or a dynamic one computed at
// retrieval time, possibly requiring the injection of other mapped values.
type mappedValue interface {
	// Get is called by TypeMapper.Get to retrieve a mapped value for a type.
	Get(injector Injector) reflect.Value
}

// literalValue is a reflected value.
type literalValue reflect.Value

// Get the literal reflected value. The argument is unused.
func (v literalValue) Get(Injector) reflect.Value {
	return reflect.Value(v)
}

// providedValue is a value dynamically retrieved from a provider function.
type providedValue struct {
	provider interface{}
	outIndex int
}

// Get the value dynamically from the provider function.
func (v providedValue) Get(i Injector) reflect.Value {
	values, err := i.Invoke(v.provider)
	if err != nil {
		panic(err)
	}
	// The index of the type-appropriate return has been populated by
	// TypeMapper.MapProvider, and interface-appropriateness checking has been
	// done by TypeMapper.Get. We can just blindly return the right value.
	return values[v.outIndex]
}

type injector struct {
	values map[reflect.Type]mappedValue
	parent Injector
}

// InterfaceOf dereferences a pointer to an Interface type.
// It panics if value is not an pointer to an interface.
func InterfaceOf(value interface{}) reflect.Type {
	t := reflect.TypeOf(value)

	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if t.Kind() != reflect.Interface {
		panic("Called inject.InterfaceOf with a value that is not a pointer to an interface. (*MyInterface)(nil)")
	}

	return t
}

// New returns a new Injector.
func New() Injector {
	return &injector{
		values: make(map[reflect.Type]mappedValue),
	}
}

// Invoke attempts to call the interface{} provided as a function,
// providing dependencies for function arguments based on Type.
// Returns a slice of reflect.Value representing the returned values of the function.
// Returns an error if the injection fails.
// It panics if f is not a function
func (inj *injector) Invoke(f interface{}) ([]reflect.Value, error) {
	t := reflect.TypeOf(f)

	var in = make([]reflect.Value, t.NumIn()) //Panic if t is not kind of Func
	for i := 0; i < t.NumIn(); i++ {
		argType := t.In(i)
		val := inj.Get(argType)
		if !val.IsValid() {
			return nil, fmt.Errorf("Value not found for type %v", argType)
		}

		in[i] = val
	}

	return reflect.ValueOf(f).Call(in), nil
}

// Maps dependencies in the Type map to each field in the struct
// that is tagged with 'inject'.
// Returns an error if the injection fails.
func (inj *injector) Apply(val interface{}) error {
	v := reflect.ValueOf(val)

	for v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	if v.Kind() != reflect.Struct {
		return nil // Should not panic here ?
	}

	t := v.Type()

	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		structField := t.Field(i)
		if f.CanSet() && (structField.Tag == "inject" || structField.Tag.Get("inject") != "") {
			ft := f.Type()
			v := inj.Get(ft)
			if !v.IsValid() {
				return fmt.Errorf("Value not found for type %v", ft)
			}

			f.Set(v)
		}

	}

	return nil
}

// Maps the concrete value of val to its dynamic type using reflect.TypeOf,
// It returns the TypeMapper registered in.
func (i *injector) Map(val interface{}) TypeMapper {
	i.values[reflect.TypeOf(val)] = literalValue(reflect.ValueOf(val))
	return i
}

func (i *injector) MapTo(val interface{}, ifacePtr interface{}) TypeMapper {
	i.values[InterfaceOf(ifacePtr)] = literalValue(reflect.ValueOf(val))
	return i
}

func (inj *injector) MapProvider(provider interface{}) TypeMapper {
	t := reflect.TypeOf(provider)

	// t.NumOut panics if t is not of Kind “Func”.
	for i := 0; i < t.NumOut(); i++ {
		inj.values[t.Out(i)] = providedValue{provider, i}
	}

	return inj
}

// Maps the given reflect.Type to the given reflect.Value and returns
// the Typemapper the mapping has been registered in.
func (i *injector) Set(typ reflect.Type, val reflect.Value) TypeMapper {
	i.values[typ] = literalValue(val)
	return i
}

// getConfig is the configuration for a type mapper Get operation.
type getConfig struct {
	// youngestInjector is the injector that application code called Get on. We
	// need to keep track of this so we can pass it into mappedValue.Get. If
	// injector.Get were to pass the receiver into mappedValue.Get, then as we
	// traversed parent injectors, we'd progressively limit ourselves to a
	// smaller and smaller set of injectable values, and fewer and fewer
	// request-scoped values which value providers might want (e.g.,
	// context.Context, *http.Request). By hanging onto a reference to the
	// first injector used, value providers mapped to injectors higher up the
	// chain can still be invoked with arguments which were mapped to child
	// injectors:
	//
	//     type foo struct{ *http.Request }
	//     fooProvider := func(req *http.Request) *foo {
	//         return &foo{req}
	//     }
	//
	//     root := inject.New()
	//     root.MapProvider(fooProvider)
	//
	//     child := inject.New()
	//     child.SetParent(root)
	//     if req, err := http.NewRequest(http.MethodGet, "/", nil); err == nil {
	//          child.Map(req)
	//     }
	//
	//     _, _ = child.Invoke(func(f *foo) {
	//         // f.Request is the one previously mapped on the child injector,
	//         // even though the provider for foo was mapped on the root.
	//     }
	youngestInjector *injector
}

type getOptionsFunc func(*getConfig)

func withYoungestInjector(i *injector) getOptionsFunc {
	return func(config *getConfig) {
		config.youngestInjector = i
	}
}

func (i *injector) Get(t reflect.Type, options ...interface{}) reflect.Value {
	config := &getConfig{
		youngestInjector: i,
	}
	for _, option := range options {
		switch option := option.(type) {
		case getOptionsFunc:
			option(config)
		default:
			panic(fmt.Errorf("unrecognized Get option: %v", option))
		}
	}

	if val, ok := i.values[t]; ok {
		return val.Get(config.youngestInjector)
	}

	// no concrete types found, try to find implementors
	// if t is an interface
	if t.Kind() == reflect.Interface {
		for k, v := range i.values {
			if k.Implements(t) {
				return v.Get(config.youngestInjector)
			}
		}
	}

	// Still no type found, try to look it up on the parent
	if i.parent != nil {
		return i.parent.Get(t, withYoungestInjector(config.youngestInjector))
	}

	return reflect.Value{}
}

func (i *injector) SetParent(parent Injector) {
	i.parent = parent
}
