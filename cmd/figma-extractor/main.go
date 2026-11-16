package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	figmaextractor "github.com/hellenic-development/figma-extractor"
	"github.com/hellenic-development/figma-extractor/pkg/figma"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

const version = figma.Version

var (
	figmaURL      string
	accessToken   string
	outputFile    string
	nodeIDs       string
	outline       bool
	depth         int
	exportImages  bool
	imageFormat   string
	imageScales   string
	imageDir      string
	componentTree bool
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

// newRootCmd builds the command. A function of its own so a test can run it.
func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "figma-extractor",
		Short: "Extract a design from a Figma file as markdown",
		Long:  "Reads a Figma file through the Figma API and writes its design tokens, components, layer trees and pictures as markdown a coding model can build from.",
		Run:   run,
	}

	rootCmd.Flags().StringVarP(&figmaURL, "url", "u", "", "Figma link to a file or a frame (required)")
	// The flag has no default on purpose: cobra prints a flag's default in
	// --help and in every usage error, and this one would be the token.
	rootCmd.Flags().StringVarP(&accessToken, "token", "t", "", "Figma personal access token (when empty, the FIGMA_TOKEN environment variable is used)")
	rootCmd.Flags().StringVarP(&outputFile, "output", "o", "FIGMA_DESIGN_SPECIFICATIONS.md", "Output markdown file")
	rootCmd.Flags().StringVarP(&nodeIDs, "node-ids", "n", "", "Comma-separated node ids to extract instead of the ones in the link")
	rootCmd.Flags().BoolVar(&outline, "outline", false, "List the file's pages and frames instead of extracting a design")
	rootCmd.Flags().BoolVar(&componentTree, "component-tree", false, "Include each frame's layer tree")
	rootCmd.Flags().IntVar(&depth, "depth", 0, "Cut each tree this many levels down (0 keeps everything)")
	rootCmd.Flags().BoolVar(&exportImages, "export-images", false, "Save screenshots, photos, icons and exported layers")
	rootCmd.Flags().StringVar(&imageFormat, "image-format", "", "Format for icons and exported layers: png, svg, jpg, pdf (default: SVG for icons, the designer's choice for exports)")
	rootCmd.Flags().StringVar(&imageScales, "image-scales", "1", "Comma-separated scale factors for raster exports (e.g. \"1,2\")")
	rootCmd.Flags().StringVar(&imageDir, "image-dir", "figma-assets", "Folder for the saved pictures")

	rootCmd.MarkFlagRequired("url")

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("figma-extractor version %s\n", version)
		},
	})
	return rootCmd
}

func run(cmd *cobra.Command, args []string) {
	green := color.New(color.FgGreen)
	red := color.New(color.FgRed)
	cyan := color.New(color.FgCyan)

	if accessToken == "" {
		accessToken = os.Getenv("FIGMA_TOKEN")
	}
	if accessToken == "" {
		red.Println("Error: no token. Pass --token or set FIGMA_TOKEN.")
		os.Exit(1)
	}

	scales, err := figmaextractor.ParseScales(imageScales)
	if err != nil {
		red.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	var parsedNodeIDs []string
	if nodeIDs != "" {
		parsedNodeIDs = figmaextractor.ParseNodeIDs(nodeIDs)
	}

	// Ctrl+C stops the requests and the downloads in flight.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	result, err := figmaextractor.Run(ctx, figmaextractor.Options{
		AccessToken:   accessToken,
		FileURL:       figmaURL,
		NodeIDs:       parsedNodeIDs,
		Outline:       outline,
		ComponentTree: componentTree,
		Depth:         depth,
		ExportImages:  exportImages,
		ImageFormat:   imageFormat,
		ImageScales:   scales,
		ImageDir:      imageDir,
		Logger:        &cliLogger{},
	})
	if err != nil {
		red.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	cyan.Printf("\n%s\n", result.FileName)
	if d := result.Design; d != nil {
		fmt.Printf("  Frames: %d, layers: %d (%d hidden skipped, %d icons collapsed)\n", len(d.Frames), d.Stats.Layers, d.Stats.Hidden, d.Stats.Icons)
		fmt.Printf("  Named styles: %d, colours: %d, text styles: %d, components: %d\n", len(d.Tokens.Styles), len(d.Tokens.Colors), len(d.Tokens.Text), len(d.Components))
	}
	if len(result.Assets) > 0 {
		fmt.Printf("  Pictures saved: %d\n", len(result.Assets))
	}
	fmt.Printf("  Figma requests: %d Tier 1, %d Tier 2\n", result.Requests.Tier1, result.Requests.Tier2)

	if err := os.WriteFile(outputFile, []byte(result.Markdown), 0o644); err != nil {
		red.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	green.Printf("\nWrote %s\n", outputFile)
}

// cliLogger implements figmaextractor.Logger with colored terminal output.
type cliLogger struct{}

func (l *cliLogger) Infof(format string, args ...any) {
	color.New(color.FgYellow).Printf(format+"\n", args...)
}

func (l *cliLogger) Warnf(format string, args ...any) {
	color.New(color.FgYellow).Printf("Warning: "+format+"\n", args...)
}

func (l *cliLogger) Errorf(format string, args ...any) {
	color.New(color.FgRed).Printf("Error: "+format+"\n", args...)
}
