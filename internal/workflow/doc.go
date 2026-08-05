// Package workflow holds operations that span more than one resource, such as
// adding an issue to a sprint.
//
// Resources never import each other. An operation that needs two of them lives
// here or in the calling layer, so the resource packages stay independent and
// individually compilable.
package workflow
