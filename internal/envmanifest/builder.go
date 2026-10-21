package envmanifest

// Builder assembles a Manifest from derived emitter output. The zero value
// is usable, but NewBuilder is the documented entry point.
type Builder struct {
	always           []string
	conditional      map[string]string
	runtimeSpecific  map[string][]string
	harnessDelivered map[string]HarnessDelivered
	reserved         []string
}

// NewBuilder returns an empty manifest builder.
func NewBuilder() *Builder {
	return &Builder{
		conditional:      map[string]string{},
		runtimeSpecific:  map[string][]string{},
		harnessDelivered: map[string]HarnessDelivered{},
	}
}

// AddAlways records variables the runner exports on every run.
func (b *Builder) AddAlways(names ...string) *Builder {
	b.always = append(b.always, names...)
	return b
}

// AddConditional records a variable the runner exports only when gate holds.
// gate describes the condition; it is contract metadata, never a value.
func (b *Builder) AddConditional(name, gate string) *Builder {
	if b.conditional == nil {
		b.conditional = map[string]string{}
	}
	b.conditional[name] = gate
	return b
}

// AddRuntimeSpecific records the variables a runtime's EnvExports emits. A
// runtime that exports nothing is recorded with an empty set rather than
// omitted.
func (b *Builder) AddRuntimeSpecific(runtime string, names ...string) *Builder {
	if b.runtimeSpecific == nil {
		b.runtimeSpecific = map[string][]string{}
	}
	b.runtimeSpecific[runtime] = append(b.runtimeSpecific[runtime], names...)
	return b
}

// AddHarnessDelivered records a variable delivered by stock harness
// configuration, with provenance describing where the delivery happens. The
// entry is labeled not-runner-guaranteed.
func (b *Builder) AddHarnessDelivered(name, provenance string) *Builder {
	if b.harnessDelivered == nil {
		b.harnessDelivered = map[string]HarnessDelivered{}
	}
	b.harnessDelivered[name] = HarnessDelivered{Provenance: provenance}
	return b
}

// AddReserved records variables reserved from harness override.
func (b *Builder) AddReserved(names ...string) *Builder {
	b.reserved = append(b.reserved, names...)
	return b
}

// Build returns the manifest stamped with the current schema version and
// every category in canonical form.
func (b *Builder) Build() Manifest {
	return Manifest{
		SchemaVersion: SchemaVersion,
		Exports: Exports{
			Always:          b.always,
			Conditional:     b.conditional,
			RuntimeSpecific: b.runtimeSpecific,
		},
		HarnessDelivered: b.harnessDelivered,
		Reserved:         b.reserved,
	}.normalized()
}
