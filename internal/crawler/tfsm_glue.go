// internal/crawler/tfsm_glue.go
// Indirection over tfsm.Parse so tests can substitute fixture parsing.
package crawler

import "github.com/scottpeterman/pathfinderssh/internal/tfsm"

var tfsmParse = tfsm.Parse
