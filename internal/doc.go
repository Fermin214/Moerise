// Package moerise implements the shared application services used by the CLI,
// browser API, read-only REST/MCP, and messaging adapters.
//
// App owns service composition and runtime changes. Source is the boundary to
// Memos; Store separates durable product state from rebuildable indexes.
// See docs/CODEBASE.md for the source map and contributor reading order.
package moerise
