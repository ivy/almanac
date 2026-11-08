package taint

// MayReveal exposes the boundary list to specs, which cannot place a package at a boundary's
// import path in analysis test data.
var MayReveal = mayReveal
