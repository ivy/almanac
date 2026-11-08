// Package seal links package hostile to hostile/reveal. Being internal, only packages under
// hostile/ can import it.
package seal

// Open returns the value inside a hostile.Of. Package hostile sets it when it initialises.
var Open func(h any) any
