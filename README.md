# Figma Design Extractor

Reads a Figma file through the Figma REST API and writes its design as markdown that an AI coding assistant can build from: the design tokens the frames really use, the components, a layer tree with layout and text, and the pictures.

It is a command-line tool and a Go library. The library is what [Plexon](https://plexon.ai) uses for its Figma tool.

## What it extracts

- Design tokens. The styles the designer named in Figma (`Neutral/Dark/Light`, `Heading/H2`) come first, with their values. Every other colour, text style, spacing value, radius, shadow and gradient follows as the raw value with how often it is used. Nothing is guessed from layer names and nothing is renamed onto a made-up scale.
- Components. Each component the frames hold instances of, with its variants and description.
- A layer tree per frame, one layer per line: size, auto layout (direction, gap, padding, alignment, wrap), sizing (fixed, hug, fill), position, fills with alpha, gradients, borders, radii, shadows, blurs, opacity, and the full text with its styled runs.
- Pictures. A screenshot of each frame, every photo as it was uploaded, each icon as one SVG, and the layers the designer set up for export.
- An outline. The pages of a file and the frames on them with their ids and sizes, for picking frames before asking for a tree.

Hidden layers are skipped and counted. A group of vector paths is collapsed into one icon line instead of being listed path by path.

## Installation

The only requirement is the [Go Programming Language](https://go.dev/dl/), version 1.27 or newer.

```bash
go install github.com/hellenic-development/figma-extractor/cmd/figma-extractor@latest
```

## Getting a Figma token

1. Log in to Figma and open **Settings**, then **Security**.
2. Under **Personal access tokens**, choose **Generate new token**.
3. Give it the **File content: Read** scope and copy it. Figma shows it once.

Pass it with `--token`, or set `FIGMA_TOKEN` and leave the flag out.

## Usage

Copy a link to a frame in Figma (right-click the frame, **Copy link to selection**) and pass it:

```bash
figma-extractor \
  --url "https://www.figma.com/design/FILE_KEY/Name?node-id=12-34" \
  --component-tree \
  --export-images
```

This writes `FIGMA_DESIGN_SPECIFICATIONS.md` and a `figma-assets` folder beside it.

For a file you do not know yet, list its pages and frames first. It is one small request:

```bash
figma-extractor --url "https://www.figma.com/design/FILE_KEY/Name" --outline
```

Then extract the frames you want by id. Ids can be written `12:34` or `12-34`:

```bash
figma-extractor --url "https://www.figma.com/design/FILE_KEY/Name" --node-ids "12:34,56:78" --component-tree
```

Without `--component-tree` the output holds the tokens, the components and the list of frames. That is enough for a question like "what colours and type does this design use".

A large frame can be cut at a depth. Each layer at the limit says how many children were left out and which id to ask for:

```bash
figma-extractor --url "..." --component-tree --depth 3
```

### Options

| Flag | Meaning |
|---|---|
| `--url`, `-u` | Link to a Figma file or frame. Required. |
| `--token`, `-t` | Personal access token. Defaults to `$FIGMA_TOKEN`. |
| `--output`, `-o` | Markdown file to write. Default `FIGMA_DESIGN_SPECIFICATIONS.md`. |
| `--node-ids`, `-n` | Comma-separated node ids, used instead of the ones in the link. |
| `--outline` | List the pages and frames instead of extracting a design. |
| `--component-tree` | Include each frame's layer tree. |
| `--depth` | Cut each tree this many levels down. `0` keeps everything. |
| `--export-images` | Save screenshots, photos, icons and exported layers. |
| `--image-format` | `png`, `jpg`, `svg` or `pdf` for icons and exported layers. Default: SVG for icons, the designer's own setting for exported layers. |
| `--image-scales` | Scale factors for raster exports, for example `"1,2"`. Default `1`. |
| `--image-dir` | Folder for the pictures. Default `figma-assets`. |

Links to a file, a frame, a prototype, a FigJam board, a branch and an embed all work. A Community link does not: open it in Figma, copy it to your drafts and use the copy's link.

## What the output looks like

```
frame "Card" #1:2 360x672 col bg=#FFFFFF border="1px #E2E8F0" radius=16 shadow="0 8px 24px -4px #0F172A0D"{Elevation/Card} clip
  image "Photo" #1:3 360x200 w=fill fit=cover src=photo.jpg radius=16,16,0,0
  frame "Body" #1:6 360x472 col gap=12 pad=24 w=fill h=hug (+1 hidden)
    icon "spark" #1:7 16x16 src=spark.svg
    text "Heading" #1:10 312x96 w=fill font="Inter 600 24/32 ls=-0.24"{Heading/H2} color=#0F172A{Neutral/Ink 900}
      "Plan the sprint, not the meeting"
    instance "Button" #1:13 312x48 of="Button" (Size=Large, Type=Primary) row gap=8 pad=0,20 justify=center align=center w=fill bg="linear-gradient(90deg, #4F46E5 0%, #7C3AED 100%)" radius=10
```

One line is one layer. `{Name}` after a value is the Figma style it comes from. The document opens with a short legend, then the token tables, then one section per frame that stands on its own (link, size, screenshot, files, tree), so a long result can be read in parts.

The same file always gives the same text: there is no timestamp in it and nothing depends on map order.

## What it costs in Figma requests

Figma meters its API. Reading a file or nodes and rendering images are its most expensive tier: about 10 to 20 requests a minute on a Full or Dev seat, and only a handful a month on a View or Collab seat. The tool is built around that:

- A frame is one request. The whole file is never fetched to read a frame.
- With `--export-images`, one render request per image format in use (usually PNG and SVG), and one cheaper request for all the photos together.
- An outline is one request.
- A rate limit that frees in a few seconds is waited out. One that frees in hours comes back at once, as an error that says when.

The command prints how many requests it spent.

## Using it as a Go library

```go
import (
	figmaextractor "github.com/hellenic-development/figma-extractor"
	"github.com/hellenic-development/figma-extractor/pkg/figma"
)

result, err := figmaextractor.Run(ctx, figmaextractor.Options{
	AccessToken:   os.Getenv("FIGMA_TOKEN"),
	FileURL:       "https://www.figma.com/design/FILE_KEY/Name?node-id=12-34",
	ComponentTree: true,
	ExportImages:  true,
	ImageDir:      "figma-assets",
})
if err != nil {
	if figma.KindOf(err) == figma.KindTokenRejected {
		// ask for a new token
	}
	return err
}
os.WriteFile("design.md", []byte(result.Markdown), 0o644)
```

- `Run` stops when its context ends, including the downloads in flight. A stopped download leaves no half-written file.
- A refusal from Figma is a `*figma.APIError` with a `Kind`: rejected token, missing scope, not found, rate limited (with `RetryAfter`), too large, render failed. Test for it with `figma.KindOf(err)`.
- A node that is missing, or a picture that could not be rendered or downloaded, does not fail the call. It is a line in `result.Warnings` and in the markdown.
- `result.Design` holds the frames and tokens as Go values, `result.Assets` the files written, `result.Requests` what the call cost.
- `FromNodes` builds the same result from a nodes response you already have, without a request.
- `figma.Client` is usable on its own: `File`, `Nodes`, `Images`, `ImageFills`, `Download`, and a generic `Get` for decoding into your own types. `WithHTTPClient`, `WithBaseURL` and `WithMaxBodyBytes` configure it.

## Testing

```bash
go test -race ./...
```

Every test runs against a local test server. Two more are opt-in:

- `TestReplay` runs the whole pipeline over answers recorded from a real file, so a change to the output can be judged on a real design without spending requests. Its comment in `replay_test.go` says how to record.
- `_examples/basic` extracts a real frame. It needs `FIGMA_TOKEN` and spends about five requests: `FIGMA_TOKEN=... go test -v ./_examples/basic/`.

## Limits

- Figma variables are not read. Their names need an endpoint only Enterprise tokens may call. Values still arrive inline on the layers.
- Vector paths are not written out. An icon is exported as an SVG file instead.
- A render is capped by Figma at 32 megapixels and scaled down past that. The pixel sizes in the output are read from the saved files.
- A very large page can produce a very large tree. Use `--outline` and then single frames, or `--depth`.

## License

This project is licensed under the [BSD 3-clause license](LICENSE), just like the Go project itself.
