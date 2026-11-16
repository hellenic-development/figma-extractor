// Package figmaextractor reads a Figma file through the Figma REST API and
// returns its design as markdown that an AI coding assistant can build from,
// along with the same design as Go values.
//
// The CLI lives in cmd/figma-extractor; this root package exposes the same
// pipeline as a Go API so that callers can embed extraction in their own
// tools without shelling out.
//
// # Import
//
// The module path contains a hyphen but Go package names cannot, so the
// package is named figmaextractor:
//
//	import "github.com/hellenic-development/figma-extractor" // package figmaextractor
//
// # Quick start
//
//	result, err := figmaextractor.Run(ctx, figmaextractor.Options{
//	    AccessToken:   os.Getenv("FIGMA_TOKEN"),
//	    FileURL:       "https://www.figma.com/design/ABC123/My-Design?node-id=12-34",
//	    ComponentTree: true,
//	    ExportImages:  true,
//	    ImageDir:      "figma-assets",
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	os.WriteFile("design.md", []byte(result.Markdown), 0o644)
//
// # What comes back
//
// [Result.Markdown] is the document for a reader. [Result.Design] holds the
// same content as values: the frames as trees of layers and the design
// tokens they use, named styles first. [Result.Assets] lists the files that
// were written and [Result.Requests] what the call cost in Figma requests.
//
// # Choosing what to read
//
// A link to a frame extracts that frame. [Options.NodeIDs] names other
// nodes, in either form ("12:34" or "12-34"). A link to a whole file with
// no node ids reads the whole file, which for a large one is slow and
// large: set [Options.Outline] first to get the map of the file (its pages
// and frames, one cheap request) and then ask for the frames you need.
// [Options.Depth] cuts a large tree.
//
// # Requests are scarce
//
// Figma meters its API, and reading nodes or rendering images is the
// expensive tier: a handful of requests a month on a View seat. A frame is
// one request, and with [Options.ExportImages] one more per image format
// in use plus a single cheaper one for all the photos. A rate limit that
// frees in seconds is waited out; one that frees later is returned at once
// as a [figma.APIError] carrying when.
//
// # Errors and warnings
//
// A refusal from Figma is a *[figma.APIError]; [figma.KindOf] tells a
// rejected token from a missing file from a rate limit. A node that is not
// in the file, or a picture that could not be rendered or downloaded, does
// not fail the call: it is a line in [Result.Warnings]. Everything stops
// when the context ends, and a stopped download leaves no half-written
// file.
//
// # Logging
//
// Pass a [Logger] implementation in [Options.Logger] to receive progress
// messages. A nil Logger silences all output.
//
//	type myLogger struct{}
//	func (l *myLogger) Infof(f string, a ...any)  { log.Printf("[INFO]  "+f, a...) }
//	func (l *myLogger) Warnf(f string, a ...any)  { log.Printf("[WARN]  "+f, a...) }
//	func (l *myLogger) Errorf(f string, a ...any) { log.Printf("[ERROR] "+f, a...) }
package figmaextractor
